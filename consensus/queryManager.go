package consensus

import (
	"context"
	cryptorand "crypto/rand"
	"dex/interfaces"
	"dex/logs"
	"dex/types"
	"encoding/binary"
	"math/rand"
	"sync"
	"time"
)

// ============================================
// 查询管理器
// ============================================

type QueryManager struct {
	nodeID               types.NodeID
	node                 *Node
	transport            interfaces.Transport
	store                interfaces.BlockStore
	engine               interfaces.ConsensusEngine
	config               *ConsensusConfig
	events               interfaces.EventBus
	Logger               logs.Logger
	activePolls          sync.Map
	nextReqID            uint32
	mu                   sync.Mutex
	missingBlockRequests sync.Map
	lastIssueTime        time.Time     // 上次发起查询的时间
	queryCooldown        time.Duration // 发起查询的最小冷却时间
	syncManager          *SyncManager  // 同步管理器引用，用于检查同步状态
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
		nodeID:        id,
		transport:     transport,
		store:         store,
		engine:        engine,
		config:        config,
		events:        events,
		Logger:        logger,
		queryCooldown: 100 * time.Millisecond, // 默认 100ms 最小间隔
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
		if (data.Reason == "timeout" || data.Reason == "parent_missing") && len(data.QueryKeys) > 0 {
			// 添加退避延迟，避免超时或因缺数据失败后立即重发导致自激荡
			// 使用 200-500ms 随机退避 + jitter
			backoff := 200*time.Millisecond + time.Duration(rand.Int63n(300))*time.Millisecond
			logs.Debug("[QueryManager] Query %s, will retry after %v (count=%d)", data.Reason, backoff, len(data.QueryKeys))
			time.AfterFunc(backoff, func() {
				qm.tryIssueQuery()
			})
		}
	})

	events.Subscribe(types.EventSyncComplete, func(e interfaces.Event) {
		// 同步完成是权威信号，可以稍微跳过冷却
		qm.mu.Lock()
		qm.lastIssueTime = time.Time{}
		qm.mu.Unlock()
		qm.tryIssueQuery()
	})

	// 新增：快照加载后也触发查询
	events.Subscribe(types.EventSnapshotLoaded, func(e interfaces.Event) {
		qm.tryIssueQuery()
	})

	events.Subscribe(types.EventBlockReceived, func(e interfaces.Event) {
		// 只有在空闲时才由区块接收触发
		if qm.engine.GetActiveQueryCount() == 0 {
			qm.tryIssueQuery()
		}
	})

	return qm
}

// SetSyncManager 设置同步管理器引用（在初始化后调用）
func (qm *QueryManager) SetSyncManager(sm *SyncManager) {
	qm.syncManager = sm
}

