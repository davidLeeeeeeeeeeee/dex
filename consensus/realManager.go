package consensus

import (
	"context"
	"dex/db"
	"dex/interfaces"
	"dex/logs"
	"dex/pb"
	"dex/sender"
	"dex/txpool"
	"dex/types"
	"sync"
)

// ConsensusNodeManager 管理共识节点的全局状态
type ConsensusNodeManager struct {
	mu             sync.RWMutex
	Node           *Node
	engine         interfaces.ConsensusEngine
	store          interfaces.BlockStore
	Transport      interfaces.Transport
	messageHandler *MessageHandler
	queryManager   *QueryManager
	dbManager      *db.Manager
	senderManager  *sender.SenderManager
	adapter        *ConsensusAdapter
	txPool         *txpool.TxPool
	Logger         logs.Logger
}

func InitConsensusManager(
	nodeID types.NodeID,
	dbManager *db.Manager,
	config *Config,
	senderMgr *sender.SenderManager,
	txPool *txpool.TxPool,
	logger logs.Logger,
) *ConsensusNodeManager {
	// 创建真实的 transport
	transport := NewRealTransport(nodeID, dbManager, senderMgr, context.Background())

	// 替换默认的 MemoryBlockStore 为 RealBlockStore
	realStore := NewRealBlockStore(nodeID, dbManager, config.Snapshot.MaxSnapshots, txPool)
	node := NewNode(nodeID, transport, realStore, false, config, logger)

	// 设置EventBus到RealBlockStore
	if rs, ok := realStore.(*RealBlockStore); ok {
		rs.SetEventBus(node.events)
	}

	// 重新创建 engine，使用 realStore
	node.engine = NewSnowmanEngine(nodeID, realStore, &config.Consensus, node.events, logger)
	node.messageHandler.engine = node.engine
	node.queryManager.engine = node.engine

	// 创建使用真实 BlockProposer 的 ProposalManager
	proposer := NewRealBlockProposer(dbManager, txPool)
	node.proposalManager.SetProposer(proposer)

	// 创建 ConsensusNodeManager
	consensusManager := &ConsensusNodeManager{
		Node:           node,
		engine:         node.engine,
		store:          node.store,
		Transport:      transport,
		messageHandler: node.messageHandler,
		queryManager:   node.queryManager,
		dbManager:      dbManager,
		senderManager:  senderMgr,
		txPool:         txPool,
		adapter:        NewConsensusAdapter(dbManager),
		Logger:         logger,
	}

	logger.Info("[ConsensusManager] Initialized with NodeID %s", nodeID)

	return consensusManager
}

func (m *ConsensusNodeManager) Start() {
	if m.Node != nil {
		m.Node.Start()
		m.Logger.Info("[ConsensusManager] Started consensus engine")
	}
}
func (m *ConsensusNodeManager) GetActiveQueryCount() int {
	if m.engine != nil {
		return m.engine.GetActiveQueryCount()
	}
	return 0
}

// 添加新区块到共识
func (m *ConsensusNodeManager) AddBlock(block *pb.Block) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 转换为types.Block
	typesBlock, err := m.adapter.DBBlockToConsensus(block)
	if err != nil {
		return err
	}

	// 添加到存储
	added, err := m.store.Add(typesBlock)
	if err != nil {
		return err
	}

	if added {
		// 触发新区块事件
		m.Node.events.PublishAsync(types.BaseEvent{
			EventType: types.EventNewBlock,
			EventData: typesBlock,
		})

		logs.Debug("[ConsensusManager] Added new block %s at height %d",
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

func (m *ConsensusNodeManager) GetFinalizedBlockID(height uint64) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.store == nil {
		return "", false
	}
	if block, ok := m.store.GetFinalizedAtHeight(height); ok && block != nil {
		return block.ID, true
	}
	return "", false
}

// HasBlock 检查是否有指定区块
func (m *ConsensusNodeManager) HasBlock(blockId string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.store.Get(blockId)
	return exists
}

// StartQuery 发起查询
func (m *ConsensusNodeManager) StartQuery() {
	m.queryManager.tryIssueQuery()
}

// 获取节点统计信息
func (m *ConsensusNodeManager) GetStats() *NodeStats {
	return m.Node.Stats
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

// GetEventBus 获取事件总线（用于适配器）
func (m *ConsensusNodeManager) GetEventBus() interfaces.EventBus {
	if m.Node != nil {
		return m.Node.events
	}
	return nil
}

// IsReady 检查共识是否准备就绪
func (m *ConsensusNodeManager) IsReady() bool {
	// 检查各组件是否就绪
	if m.Node == nil || m.engine == nil {
		return false
	}

	// 检查是否有足够的对等节点
	peers := m.Transport.SamplePeers(m.Node.ID, 1)
	if len(peers) == 0 {
		return false
	}

	return true
}

// 停止共识管理器
func (m *ConsensusNodeManager) Stop() {
	if m.Node != nil {
		m.Node.cancel() // 触发优雅关闭
		m.Node.Stop()
	}
	logs.Info("[ConsensusManager] Stopped")
}
