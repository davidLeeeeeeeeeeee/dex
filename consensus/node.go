package consensus

import (
	"context"
	"dex/interfaces"
	"dex/logs"
	"dex/stats"
	"dex/types"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ============================================
// 节点实现
// ============================================

type Node struct {
	ID                types.NodeID
	IsByzantine       bool
	transport         interfaces.Transport
	store             interfaces.BlockStore
	engine            interfaces.ConsensusEngine
	events            interfaces.EventBus
	messageHandler    *MessageHandler
	queryManager      *QueryManager
	gossipManager     *GossipManager
	SyncManager       *SyncManager
	snapshotManager   *SnapshotManager
	proposalManager   *ProposalManager
	ctx               context.Context
	cancel            context.CancelFunc
	Logger            logs.Logger
	config            *Config
	Stats             *NodeStats
	handleMsgSemInUse atomic.Int64
	handleMsgSemPeak  atomic.Int64
	handleMsgSemCap   atomic.Int64
	handleMsgLatency  *stats.LatencyRecorder
	handleMsgSlow100  atomic.Uint64
	handleMsgSlow1s   atomic.Uint64
	handleMsgByType   sync.Map // map[string]*handleMsgTypeAccumulator
	lastPressureLogNs atomic.Int64
}

type HandleMsgRuntimeStats struct {
	SemInUse      int64
	SemCapacity   int64
	SemPeak       int64
	SlowWait100ms uint64
	SlowWait1s    uint64
}

type HandleMsgTypeRuntimeStats struct {
	Type          string
	InFlight      int64
	Started       uint64
	Completed     uint64
	SlowWait100ms uint64
	SlowWait1s    uint64
}

type handleMsgTypeAccumulator struct {
	inFlight      atomic.Int64
	started       atomic.Uint64
	completed     atomic.Uint64
	slowWait100ms atomic.Uint64
	slowWait1s    atomic.Uint64
}

func NewNode(id types.NodeID, transport interfaces.Transport, store interfaces.BlockStore, byzantine bool, config *Config, logger logs.Logger) *Node {
	return NewNodeWithSigner(id, transport, store, byzantine, config, logger, nil)
}

func NewNodeWithSigner(id types.NodeID, transport interfaces.Transport, store interfaces.BlockStore, byzantine bool, config *Config, logger logs.Logger, signer interfaces.NodeSigner) *Node {
	ctx, cancel := context.WithCancel(context.Background())

	events := NewEventBus()
	engine := NewSnowmanEngine(id, store, &config.Consensus, events, logger)

	node := &Node{
		ID:               id,
		IsByzantine:      byzantine,
		transport:        transport,
		store:            store,
		engine:           engine,
		events:           events,
		ctx:              ctx,
		cancel:           cancel,
		Logger:           logger,
		config:           config,
		Stats:            NewNodeStats(events),
		handleMsgLatency: stats.NewLatencyRecorder(4096),
	}

	messageHandler := NewMessageHandler(id, byzantine, transport, store, engine, events, &config.Consensus, logger)
	messageHandler.node = node
	messageHandler.signer = signer

	queryManager := NewQueryManager(id, transport, store, engine, &config.Consensus, events, logger)
	queryManager.node = node

	gossipManager := NewGossipManager(id, transport, store, &config.Gossip, events, logger)
	gossipManager.node = node
	gossipManager.SetQueryManager(queryManager)

	syncManager := NewSyncManager(id, transport, store, &config.Sync, &config.Snapshot, events, logger)
	syncManager.node = node

	// 注入 SyncManager 到 QueryManager，用于同步期间暂停共识
	queryManager.SetSyncManager(syncManager)

	snapshotManager := NewSnapshotManager(id, store, &config.Snapshot, events, logger)

	proposalManager := NewProposalManager(id, transport, store, &config.Node, events, logger)
	proposalManager.node = node

	messageHandler.SetManagers(queryManager, gossipManager, syncManager, snapshotManager)
	messageHandler.SetProposalManager(proposalManager)

	node.messageHandler = messageHandler
	node.queryManager = queryManager
	node.gossipManager = gossipManager
	node.SyncManager = syncManager
	node.snapshotManager = snapshotManager
	node.proposalManager = proposalManager

	return node
}

func (n *Node) Start() {
	go func() {
		logs.SetThreadLogger(n.Logger)

		controlCh := n.transport.Receive()
		dataCh := (<-chan types.Message)(nil)
		if tr, ok := n.transport.(interface{ ReceiveData() <-chan types.Message }); ok {
			dataCh = tr.ReceiveData()
		}
		immCh := (<-chan types.Message)(nil)
		if tr, ok := n.transport.(interface{ ReceiveImmediate() <-chan types.Message }); ok {
			immCh = tr.ReceiveImmediate()
		}

		// 使用信号量限制并发处理，防止阻塞接收队列
		// 允许最高 2000 个并发处理 (考虑到 I/O 等待)
		const maxConcurrency = 2000
		sem := make(chan struct{}, maxConcurrency)
		n.handleMsgSemCap.Store(int64(maxConcurrency))

		for {
			// 1. 最高优先级：紧急消息（区块补全）
			if immCh != nil {
				select {
				case msg := <-immCh:
					n.dispatchMessageWithSem(sem, msg)
					continue
				default:
				}
			}

			// 2. 次高优先级：数据消息（区块数据推送）
			if dataCh != nil {
				select {
				case msg := <-dataCh:
					n.dispatchMessageWithSem(sem, msg)
					continue
				default:
				}
			}

			// 3. 普通消息（共识查询等）
			select {
			case <-n.ctx.Done():
				return
			case msg := <-immCh:
				n.dispatchMessageWithSem(sem, msg)
			case msg := <-dataCh:
				n.dispatchMessageWithSem(sem, msg)
			case msg := <-controlCh:
				n.dispatchMessageWithSem(sem, msg)
			}
		}
	}()

	// 启动统计数据清理 goroutine
	go n.statsCleanupLoop()

	n.engine.Start(n.ctx)
	n.queryManager.Start(n.ctx)
	n.gossipManager.Start(n.ctx)
	n.SyncManager.Start(n.ctx)
	n.snapshotManager.Start(n.ctx)

	if !n.IsByzantine {
		n.proposalManager.Start(n.ctx)
	}
}

// statsCleanupLoop 定期清理统计数据中的旧高度数据
func (n *Node) statsCleanupLoop() {
	logs.SetThreadLogger(n.Logger)
	ticker := time.NewTicker(60 * time.Second) // 每分钟清理一次
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			_, currentHeight := n.store.GetLastAccepted()
			if currentHeight > 100 {
				cleanupHeight := currentHeight - 100
				if n.Stats != nil {
					n.Stats.CleanupOldHeights(cleanupHeight)
				}
			}
		case <-n.ctx.Done():
			return
		}
	}
}

