package consensus

import (
	"context"
	"dex/interfaces"
	"dex/logs"
	"dex/types"
	"sort"
	"sync"
	"sync/atomic"
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

func NewQueryManager(nodeID types.NodeID, transport interfaces.Transport, store interfaces.BlockStore, engine interfaces.ConsensusEngine, config *ConsensusConfig, events interfaces.EventBus) *QueryManager {
	qm := &QueryManager{
		nodeID:    nodeID,
		transport: transport,
		store:     store,
		engine:    engine,
		config:    config,
		events:    events,
	}

	events.Subscribe(types.EventQueryComplete, func(e interfaces.Event) {
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
		return
	}

	requestID := atomic.AddUint32(&qm.nextReqID, 1)
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
		qm.node.stats.Mu.Lock()
		qm.node.stats.QueriesSent++
		qm.node.stats.QueriesPerHeight[block.Height]++
		qm.node.stats.Mu.Unlock()
	}
}

func (qm *QueryManager) HandleChit(msg types.Message) {
	if poll, ok := qm.activePolls.Load(msg.RequestID); ok {
		p := poll.(*Poll)
		qm.engine.SubmitChit(types.NodeID(msg.From), p.queryKey, msg.PreferredID)
	}
}

func (qm *QueryManager) Start(ctx context.Context) {
	go func() {
		time.Sleep(100 * time.Millisecond)
		for i := 0; i < qm.config.MaxConcurrentQueries; i++ {
			qm.tryIssueQuery()
		}
	}()

	go func() {
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
