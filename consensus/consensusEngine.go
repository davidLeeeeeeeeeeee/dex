package consensus

import (
	"context"
	"dex/interfaces"
	"dex/logs"
	"dex/types"
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
	queryKey  string
	blockID   string
	votes     map[string]int
	voters    map[types.NodeID]bool
	responded int
	startTime time.Time
	height    uint64
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
		// DI 模式下不需要 SetThreadNodeContext，但为了兼容性仍可保留或直接用 Logger
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

func (e *SnowmanEngine) RegisterQuery(nodeID types.NodeID, requestID uint32, blockID string, height uint64) string {
	e.mu.Lock()
	defer e.mu.Unlock()

	queryKey := fmt.Sprintf("%s-%d", nodeID, requestID)
	e.activeQueries[queryKey] = &QueryContext{
		queryKey:  queryKey,
		blockID:   blockID,
		votes:     make(map[string]int),
		voters:    make(map[types.NodeID]bool),
		responded: 0,
		startTime: time.Now(),
		height:    height,
	}

	return queryKey
}

// SubmitChit 提交来自特定节点的投票响应（Chit）
func (e *SnowmanEngine) SubmitChit(nodeID types.NodeID, queryKey string, preferredID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// 检查查询是否存在，以及该节点是否已经对该查询投过票（防止重复计票）
	ctx, exists := e.activeQueries[queryKey]
	if !exists || ctx.voters[nodeID] {
		return
	}

	// 记录该节点的选票及其偏好的区块 ID
	ctx.voters[nodeID] = true
	ctx.votes[preferredID]++
	ctx.responded++ // 增加已收到的响应计数

	// --- 优化结算逻辑 ---
	// 判定是否结算的两个维度：
	// 1. 提前胜出：某个候选块已经获得了 Alpha 张票。此时无论后续 K-responded 结果如何，该块在这一轮都已经胜出。
	// 2. 采样完成：已经收到了全部 K 个预期的响应。此时无论各块票数如何，都必须根据当前统计结果由于 Snowball 进行状态更新。

	hasWinner := false
	if preferredID != "" && ctx.votes[preferredID] >= e.config.Alpha {
		hasWinner = true
	}

	if hasWinner || ctx.responded >= e.config.K {
		// 处理本次查询收集到的所有选票，并更新 Snowball 状态
		reason := e.processVotes(ctx)
		// 查询任务完成，从活跃查询映射中移除
		delete(e.activeQueries, queryKey)
		// 异步发布查询完成事件，通知系统其他部分
		e.events.PublishAsync(types.BaseEvent{
			EventType: types.EventQueryComplete,
			EventData: QueryCompleteData{Reason: reason, QueryKeys: []string{queryKey}},
		})
	}
}

func (e *SnowmanEngine) processVotes(ctx *QueryContext) string {
	sb, exists := e.snowballs[ctx.height]
	if !exists {
		sb = NewSnowball(e.events)
		e.snowballs[ctx.height] = sb
	}

	// 获取父区块（height-1 的已最终化区块）
	// 只有父区块已最终化的候选区块才能参与共识
	var parentBlock *types.Block
	if ctx.height > 0 {
		parent, ok := e.store.GetFinalizedAtHeight(ctx.height - 1)
		if !ok {
			// 父区块尚未最终化，无法对当前高度进行共识
			logs.Debug("[Engine] Parent block at height %d not finalized, skipping vote processing for height %d",
				ctx.height-1, ctx.height)
			return "parent_missing"
		}
		parentBlock = parent
	}

	// 候选区块：只包含那些 ParentID 指向已最终化父区块的区块
	candidates := make([]string, 0)
	blocks := e.store.GetByHeight(ctx.height)
	for _, block := range blocks {
		// 对于 height > 0 的区块，必须验证父区块链接
		if ctx.height > 0 && parentBlock != nil {
			if block.ParentID != parentBlock.ID {
				logs.Debug("[Engine] Block %s rejected from candidates: parent mismatch (expected %s, got %s)",
					block.ID, parentBlock.ID, block.ParentID)
				continue
			}
		}
		candidates = append(candidates, block.ID)
	}

	// 如果没有有效候选，直接返回
	if len(candidates) == 0 {
		logs.Debug("[Engine] No valid candidates for height %d (all blocks have wrong parent)", ctx.height)
		return "candidates_missing"
	}

	//核心：统计投票
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
	sb.RecordVote(candidates, filteredVotes, e.config.Alpha)

	newPreference := sb.GetPreference()
	if newPreference != "" {
		e.preferences[ctx.height] = newPreference
	}

	if sb.CanFinalize(e.config.Beta) && newPreference != "" {
		e.finalizeBlock(ctx.height, newPreference)
	}
	return "success"
}

func (e *SnowmanEngine) finalizeBlock(height uint64, blockID string) {
	if _, exists := e.store.Get(blockID); !exists {
		logs.Warn("[Engine] Finalize skipped: block %s not found at height %d", blockID, height)
		return
	}
	e.store.SetFinalized(height, blockID)

	sb := e.snowballs[height]
	if sb != nil {
		sb.Finalize()
	}

	if block, exists := e.store.Get(blockID); exists {
		Logf("[Engine] 🎉 Finalized block %s at height %d\n", blockID, height)
		e.events.PublishAsync(types.BaseEvent{
			EventType: types.EventBlockFinalized,
			EventData: block,
		})
	}
}

type QueryCompleteData struct {
	Reason    string   // "success" | "timeout"
	QueryKeys []string // 结束的查询键（可选）
}

func (e *SnowmanEngine) checkTimeouts() {
	e.mu.Lock()
	now := time.Now()
	var expiredCount int
	var expiredKeys []string

	// 找出所有超时的查询
	for k, ctx := range e.activeQueries {
		if now.Sub(ctx.startTime) > e.config.QueryTimeout {
			// 重要：即使超时，也要把当前收到的这些票处理掉（可能已经够 Alpha 了）
			e.processVotes(ctx)

			expiredKeys = append(expiredKeys, k)
			delete(e.activeQueries, k)
			expiredCount++
		}
	}
	e.mu.Unlock()

	if expiredCount > 0 {
		logs.Debug("[Engine] Query timeout: %d expired. Still processed available votes before deletion.", expiredCount)
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