func (n *Node) Stop() {
	n.cancel()
}

func (n *Node) GetLastAccepted() (string, uint64) {
	return n.store.GetLastAccepted()
}

func (n *Node) GetBlock(id string) (*types.Block, bool) {
	return n.store.Get(id)
}

// GetMessageStats 获取消息处理统计
func (n *Node) GetMessageStats() map[string]uint64 {
	if n.messageHandler != nil && n.messageHandler.stats != nil {
		return n.messageHandler.stats.GetAPICallStats()
	}
	return nil
}

func (n *Node) GetBlockStore() interfaces.BlockStore {
	return n.store
}

// ResetProposalTimer 重置出块计时器
func (n *Node) ResetProposalTimer() {
	if n.proposalManager != nil {
		n.proposalManager.ResetProposalTimer()
	}
}

func (n *Node) dispatchMessageWithSem(sem chan struct{}, msg types.Message) {
	waitStart := time.Now()
	sem <- struct{}{}
	waitDuration := time.Since(waitStart)

	msgType := n.normalizeMessageType(msg.Type)
	acc := n.getHandleMsgTypeAccumulator(msgType)

	if n.handleMsgLatency != nil {
		n.handleMsgLatency.Record("node.handle_msg.sem_wait", waitDuration)
		n.handleMsgLatency.Record("node.handle_msg.sem_wait."+msgType, waitDuration)
	}
	if waitDuration >= 100*time.Millisecond {
		n.handleMsgSlow100.Add(1)
		acc.slowWait100ms.Add(1)
	}
	if waitDuration >= time.Second {
		n.handleMsgSlow1s.Add(1)
		acc.slowWait1s.Add(1)
	}
	acc.started.Add(1)
	acc.inFlight.Add(1)
	n.incHandleMsgSemInUse()
	n.maybeLogHandleMsgPressure(waitDuration, sem, msg, msgType)

	go func(m types.Message, typeKey string, typeAcc *handleMsgTypeAccumulator) {
		start := time.Now()
		defer func() {
			if n.handleMsgLatency != nil {
				totalDuration := time.Since(start)
				n.handleMsgLatency.Record("node.handle_msg.total", totalDuration)
				n.handleMsgLatency.Record("node.handle_msg.total."+typeKey, totalDuration)
			}
			typeAcc.completed.Add(1)
			typeAcc.inFlight.Add(-1)
			n.decHandleMsgSemInUse()
			<-sem
		}()
		n.messageHandler.HandleMsg(m)
	}(msg, msgType, acc)
}

