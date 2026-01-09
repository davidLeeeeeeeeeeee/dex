package consensus

import (
	"context"
	"crypto/rand"
	"dex/interfaces"
	"dex/logs"
	"dex/types"
	"encoding/binary"
	"sort"
	"sync"
	"time"
)

// ============================================
// 查询管理器
// ============================================

type QueryManager struct {
	nodeID      types.NodeID
	node        *Node
	transport   interfaces.Transport
	store       interfaces.BlockStore
	engine      interfaces.ConsensusEngine
	config      *ConsensusConfig
	events      interfaces.EventBus
	Logger      logs.Logger
	activePolls sync.Map
	nextReqID   uint32
	mu          sync.Mutex
}

type Poll struct {
	requestID uint32
	blockID   string
	queryKey  string
	startTime time.Time
	height    uint64
}

func NewQueryManager(id types.NodeID, transport interfaces.Transport, store interfaces.BlockStore, engine interfaces.ConsensusEngine, config *ConsensusConfig, events interfaces.EventBus, logger logs.Logger) *QueryManager {
	qm := &QueryManager{
		nodeID:    id,
		transport: transport,
		store:     store,
		engine:    engine,
		config:    config,
		events:    events,
		Logger:    logger,
	}

	events.Subscribe(types.EventQueryComplete, func(e interfaces.Event) {
		data, ok := e.Data().(QueryCompleteData)
		if !ok {
			if ptr, okPtr := e.Data().(*QueryCompleteData); okPtr && ptr != nil {
				data = *ptr
				ok = true
			}
		}
		if !ok {
			logs.Debug("[QueryManager] QueryComplete event has unexpected data type: %T", e.Data())
			return
		}

		qm.cleanupPollsByQueryKeys(data.QueryKeys, data.Reason)
		if data.Reason == "timeout" && len(data.QueryKeys) > 0 {
			logs.Debug("[QueryManager] Re-issuing query after timeout (count=%d)", len(data.QueryKeys))
			qm.tryIssueQuery()
		}
		//只有timeout才需要清理
	})

	events.Subscribe(types.EventSyncComplete, func(e interfaces.Event) {
		qm.tryIssueQuery()
	})

	// 新增：快照加载后也触发查询
	events.Subscribe(types.EventSnapshotLoaded, func(e interfaces.Event) {
		qm.tryIssueQuery()
	})

	events.Subscribe(types.EventBlockReceived, func(e interfaces.Event) {
		qm.tryIssueQuery()
	})

	return qm
}

// 尝试发起查询
func (qm *QueryManager) tryIssueQuery() {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	_, currentHeight := qm.store.GetLastAccepted()
	nextHeight := currentHeight + 1

	blocks := qm.store.GetByHeight(nextHeight)
	if len(blocks) == 0 {
		return
	}

	if qm.engine.GetActiveQueryCount() >= qm.config.MaxConcurrentQueries {
		return
	}

	qm.issueQuery()
}

