package consensus

import (
	"context"
	"dex/interfaces"
	"dex/logs"
	"dex/pb"
	"dex/types"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ============================================
// 共识引擎
// ============================================

type SnowmanEngine struct {
	mu            sync.RWMutex
	nodeID        types.NodeID
	store         interfaces.BlockStore
	config        *ConsensusConfig
	events        interfaces.EventBus
	snowballs     map[uint64]*Snowball
	activeQueries map[string]*QueryContext
	preferences   map[uint64]string
	Logger        logs.Logger
}

type QueryContext struct {
	queryKey   string
	blockID    string
	votes      map[string]int
	voters     map[types.NodeID]string // nodeID -> preferredBlockID
	signatures map[types.NodeID][]byte // VRF chit signatures: nodeID -> signature bytes
	responded  int
	startTime  time.Time
	height     uint64
	seqID      uint32
}

func NewSnowmanEngine(nodeID types.NodeID, store interfaces.BlockStore, config *ConsensusConfig, events interfaces.EventBus, logger logs.Logger) interfaces.ConsensusEngine {
	return &SnowmanEngine{
		nodeID:        nodeID,
		store:         store,
		config:        config,
		events:        events,
		snowballs:     make(map[uint64]*Snowball),
		activeQueries: make(map[string]*QueryContext),
		preferences:   make(map[uint64]string),
		Logger:        logger,
	}
}

func (e *SnowmanEngine) Start(ctx context.Context) error {
	// 初始化创世区块的Snowball
	e.mu.Lock()
	genesisSB := NewSnowball(e.events)
	genesisSB.Finalize()
	e.snowballs[0] = genesisSB
	e.preferences[0] = "genesis"
	e.mu.Unlock()

	// 定期检查超时
	go func() {
		logs.SetThreadLogger(e.Logger)
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				e.checkTimeouts()
			case <-ctx.Done():
				return
			}
		}
	}()

	return nil
}

func (e *SnowmanEngine) RegisterQuery(nodeID types.NodeID, requestID uint32, blockID string, height uint64, seqID uint32) string {
	e.mu.Lock()
	defer e.mu.Unlock()

	queryKey := fmt.Sprintf("%s-%d", nodeID, requestID)
	e.activeQueries[queryKey] = &QueryContext{
		queryKey:   queryKey,
		blockID:    blockID,
		votes:      make(map[string]int),
		voters:     make(map[types.NodeID]string),
		signatures: make(map[types.NodeID][]byte),
		startTime:  time.Now(),
		height:     height,
		seqID:      seqID,
	}

	return queryKey
}

// SubmitChit 提交来自特定节点的投票响应（Chit）
func (e *SnowmanEngine) SubmitChit(nodeID types.NodeID, queryKey string, preferredID string, chitSignature []byte) {
	var completedCtx *QueryContext
	shouldProcess := false
	e.mu.Lock()
	ctx, exists := e.activeQueries[queryKey]
	if !exists {
		e.mu.Unlock()
		return
	}
	if _, voted := ctx.voters[nodeID]; voted {
		e.mu.Unlock()
		return
	}
	ctx.voters[nodeID] = preferredID
	ctx.votes[preferredID]++
	ctx.responded++
	if len(chitSignature) > 0 {
		ctx.signatures[nodeID] = chitSignature
	}
	hasWinner := preferredID != "" && ctx.votes[preferredID] >= e.config.Alpha
	if hasWinner || ctx.responded >= e.config.K {
		delete(e.activeQueries, queryKey)
		completedCtx = ctx
		shouldProcess = true
	}
	e.mu.Unlock()
	if !shouldProcess {
		return
	}
	// Process outside e.mu to avoid holding the engine lock on finalize-heavy path.
	reason := e.processVotes(completedCtx)
	e.events.PublishAsync(types.BaseEvent{
		EventType: types.EventQueryComplete,
		EventData: QueryCompleteData{Reason: reason, QueryKeys: []string{queryKey}},
	})
}
func (e *SnowmanEngine) processVotes(ctx *QueryContext) string {
	sb := e.getOrCreateSnowball(ctx.height)

	// Only blocks whose parent is already finalized can participate.
	var parentBlock *types.Block
	if ctx.height > 0 {
		parent, ok := e.store.GetFinalizedAtHeight(ctx.height - 1)
		if !ok {
			logs.Debug("[Engine] Parent block at height %d not finalized, skipping vote processing for height %d",
				ctx.height-1, ctx.height)
			return "parent_missing"
		}
		parentBlock = parent
	}

	candidates := make([]string, 0)
	blocks := e.store.GetByHeight(ctx.height)
	for _, block := range blocks {
		if ctx.height > 0 && parentBlock != nil {
			if block.Header.ParentID != parentBlock.ID {
				logs.Debug("[Engine] Block %s rejected from candidates: parent mismatch (expected %s, got %s)",
					block.ID, parentBlock.ID, block.Header.ParentID)
				continue
			}
		}
		candidates = append(candidates, block.ID)
	}

	if len(candidates) == 0 {
		logs.Debug("[Engine] No valid candidates for height %d (all blocks have wrong parent)", ctx.height)
		return "candidates_missing"
	}

	candidateSet := make(map[string]bool, len(candidates))
	for _, id := range candidates {
		candidateSet[id] = true
	}
	filteredVotes := make(map[string]int, len(ctx.votes))
	droppedVotes := 0
	for id, count := range ctx.votes {
		if candidateSet[id] {
			filteredVotes[id] = count
		} else {
			droppedVotes += count
		}
	}
	if droppedVotes > 0 {
		logs.Debug("[Engine] Dropped %d vote(s) for non-candidate blocks at height %d (query=%s)",
			droppedVotes, ctx.height, ctx.queryKey)
	}

	voteDetails := make([]types.FinalizationChit, 0, len(ctx.voters))
	for nodeID, preferredID := range ctx.voters {
		voteDetails = append(voteDetails, types.FinalizationChit{
			NodeID:      string(nodeID),
			PreferredID: preferredID,
			Timestamp:   ctx.startTime.UnixMilli(),
			Signature:   ctx.signatures[nodeID], // nil if no signature
		})
	}
	sb.RecordVoteWithDetails(candidates, filteredVotes, e.config.Alpha, ctx.seqID, voteDetails)

	newPreference := sb.GetPreference()
	if newPreference != "" {
		e.mu.Lock()
		e.preferences[ctx.height] = newPreference
		e.mu.Unlock()
	}

	if sb.CanFinalize(e.config.Beta) && newPreference != "" {
		chits := e.collectFinalizationChits(ctx.height, newPreference)
		sigSet := e.collectConsensusSignatureSet(ctx.height, newPreference)
		e.finalizeBlock(ctx.height, newPreference, chits, sigSet)
	}
	return "success"
}

