package consensus

import (
	"context"
	"dex/db"
	"dex/interfaces"
	"dex/logs"
	"dex/types"
	"fmt"
	"sync"
)

// ConsensusNodeManager 管理共识节点的全局状态
type ConsensusNodeManager struct {
	mu             sync.RWMutex
	node           *Node
	engine         interfaces.ConsensusEngine
	store          interfaces.BlockStore
	transport      interfaces.Transport
	messageHandler *MessageHandler
	queryManager   *QueryManager
	dbManager      *db.Manager
}

func InitConsensusManager(nodeID types.NodeID, dbManager *db.Manager, config *Config) *ConsensusNodeManager {
	// 直接创建新实例，不使用sync.Once

	// 创建真实的组件
	store := NewRealBlockStore(dbManager, config.Snapshot.MaxSnapshots)
	events := NewEventBus()
	engine := NewSnowmanEngine(nodeID, store, &config.Consensus, events)
	transport := NewRealTransport(nodeID, dbManager, context.Background())
	// 创建 context
	ctx, cancel := context.WithCancel(context.Background())

	// 创建节点
	node := &Node{
		ID:          nodeID,
		IsByzantine: false,
		transport:   transport,
		store:       store,
		engine:      engine,
		events:      events,
		config:      config,
		stats:       NewNodeStats(),
		ctx:         ctx,    // 添加
		cancel:      cancel, // 添加
	}

	// 创建各种管理器
	messageHandler := NewMessageHandler(nodeID, false, transport, store, engine, events, &config.Consensus)
	queryManager := NewQueryManager(nodeID, transport, store, engine, &config.Consensus, events)
	gossipManager := NewGossipManager(nodeID, transport, store, &config.Gossip, events)
	syncManager := NewSyncManager(nodeID, transport, store, &config.Sync, &config.Snapshot, events)
	snapshotManager := NewSnapshotManager(nodeID, store, &config.Snapshot, events)
	proposer := NewRealBlockProposer(dbManager)
	proposalManager := NewProposalManagerWithProposer(nodeID, transport, store, &config.Node, events, proposer)

	// 设置关联
	messageHandler.SetManagers(queryManager, gossipManager, syncManager, snapshotManager)
	messageHandler.node = node
	queryManager.node = node
	gossipManager.node = node
	syncManager.node = node
	proposalManager.node = node

	node.messageHandler = messageHandler
	node.queryManager = queryManager
	node.gossipManager = gossipManager
	node.syncManager = syncManager
	node.snapshotManager = snapshotManager
	node.proposalManager = proposalManager

	// 创建ConsensusNodeManager
	consensusManager := &ConsensusNodeManager{
		node:           node,
		engine:         engine,
		store:          store,
		transport:      transport,
		messageHandler: messageHandler,
		queryManager:   queryManager,
		dbManager:      dbManager,
	}

	// 启动节点
	node.Start()

	logs.Info("[ConsensusManager] Initialized with NodeID %s", nodeID)

	return consensusManager
}

// AddBlock 添加新区块到共识
func (m *ConsensusNodeManager) AddBlock(block *db.Block) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 转换为types.Block
	typesBlock := &types.Block{
		ID:       block.BlockHash,
		Height:   block.Height,
		ParentID: block.PrevBlockHash,
		Data:     fmt.Sprintf("TxCount: %d", len(block.Body)),
		Proposer: "0", // 需要从Miner字段解析
	}

	// 添加到存储
	added, err := m.store.Add(typesBlock)
	if err != nil {
		return err
	}

	if added {
		// 触发新区块事件
		m.node.events.PublishAsync(types.BaseEvent{
			EventType: types.EventNewBlock,
			EventData: typesBlock,
		})

		logs.Info("[ConsensusManager] Added new block %s at height %d",
			block.BlockHash, block.Height)
	}

	return nil
}

// GetPreference 获取指定高度的偏好区块
func (m *ConsensusNodeManager) GetPreference(height uint64) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.engine.GetPreference(height)
}

// GetLastAccepted 获取最后接受的区块
func (m *ConsensusNodeManager) GetLastAccepted() (string, uint64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store.GetLastAccepted()
}

// HasBlock 检查是否有指定区块
func (m *ConsensusNodeManager) HasBlock(blockId string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.store.Get(blockId)
	return exists
}

// ProcessMessage 处理接收到的共识消息
func (m *ConsensusNodeManager) ProcessMessage(msg types.Message) {
	m.messageHandler.Handle(msg)
}

// StartQuery 发起查询
func (m *ConsensusNodeManager) StartQuery() {
	m.queryManager.tryIssueQuery()
}

// GetStats 获取节点统计信息
func (m *ConsensusNodeManager) GetStats() *NodeStats {
	return m.node.stats
}

// CreateSnapshot 创建快照
func (m *ConsensusNodeManager) CreateSnapshot(height uint64) error {
	_, err := m.store.CreateSnapshot(height)
	if err != nil {
		return err
	}

	logs.Info("[ConsensusManager] Created snapshot at height %d", height)
	return nil
}

// LoadSnapshot 加载快照
func (m *ConsensusNodeManager) LoadSnapshot(snapshot *types.Snapshot) error {
	if err := m.store.LoadSnapshot(snapshot); err != nil {
		return err
	}

	logs.Info("[ConsensusManager] Loaded snapshot at height %d", snapshot.Height)
	return nil
}

// GetCurrentHeight 获取当前高度
func (m *ConsensusNodeManager) GetCurrentHeight() uint64 {
	return m.store.GetCurrentHeight()
}

// IsReady 检查共识是否准备就绪
func (m *ConsensusNodeManager) IsReady() bool {
	// 检查各组件是否就绪
	if m.node == nil || m.engine == nil {
		return false
	}

	// 检查是否有足够的对等节点
	peers := m.transport.SamplePeers(m.node.ID, 1)
	if len(peers) == 0 {
		return false
	}

	return true
}

// Stop 停止共识管理器
func (m *ConsensusNodeManager) Stop() {
	if m.node != nil {
		m.node.cancel() // 触发优雅关闭
		m.node.Stop()
	}
	logs.Info("[ConsensusManager] Stopped")
}
