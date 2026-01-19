package consensus

import (
	"context"
	"dex/interfaces"
	"dex/logs"
	"dex/types"
	"time"
)

// ============================================
// 节点实现
// ============================================

type Node struct {
	ID              types.NodeID
	IsByzantine     bool
	transport       interfaces.Transport
	store           interfaces.BlockStore
	engine          interfaces.ConsensusEngine
	events          interfaces.EventBus
	messageHandler  *MessageHandler
	queryManager    *QueryManager
	gossipManager   *GossipManager
	SyncManager     *SyncManager
	snapshotManager *SnapshotManager
	proposalManager *ProposalManager
	ctx             context.Context
	cancel          context.CancelFunc
	Logger          logs.Logger
	config          *Config
	Stats           *NodeStats
}

func NewNode(id types.NodeID, transport interfaces.Transport, store interfaces.BlockStore, byzantine bool, config *Config, logger logs.Logger) *Node {
	ctx, cancel := context.WithCancel(context.Background())

	events := NewEventBus()
	engine := NewSnowmanEngine(id, store, &config.Consensus, events, logger)

	node := &Node{
		ID:          id,
		IsByzantine: byzantine,
		transport:   transport,
		store:       store, // 使用传入的 store
		engine:      engine,
		events:      events,
		ctx:         ctx,
		cancel:      cancel,
		Logger:      logger,
		config:      config,
		Stats:       NewNodeStats(events),
	}

	messageHandler := NewMessageHandler(id, byzantine, transport, store, engine, events, &config.Consensus, logger)
	messageHandler.node = node

	queryManager := NewQueryManager(id, transport, store, engine, &config.Consensus, events, logger)
	queryManager.node = node

	gossipManager := NewGossipManager(id, transport, store, &config.Gossip, events, logger)
	gossipManager.node = node

	syncManager := NewSyncManager(id, transport, store, &config.Sync, &config.Snapshot, events, logger)
	syncManager.node = node

	snapshotManager := NewSnapshotManager(id, store, &config.Snapshot, events, logger) // 新增

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
		for {
			select {
			case msg := <-n.transport.Receive():
				n.messageHandler.HandleMsg(msg)
			case <-n.ctx.Done():
				return
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