// collectFinalizationChits 从 Snowball 获取有效轮次的投票历史
func (e *SnowmanEngine) getOrCreateSnowball(height uint64) *Snowball {
	e.mu.Lock()
	defer e.mu.Unlock()
	sb, exists := e.snowballs[height]
	if !exists {
		sb = NewSnowball(e.events)
		e.snowballs[height] = sb
	}
	return sb
}

func (e *SnowmanEngine) collectFinalizationChits(height uint64, blockID string) *types.FinalizationChits {
	e.mu.RLock()
	sb := e.snowballs[height]
	e.mu.RUnlock()
	if sb == nil {
		return nil
	}

	successHistory := sb.GetSuccessHistory()

	// 转换为 types.RoundChits 格式
	rounds := make([]types.RoundChits, 0, len(successHistory))
	allChits := make([]types.FinalizationChit, 0)
	for _, sr := range successHistory {
		rc := types.RoundChits{
			Round:     sr.Round,
			Timestamp: sr.Timestamp,
			Votes:     sr.Votes,
		}
		rounds = append(rounds, rc)
		allChits = append(allChits, sr.Votes...)
	}

	return &types.FinalizationChits{
		BlockID:     blockID,
		Height:      height,
		TotalRounds: len(rounds),
		Rounds:      rounds,
		TotalVotes:  len(allChits),
		Chits:       allChits,
		FinalizedAt: time.Now().UnixMilli(),
	}
}

// collectConsensusSignatureSet 从 Snowball 有效轮次历史中提取签名集合
func (e *SnowmanEngine) collectConsensusSignatureSet(height uint64, blockID string) *pb.ConsensusSignatureSet {
	e.mu.RLock()
	sb := e.snowballs[height]
	e.mu.RUnlock()
	if sb == nil {
		return nil
	}

	// 获取父块信息作为锚点
	var parentID string
	if height > 0 {
		if parent, ok := e.store.GetFinalizedAtHeight(height - 1); ok {
			parentID = parent.ID
		}
	}

	// 计算 VRF seed（与 queryManager 中一致）
	var vrfSeed []byte
	if block, ok := e.store.Get(blockID); ok {
		vrfSeed = computeVRFSeed(parentID, height, block.Header.Window, e.nodeID)
	}

	successHistory := sb.GetSuccessHistory()
	rounds := make([]*pb.RoundSignatures, 0, len(successHistory))
	for _, sr := range successHistory {
		chitSigs := make([]*pb.ChitSignature, 0, len(sr.Votes))
		for _, v := range sr.Votes {
			chitSigs = append(chitSigs, &pb.ChitSignature{
				NodeId:      v.NodeID,
				PreferredId: v.PreferredID,
				Signature:   v.Signature,
			})
		}
		rounds = append(rounds, &pb.RoundSignatures{
			SeqId:      sr.SeqID,
			Signatures: chitSigs,
		})
	}

	return &pb.ConsensusSignatureSet{
		BlockId:  blockID,
		Height:   height,
		ParentId: parentID,
		VrfSeed:  vrfSeed,
		Rounds:   rounds,
	}
}