func (qm *QueryManager) issueQuery() {
	_, currentHeight := qm.store.GetLastAccepted()
	nextHeight := currentHeight + 1

	blocks := qm.store.GetByHeight(nextHeight)
	if len(blocks) == 0 {
		return
	}

	// 获取偏好区块ID
	blockID := qm.engine.GetPreference(nextHeight)
	if blockID != "" {
		if _, exists := qm.store.Get(blockID); !exists {
			logs.Warn("[QueryManager] Preferred block %s missing at height %d, fallback to candidates",
				blockID, nextHeight)
			blockID = ""
		}
	}
	if blockID == "" {
		candidates := make([]string, 0, len(blocks))
		for _, b := range blocks {
			candidates = append(candidates, b.ID)
		}
		sort.Strings(candidates)
		blockID = candidates[len(candidates)-1]
	}

	block, exists := qm.store.Get(blockID)
	if !exists {
		logs.Warn("[QueryManager] Block %s not found for query at height %d", blockID, nextHeight)
		return
	}
	requestID, _ := secureRandUint32()
	queryKey := qm.engine.RegisterQuery(qm.nodeID, requestID, blockID, block.Height)

	poll := &Poll{
		requestID: requestID,
		blockID:   blockID,
		queryKey:  queryKey,
		startTime: time.Now(),
		height:    block.Height,
	}
	qm.activePolls.Store(requestID, poll)

	peers := qm.transport.SamplePeers(qm.nodeID, qm.config.K)

	// 判断自己是否是该区块的提议者
	isProposer := (block.Proposer == string(qm.nodeID))

	var msg types.Message
	if isProposer {
		// 只有提议者发送PushQuery（携带完整区块）
		msg = types.Message{
			Type:      types.MsgPushQuery,
			From:      qm.nodeID,
			RequestID: requestID,
			BlockID:   blockID,
			Block:     block, // 携带完整区块数据
			Height:    block.Height,
		}
		logs.Debug("[Node %s] Sending PushQuery for block %s (I'm the proposer)",
			qm.nodeID, blockID)
	} else {
		// 非提议者发送PullQuery（只携带区块ID）
		msg = types.Message{
			Type:      types.MsgPullQuery,
			From:      qm.nodeID,
			RequestID: requestID,
			BlockID:   blockID,
			Height:    block.Height,
		}
		logs.Debug("[Node %s] Sending PullQuery for block %s",
			qm.nodeID, blockID)
	}

	qm.transport.Broadcast(msg, peers)

	if qm.node != nil {
		qm.node.Stats.Mu.Lock()
		qm.node.Stats.QueriesSent++
		qm.node.Stats.QueriesPerHeight[block.Height]++
		qm.node.Stats.Mu.Unlock()
	}
}

// 返回 [0, 2^32-1] 范围内的安全随机 uint32
func secureRandUint32() (uint32, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b[:]), nil
}

func (qm *QueryManager) HandleChit(msg types.Message) {
	if poll, ok := qm.activePolls.Load(msg.RequestID); ok {
		p := poll.(*Poll)
		if msg.PreferredID != "" {
			if _, exists := qm.store.Get(msg.PreferredID); !exists {
				logs.Warn("[QueryManager] Missing preferred block %s (h=%d) from %s; requesting",
					msg.PreferredID, msg.PreferredIDHeight, msg.From)
				_ = qm.transport.Send(types.NodeID(msg.From), types.Message{
					Type:      types.MsgGet,
					From:      qm.nodeID,
					RequestID: msg.RequestID,
					BlockID:   msg.PreferredID,
					Height:    msg.PreferredIDHeight,
				})
			}
		}
		qm.engine.SubmitChit(types.NodeID(msg.From), p.queryKey, msg.PreferredID)
	}
}

func (qm *QueryManager) cleanupPollsByQueryKeys(keys []string, reason string) {
	if len(keys) == 0 {
		return
	}
	keySet := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		keySet[k] = struct{}{}
	}
	removed := 0
	qm.activePolls.Range(func(k, v interface{}) bool {
		p := v.(*Poll)
		if _, ok := keySet[p.queryKey]; ok {
			qm.activePolls.Delete(k)
			removed++
		}
		return true
	})
	if removed > 0 {
		logs.Debug("[QueryManager] Cleaned %d poll(s) on query complete (reason=%s)", removed, reason)
	}
}

func (qm *QueryManager) Start(ctx context.Context) {
	go func() {
		logs.SetThreadNodeContext(string(qm.nodeID))
		time.Sleep(100 * time.Millisecond)
		for i := 0; i < qm.config.MaxConcurrentQueries; i++ {
			qm.tryIssueQuery()
		}
	}()

	go func() {
		logs.SetThreadNodeContext(string(qm.nodeID))
		ticker := time.NewTicker(107 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				qm.tryIssueQuery()
			case <-ctx.Done():
				return
			}
		}
	}()
}
