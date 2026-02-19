package consensus

import (
	"context"
	"dex/config"
	"dex/db"
	"dex/interfaces"
	"dex/logs"
	"dex/pb"
	"dex/sender"
	"dex/txpool"
	"dex/types"
	"sync"
	"time"
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
	globalCfg *config.Config,
) *ConsensusNodeManager {
	return InitConsensusManagerWithSimulation(nodeID, dbManager, config, senderMgr, txPool, logger, 0.0, 0, 0, globalCfg)
}

// InitConsensusManagerWithPacketLoss 初始化共识管理器，支持丢包率模拟（兼容旧接口）
// packetLossRate: 丢包率，范围 0.0 到 1.0，例如 0.1 表示 10% 丢包率
func InitConsensusManagerWithPacketLoss(
	nodeID types.NodeID,
	dbManager *db.Manager,
	config *Config,
	senderMgr *sender.SenderManager,
	txPool *txpool.TxPool,
	logger logs.Logger,
	packetLossRate float64,
	globalCfg *config.Config,
) *ConsensusNodeManager {
	return InitConsensusManagerWithSimulation(nodeID, dbManager, config, senderMgr, txPool, logger, packetLossRate, 0, 0, globalCfg)
}

// InitConsensusManagerWithSimulation 初始化共识管理器，支持网络模拟（丢包率+延迟）
// packetLossRate: 丢包率，范围 0.0 到 1.0，例如 0.1 表示 10% 丢包率
// minLatency, maxLatency: 随机延迟范围，例如 100ms 到 200ms
func InitConsensusManagerWithSimulation(
	nodeID types.NodeID,
	dbManager *db.Manager,
	config *Config,
	senderMgr *sender.SenderManager,
	txPool *txpool.TxPool,
	logger logs.Logger,
	packetLossRate float64,
	minLatency, maxLatency time.Duration,
	globalCfg *config.Config,
) *ConsensusNodeManager {
	// 创建带网络模拟的 transport
	transport := NewRealTransportWithSimulation(nodeID, dbManager, senderMgr, context.Background(), packetLossRate, minLatency, maxLatency)

	// 替换默认的 MemoryBlockStore 为 RealBlockStore
	realStore := NewRealBlockStore(nodeID, dbManager, txPool, globalCfg)
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

	// 创建 ConsensusAdapter（需要提前创建用于 PendingBlockBuffer）
	adapter := NewConsensusAdapterWithTxPool(dbManager, txPool)

	// 创建 PendingBlockBuffer 并注入到 MessageHandler 和 SyncManager
	pendingBlockBuffer := NewPendingBlockBuffer(txPool, adapter, senderMgr, logger)
	pendingBlockBuffer.Start()
	node.messageHandler.SetPendingBlockBuffer(pendingBlockBuffer)
	node.SyncManager.SetPendingBlockBuffer(pendingBlockBuffer)

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
		adapter:        adapter,
		Logger:         logger,
	}

	if packetLossRate > 0 || maxLatency > 0 {
		logger.Info("[ConsensusManager] Initialized with NodeID %s, PacketLossRate=%.2f%%, Latency=%v~%v",
			nodeID, packetLossRate*100, minLatency, maxLatency)
	} else {
		logger.Info("[ConsensusManager] Initialized with NodeID %s", nodeID)
	}

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

	// Keep payload in global cache for RealBlockStore.Add full-data checks.
	CacheBlock(block)

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
			block.BlockHash, block.Header.Height)
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

// GetCurrentHeight 获取当前高度
func (m *ConsensusNodeManager) GetCurrentHeight() uint64 {
	return m.store.GetCurrentHeight()
}

// GetPendingBlocksCount 获取候选区块数量
func (m *ConsensusNodeManager) GetPendingBlocksCount() int {
	return m.store.GetPendingBlocksCount()
}

// GetPendingBlocks 获取候选区块列表
func (m *ConsensusNodeManager) GetPendingBlocks() []*types.Block {
	return m.store.GetPendingBlocks()
}

// GetEventBus 获取事件总线（用于适配器）
func (m *ConsensusNodeManager) GetEventBus() interfaces.EventBus {
	if m.Node != nil {
		return m.Node.events
	}
	return nil
}

// GetBlockStore 获取区块存储
func (m *ConsensusNodeManager) GetBlockStore() interfaces.BlockStore {
	return m.store
}

// GetFinalizationChits 获取指定高度的最终化投票信息
func (m *ConsensusNodeManager) GetFinalizationChits(height uint64) *types.FinalizationChits {
	if realStore, ok := m.store.(*RealBlockStore); ok {
		chits, exists := realStore.GetFinalizationChits(height)
		if exists {
			return chits
		}
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

// ResetProposalTimer 重置出块计时器
func (m *ConsensusNodeManager) ResetProposalTimer() {
	if m.Node != nil {
		m.Node.ResetProposalTimer()
	}
}

// 停止共识管理器
func (m *ConsensusNodeManager) Stop() {
	if m.Node != nil {
		m.Node.cancel() // 触发优雅关闭
		m.Node.Stop()
	}
	logs.Info("[ConsensusManager] Stopped")
}

// GetPendingHeightsState 获取未最终化高度的共识状态
func (m *ConsensusNodeManager) GetPendingHeightsState() []*HeightState {
	if se, ok := m.engine.(*SnowmanEngine); ok {
		return se.GetPendingHeightsState()
	}
	return nil
}

// GetPendingBlockBuffer 获取待补课区块缓冲区
func (m *ConsensusNodeManager) GetPendingBlockBuffer() *PendingBlockBuffer {
	if m.messageHandler != nil {
		return m.messageHandler.pendingBlockBuffer
	}
	return nil
}

// SyncStatus 同步状态
type SyncStatus struct {
	IsSyncing        bool   // 是否正在同步追赶
	SyncTargetHeight uint64 // 同步目标高度（网络最高已最终化高度）
}

// GetSyncStatus 获取节点同步状态
func (m *ConsensusNodeManager) GetSyncStatus() SyncStatus {
	if m.Node == nil || m.Node.SyncManager == nil {
		return SyncStatus{}
	}

	sm := m.Node.SyncManager
	sm.Mu.RLock()
	defer sm.Mu.RUnlock()

	// 计算网络最高已最终化高度
	var maxPeerHeight uint64
	for _, h := range sm.PeerHeights {
		if h > maxPeerHeight {
			maxPeerHeight = h
		}
	}

	return SyncStatus{
		IsSyncing:        sm.Syncing,
		SyncTargetHeight: maxPeerHeight,
	}
}