// 尝试发起查询
func (qm *QueryManager) tryIssueQuery() {
	// 如果正在同步，暂停共识查询
	if qm.syncManager != nil {
		qm.syncManager.Mu.RLock()
		syncing := qm.syncManager.Syncing || qm.syncManager.sampling
		qm.syncManager.Mu.RUnlock()
		if syncing {
			return
		}
	}

	qm.mu.Lock()
	defer qm.mu.Unlock()

	_, currentHeight := qm.store.GetLastAccepted()
	nextHeight := currentHeight + 1

	blocks := qm.store.GetByHeight(nextHeight)
	if len(blocks) == 0 {
		return
	}

	// 1. 冷却时间检查（防抖）
	if time.Since(qm.lastIssueTime) < qm.queryCooldown {
		return
	}

	if qm.engine.GetActiveQueryCount() >= qm.config.MaxConcurrentQueries {
		return
	}

	qm.lastIssueTime = time.Now()
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
		// 使用 selectByMinHash 与 Snowball 保持一致的确定性选择规则
		blockID = selectByMinHash(candidates)
	}

	block, exists := qm.store.Get(blockID)
	if !exists {
		logs.Warn("[QueryManager] Block %s not found for query at height %d (pending?), retrying after backoff", blockID, nextHeight)
		// 数据还没补齐，延迟 500ms 后再试，避免形成高频查询风暴
		time.AfterFunc(500*time.Millisecond, func() {
			qm.tryIssueQuery()
		})
		return
	}
	requestID, _ := secureRandUint32()
	queryKey := qm.engine.RegisterQuery(qm.nodeID, requestID, blockID, block.Header.Height)

	poll := &Poll{
		requestID: requestID,
		blockID:   blockID,
		queryKey:  queryKey,
		startTime: time.Now(),
		height:    block.Header.Height,
	}
	qm.activePolls.Store(requestID, poll)

	peers := qm.transport.SamplePeers(qm.nodeID, qm.config.K)

	// 判断自己是否是该区块的提议者
	isProposer := (block.Header.Proposer == string(qm.nodeID))

	var msg types.Message
	if isProposer {
		// 只有提议者发送PushQuery（携带完整区块）
		msg = types.Message{
			Type:      types.MsgPushQuery,
			From:      qm.nodeID,
			RequestID: requestID,
			BlockID:   blockID,
			Block:     block, // 携带完整区块数据
			Height:    block.Header.Height,
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
			Height:    block.Header.Height,
		}
		logs.Debug("[Node %s] Sending PullQuery for block %s",
			qm.nodeID, blockID)
	}

	qm.transport.Broadcast(msg, peers)

	if qm.node != nil {
		qm.node.Stats.Mu.Lock()
		qm.node.Stats.QueriesSent++
		qm.node.Stats.QueriesPerHeight[block.Header.Height]++
		qm.node.Stats.Mu.Unlock()
	}
}

// 返回 [0, 2^32-1] 范围内的安全随机 uint32
func secureRandUint32() (uint32, error) {
	var b [4]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b[:]), nil
}

// RequestBlock 尝试请求缺失的区块，带有自激荡保护（限流）
func (qm *QueryManager) RequestBlock(blockID string, from types.NodeID) {
	// 检查是否最近已请求过该块，避免自激
	lastReqTime, loaded := qm.missingBlockRequests.LoadOrStore(blockID, time.Now())
	shouldRequest := true
	if loaded {
		if time.Since(lastReqTime.(time.Time)) < 3*time.Second {
			shouldRequest = false
		} else {
			// 更新时间
			qm.missingBlockRequests.Store(blockID, time.Now())
		}
	}

	if shouldRequest {
		logs.Warn("[QueryManager] Requesting missing block %s from %s", blockID, from)
		if err := qm.transport.Send(from, types.Message{
			Type:      types.MsgGet,
			From:      qm.nodeID,
			RequestID: 0, // 主动请求不需要 RequestID
			BlockID:   blockID,
		}); err != nil {
			logs.Warn("[QueryManager] Failed to request missing block %s from %s: %v",
				blockID, from, err)
		}
	}
}

func (qm *QueryManager) HandleChit(msg types.Message) {
	if poll, ok := qm.activePolls.Load(msg.RequestID); ok {
		p := poll.(*Poll)
		if msg.PreferredID != "" {
			if _, exists := qm.store.Get(msg.PreferredID); !exists {
				// 使用重构后的 RequestBlock 方法
				qm.RequestBlock(msg.PreferredID, types.NodeID(msg.From))
			}
		}
		qm.engine.SubmitChit(types.NodeID(msg.From), p.queryKey, msg.PreferredID)
	}

	// 事件驱动同步：探测到任何领先高度都尝试触发（不再等待 BehindThreshold 阈值）
	if qm.syncManager != nil && msg.AcceptedHeight > 0 {
		_, localAccepted := qm.store.GetLastAccepted()
		if msg.AcceptedHeight > localAccepted {
			qm.syncManager.TriggerSyncFromChit(msg.AcceptedHeight, types.NodeID(msg.From))
		}
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