func (e *SnowmanEngine) finalizeBlock(height uint64, blockID string, chits *types.FinalizationChits, sigSet *pb.ConsensusSignatureSet) {
	if _, exists := e.store.Get(blockID); !exists {
		logs.Warn("[Engine] Finalize skipped: block %s not found at height %d", blockID, height)
		return
	}
	if err := e.store.SetFinalized(height, blockID); err != nil {
		if errors.Is(err, ErrAlreadyFinalized) {
			return
		}
		logs.Warn("[Engine] Finalize failed: block %s at height %d: %v", blockID, height, err)
		return
	}

	// 存储 chits 和签名集合到 RealBlockStore（如果支持）
	if realStore, ok := e.store.(*RealBlockStore); ok {
		if chits != nil {
			realStore.SetFinalizationChits(height, chits)
		}
		if sigSet != nil {
			realStore.SetSignatureSet(height, sigSet)
		}
	}

	e.mu.RLock()
	sb := e.snowballs[height]
	e.mu.RUnlock()
	if sb != nil {
		sb.Finalize()
	}

	if block, exists := e.store.Get(blockID); exists {
		Logf("[Engine] 🎉 Finalized block %s at height %d\n", blockID, height)
		// 发布包含 chits 的事件
		e.events.PublishAsync(types.BaseEvent{
			EventType: types.EventBlockFinalized,
			EventData: &types.BlockFinalizedData{
				Block: block,
				Chits: chits,
			},
		})
	}

}

type QueryCompleteData struct {
	Reason    string   // "success" | "timeout"
	QueryKeys []string // 结束的查询键（可选）
}

func (e *SnowmanEngine) checkTimeouts() {
	now := time.Now()
	expiredCtxs := make([]*QueryContext, 0)
	var expiredKeys []string
	e.mu.Lock()
	for k, ctx := range e.activeQueries {
		if now.Sub(ctx.startTime) > e.config.QueryTimeout {
			expiredCtxs = append(expiredCtxs, ctx)
			expiredKeys = append(expiredKeys, k)
			delete(e.activeQueries, k)
		}
	}
	e.mu.Unlock()
	// Process timed-out query votes outside e.mu.
	for _, ctx := range expiredCtxs {
		e.processVotes(ctx)
	}
	if len(expiredCtxs) > 0 {
		logs.Debug("[Engine] Query timeout: %d expired. Still processed available votes before deletion.", len(expiredCtxs))
		e.events.PublishAsync(types.BaseEvent{
			EventType: types.EventQueryComplete,
			EventData: QueryCompleteData{Reason: "timeout", QueryKeys: expiredKeys},
		})
	}
}
func (e *SnowmanEngine) GetActiveQueryCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.activeQueries)
}

func (e *SnowmanEngine) GetPreference(height uint64) string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if pref, exists := e.preferences[height]; exists {
		return pref
	}

	if sb, exists := e.snowballs[height]; exists {
		return sb.GetPreference()
	}

	return ""
}

// HeightState 表示某个高度的共识状态
type HeightState struct {
	Height     uint64
	Preference string
	Confidence int
	Finalized  bool
	LastVotes  map[string]int
}

// GetHeightState 获取指定高度的共识状态
func (e *SnowmanEngine) GetHeightState(height uint64) *HeightState {
	e.mu.RLock()
	defer e.mu.RUnlock()

	sb, exists := e.snowballs[height]
	if !exists {
		return nil
	}

	return &HeightState{
		Height:     height,
		Preference: sb.GetPreference(),
		Confidence: sb.GetConfidence(),
		Finalized:  sb.IsFinalized(),
		LastVotes:  sb.GetLastVotes(),
	}
}

// GetPendingHeightsState 获取所有未最终化高度的共识状态
func (e *SnowmanEngine) GetPendingHeightsState() []*HeightState {
	e.mu.RLock()
	defer e.mu.RUnlock()

	result := make([]*HeightState, 0)
	for height, sb := range e.snowballs {
		if !sb.IsFinalized() {
			result = append(result, &HeightState{
				Height:     height,
				Preference: sb.GetPreference(),
				Confidence: sb.GetConfidence(),
				Finalized:  false,
				LastVotes:  sb.GetLastVotes(),
			})
		}
	}
	return result
}


