package consensus

import (
	"context"
	"dex/db"
	"dex/interfaces"
	"dex/logs"
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
}

func InitConsensusManager(
	nodeID types.NodeID,
	dbManager *db.Manager,
	config *Config,
	senderMgr *sender.SenderManager,
	txPool *txpool.TxPool,
) *ConsensusNodeManager {
	// 创建真实的 transport
	transport := NewRealTransport(nodeID, dbManager, senderMgr, context.Background())

	// 替换默认的 MemoryBlockStore 为 RealBlockStore
	realStore := NewRealBlockStore(dbManager, config.Snapshot.MaxSnapshots, txPool)
	node := NewNode(nodeID, transport, realStore, false, config)

	// 重新创建 engine，使用 realStore
	node.engine = NewSnowmanEngine(nodeID, realStore, &config.Consensus, node.events)

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
	}

	logs.Info("[ConsensusManager] Initialized with NodeID %s", nodeID)

	return consensusManager
}

func (m *ConsensusNodeManager) Start() {
	if m.Node != nil {
		m.Node.Start()
		logs.Info("[ConsensusManager] Started consensus engine")
	}
}
func (m *ConsensusNodeManager) GetActiveQueryCount() int {
	if m.engine != nil {
		return m.engine.GetActiveQueryCount()
	}
	return 0
}

// 添加新区块到共识
func (m *ConsensusNodeManager) AddBlock(block *db.Block) error {
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

// HasBlock 检查是否有指定区块
func (m *ConsensusNodeManager) HasBlock(blockId string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.store.Get(blockId)
	return exists
}

// ProcessMessage 处理接收到的共识消息
func (m *ConsensusNodeManager) ProcessMessage(msg types.Message) {
	m.messageHandler.HandleMsg(msg)
}

// StartQuery 发起查询
func (m *ConsensusNodeManager) StartQuery() {
	m.queryManager.tryIssueQuery()
}

// 获取节点统计信息
func (m *ConsensusNodeManager) GetStats() *NodeStats {
	return m.Node.stats
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
