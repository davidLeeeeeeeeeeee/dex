package consensus

import (
	"context"
	"dex/interfaces"
	"dex/types"
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
	syncManager     *SyncManager
	snapshotManager *SnapshotManager
	proposalManager *ProposalManager
	ctx             context.Context
	cancel          context.CancelFunc
	config          *Config
	stats           *NodeStats
}

func NewNode(id types.NodeID, transport interfaces.Transport, store interfaces.BlockStore, byzantine bool, config *Config) *Node {
	ctx, cancel := context.WithCancel(context.Background())

	events := NewEventBus()
	engine := NewSnowmanEngine(id, store, &config.Consensus, events)

	node := &Node{
		ID:          id,
		IsByzantine: byzantine,
		transport:   transport,
		store:       store, // 使用传入的 store
		engine:      engine,
		events:      events,
		ctx:         ctx,
		cancel:      cancel,
		config:      config,
		stats:       NewNodeStats(),
	}

	messageHandler := NewMessageHandler(id, byzantine, transport, store, engine, events, &config.Consensus)
	messageHandler.node = node

	queryManager := NewQueryManager(id, transport, store, engine, &config.Consensus, events)
	queryManager.node = node

	gossipManager := NewGossipManager(id, transport, store, &config.Gossip, events)
	gossipManager.node = node

	syncManager := NewSyncManager(id, transport, store, &config.Sync, &config.Snapshot, events)
	syncManager.node = node

	snapshotManager := NewSnapshotManager(id, store, &config.Snapshot, events) // 新增

	proposalManager := NewProposalManager(id, transport, store, &config.Node, events)
	proposalManager.node = node

	messageHandler.SetManagers(queryManager, gossipManager, syncManager, snapshotManager)

	node.messageHandler = messageHandler
	node.queryManager = queryManager
	node.gossipManager = gossipManager
	node.syncManager = syncManager
	node.snapshotManager = snapshotManager
	node.proposalManager = proposalManager

	return node
}

func (n *Node) Start() {
	go func() {
		for {
			select {
			case msg := <-n.transport.Receive():
				n.messageHandler.HandleMsg(msg)
			case <-n.ctx.Done():
				return
			}
		}
	}()

	n.engine.Start(n.ctx)
	n.queryManager.Start(n.ctx)
	n.gossipManager.Start(n.ctx)
	n.syncManager.Start(n.ctx)
	n.snapshotManager.Start(n.ctx)

	if !n.IsByzantine {
		n.proposalManager.Start(n.ctx)
	}
}

func (n *Node) Stop() {
	n.cancel()
}