func (n *Node) incHandleMsgSemInUse() {
	current := n.handleMsgSemInUse.Add(1)
	for {
		peak := n.handleMsgSemPeak.Load()
		if current <= peak {
			return
		}
		if n.handleMsgSemPeak.CompareAndSwap(peak, current) {
			return
		}
	}
}

func (n *Node) decHandleMsgSemInUse() {
	n.handleMsgSemInUse.Add(-1)
}

func (n *Node) GetRuntimeStats() HandleMsgRuntimeStats {
	return HandleMsgRuntimeStats{
		SemInUse:      n.handleMsgSemInUse.Load(),
		SemCapacity:   n.handleMsgSemCap.Load(),
		SemPeak:       n.handleMsgSemPeak.Load(),
		SlowWait100ms: n.handleMsgSlow100.Load(),
		SlowWait1s:    n.handleMsgSlow1s.Load(),
	}
}

func (n *Node) GetHandleMsgTypeStats(reset bool, topN int) []HandleMsgTypeRuntimeStats {
	if n == nil {
		return nil
	}

	result := make([]HandleMsgTypeRuntimeStats, 0, 8)
	n.handleMsgByType.Range(func(k, v any) bool {
		typeKey, ok := k.(string)
		if !ok {
			return true
		}
		acc, ok := v.(*handleMsgTypeAccumulator)
		if !ok || acc == nil {
			return true
		}

		stat := HandleMsgTypeRuntimeStats{
			Type:     typeKey,
			InFlight: acc.inFlight.Load(),
		}
		if reset {
			stat.Started = acc.started.Swap(0)
			stat.Completed = acc.completed.Swap(0)
			stat.SlowWait100ms = acc.slowWait100ms.Swap(0)
			stat.SlowWait1s = acc.slowWait1s.Swap(0)
		} else {
			stat.Started = acc.started.Load()
			stat.Completed = acc.completed.Load()
			stat.SlowWait100ms = acc.slowWait100ms.Load()
			stat.SlowWait1s = acc.slowWait1s.Load()
		}

		// Keep types with live pressure or recent traffic.
		if stat.InFlight > 0 || stat.Started > 0 || stat.SlowWait100ms > 0 || stat.SlowWait1s > 0 {
			result = append(result, stat)
		}
		return true
	})

	if len(result) == 0 {
		return nil
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].InFlight != result[j].InFlight {
			return result[i].InFlight > result[j].InFlight
		}
		if result[i].SlowWait1s != result[j].SlowWait1s {
			return result[i].SlowWait1s > result[j].SlowWait1s
		}
		if result[i].SlowWait100ms != result[j].SlowWait100ms {
			return result[i].SlowWait100ms > result[j].SlowWait100ms
		}
		if result[i].Started != result[j].Started {
			return result[i].Started > result[j].Started
		}
		return result[i].Type < result[j].Type
	})

	if topN > 0 && len(result) > topN {
		result = result[:topN]
	}
	return result
}

func (n *Node) GetHandleMsgLatencyStats(reset bool) map[string]stats.LatencySummary {
	if n == nil || n.handleMsgLatency == nil {
		return nil
	}
	return n.handleMsgLatency.Snapshot(reset)
}

func (n *Node) normalizeMessageType(msgType types.MessageType) string {
	if msgType == "" {
		return "unknown"
	}
	return string(msgType)
}

func (n *Node) getHandleMsgTypeAccumulator(typeKey string) *handleMsgTypeAccumulator {
	if existing, ok := n.handleMsgByType.Load(typeKey); ok {
		if acc, ok := existing.(*handleMsgTypeAccumulator); ok && acc != nil {
			return acc
		}
	}

	acc := &handleMsgTypeAccumulator{}
	actual, _ := n.handleMsgByType.LoadOrStore(typeKey, acc)
	if typed, ok := actual.(*handleMsgTypeAccumulator); ok && typed != nil {
		return typed
	}
	return acc
}

func (n *Node) maybeLogHandleMsgPressure(wait time.Duration, sem chan struct{}, msg types.Message, msgType string) {
	semCap := cap(sem)
	if semCap == 0 {
		return
	}
	semLen := len(sem)
	usagePct := semLen * 100 / semCap
	if wait < 500*time.Millisecond && usagePct < 90 {
		return
	}

	now := time.Now().UnixNano()
	last := n.lastPressureLogNs.Load()
	if last != 0 && now-last < int64(5*time.Second) {
		return
	}
	if !n.lastPressureLogNs.CompareAndSwap(last, now) {
		return
	}

	logs.Warn(
		"[HandleMsg] pressure local=%s wait=%v sem=%d/%d usage=%d%% type=%s from=%s request=%d block=%s",
		n.ID, wait, semLen, semCap, usagePct, msgType, msg.From, msg.RequestID, msg.BlockID,
	)
}
