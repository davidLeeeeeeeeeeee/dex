package consensus

import (
	"context"
	"dex/config"
	"dex/interfaces"
	"dex/logs"
	"dex/types"
	"sync"
	"time"
)

type ProposalManager struct {
	nodeID          types.NodeID
	node            *Node
	transport       interfaces.Transport
	store           interfaces.BlockStore
	nodeConfig      *NodeConfig         // Renamed from 'config'
	windowConfig    config.WindowConfig // Window配置
	events          interfaces.EventBus
	Logger          logs.Logger
	proposedBlocks  map[string]bool
	proposalWindow  int                    // 当前window（替代proposalRound）
	lastBlockTime   time.Time              // 上次出块时间
	cachedProposals map[int][]*types.Block // 缓存的提案，按window分组
	mu              sync.Mutex
	proposer        interfaces.BlockProposer // 注入的提案者接口
}

// NewProposalManager 创建新的提案管理器（使用默认提案者）
func NewProposalManager(id types.NodeID, transport interfaces.Transport, store interfaces.BlockStore, nodeConfig *NodeConfig, events interfaces.EventBus, logger logs.Logger) *ProposalManager {
	// 获取window配置
	cfg := config.DefaultConfig()
	return NewProposalManagerWithProposer(id, transport, store, nodeConfig, events, NewDefaultBlockProposer(), cfg.Window, logger)
}

// 创建新的提案管理器（可注入自定义提案者）
func NewProposalManagerWithProposer(nodeID types.NodeID, transport interfaces.Transport, store interfaces.BlockStore, nodeConfig *NodeConfig, events interfaces.EventBus, proposer interfaces.BlockProposer, windowConfig config.WindowConfig, logger logs.Logger) *ProposalManager {
	return &ProposalManager{
		nodeID:          nodeID,
		transport:       transport,
		store:           store,
		nodeConfig:      nodeConfig, // Renamed from 'config'
		windowConfig:    windowConfig,
		events:          events,
		Logger:          logger,
		proposedBlocks:  make(map[string]bool),
		proposer:        proposer,
		lastBlockTime:   time.Now(), // 初始化为当前时间
		cachedProposals: make(map[int][]*types.Block),
	}
}

func (pm *ProposalManager) Start(ctx context.Context) {
	// 订阅区块最终化事件
	pm.events.Subscribe(types.EventBlockFinalized, pm.handleBlockFinalized)

	go func() {
		logs.SetThreadNodeContext(string(pm.nodeID))
		ticker := time.NewTicker(pm.nodeConfig.ProposalInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				pm.proposeBlock()
			case <-ctx.Done():
				return
			}
		}
	}()
}

// handleBlockFinalized 处理区块最终化事件
func (pm *ProposalManager) handleBlockFinalized(event interfaces.Event) {
	if block, ok := event.Data().(*types.Block); ok {
		pm.UpdateLastBlockTime(time.Now())
		logs.Debug("[ProposalManager] Block %s finalized, updated last block time", block.ID)
	}
}

func (pm *ProposalManager) proposeBlock() {
	pm.mu.Lock()

	// 计算当前window
	currentWindow := pm.calculateCurrentWindow()
	pm.proposalWindow = currentWindow
	lastBlockTime := pm.lastBlockTime

	pm.mu.Unlock()

	// 先检查缓存中是否有可以处理的提案
	pm.processCachedProposals(currentWindow)

	// 获取最后接受的高度
	_, lastHeight := pm.store.GetLastAccepted()
	targetHeight := lastHeight + 1

	// 关键修复：使用已最终化的父区块，而不是"最后接受"的区块
	// 这防止了基于未达成共识的区块提议新块，避免分叉
	parentBlock, exists := pm.store.GetFinalizedAtHeight(lastHeight)
	if !exists {
		// 如果父区块未最终化，等待共识完成
		logs.Debug("[ProposalManager] Parent block at height %d not finalized yet, skipping proposal", lastHeight)
		return
	}
	parentID := parentBlock.ID

	// 只统计“基于已最终化父块”的候选，否则会出现：
	// - height=N+1 先产生了很多基于非最终化父块的候选
	// - height=N 最终化后，这些候选全部因 parent mismatch 变成无效候选
	// - 但 currentBlocks 已达到上限，导致后续不再出块，最终永久卡住
	currentBlocks := 0
	for _, b := range pm.store.GetByHeight(targetHeight) {
		if b != nil && b.ParentID == parentID {
			currentBlocks++
		}
	}

	// 判断是否应该在当前window提出区块
	if !pm.proposer.ShouldPropose(pm.nodeID, currentWindow, currentBlocks, int(lastHeight), int(targetHeight), lastBlockTime) {
		return
	}

	block, err := pm.proposer.ProposeBlock(parentID, targetHeight, pm.nodeID, currentWindow)
	if err != nil {
		Logf("[Node %s] Failed to propose block: %v\n", pm.nodeID, err)
		return
	}
	if block == nil {
		logs.Debug("[ProposalManager] no block proposed (pending txs=0?) at height=%d window=%d",
			targetHeight, currentWindow)
		return
	}

	pm.mu.Lock()
	if pm.proposedBlocks[block.ID] {
		pm.mu.Unlock()
		return
	}
	pm.proposedBlocks[block.ID] = true
	pm.mu.Unlock()

	isNew, err := pm.store.Add(block)
	if err != nil || !isNew {
		return
	}

	if pm.node != nil {
		pm.node.Stats.Mu.Lock()
		pm.node.Stats.BlocksProposed++
		pm.node.Stats.Mu.Unlock()
	}

	Logf("[Node %s] Proposing %s at height %d on parent %s (window %d)\n",
		pm.nodeID, block.ID, block.Height, parentID, currentWindow)

	pm.events.PublishAsync(types.BaseEvent{
		EventType: types.EventNewBlock,
		EventData: block,
	})

	// 提议者立即发起PushQuery来传播自己的区块
	if pm.node != nil && pm.node.queryManager != nil {
		go func() {
			logs.SetThreadNodeContext(string(pm.nodeID))
			pm.node.queryManager.tryIssueQuery()
		}()
	}
}

// SetProposer 允许运行时更换提案者实现
func (pm *ProposalManager) SetProposer(proposer interfaces.BlockProposer) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.proposer = proposer
}

// calculateCurrentWindow 根据上次出块时间计算当前window
// 使用配置中的window阶段定义
func (pm *ProposalManager) calculateCurrentWindow() int {
	if !pm.windowConfig.Enabled || len(pm.windowConfig.Stages) == 0 {
		return 0 // 如果未启用或没有配置，返回window 0
	}

	elapsed := time.Since(pm.lastBlockTime)
	var cumulativeDuration time.Duration

	// 遍历配置的阶段，找到当前所属的window
	for i, stage := range pm.windowConfig.Stages {
		if stage.Duration == 0 {
			// Duration为0表示最后一个无限阶段
			return i
		}
		cumulativeDuration += stage.Duration
		if elapsed < cumulativeDuration {
			return i
		}
	}

	// 如果超过所有阶段，返回最后一个window
	return len(pm.windowConfig.Stages) - 1
}

// 处理缓存中符合当前window的提案
func (pm *ProposalManager) processCachedProposals(currentWindow int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// 处理所有小于等于当前window的缓存提案
	for window := 0; window <= currentWindow; window++ {
		if proposals, exists := pm.cachedProposals[window]; exists {
			for _, block := range proposals {
				// 尝试添加到区块存储
				isNew, err := pm.store.Add(block)
				if err == nil && isNew {
					logs.Info("[ProposalManager] Processed cached proposal %s from window %d", block.ID, window)

					// 发布事件
					pm.events.PublishAsync(types.BaseEvent{
						EventType: types.EventNewBlock,
						EventData: block,
					})
				}
			}
			// 清除已处理的缓存
			delete(pm.cachedProposals, window)
		}
	}
}

// CacheProposal 缓存一个提案（当收到的提案window大于当前window时）
func (pm *ProposalManager) CacheProposal(block *types.Block) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	currentWindow := pm.calculateCurrentWindow()

	// 只缓存未来window的提案
	if block.Window > currentWindow {
		pm.cachedProposals[block.Window] = append(pm.cachedProposals[block.Window], block)
		logs.Debug("[ProposalManager] Cached proposal %s for future window %d (current: %d)",
			block.ID, block.Window, currentWindow)
	}
}

// UpdateLastBlockTime 更新上次出块时间（当区块被最终化时调用）
func (pm *ProposalManager) UpdateLastBlockTime(t time.Time) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.lastBlockTime = t
	logs.Debug("[ProposalManager] Updated last block time to %v", t)
}

// ResetProposalTimer 重置出块计时器，用于网络启动时的零点对齐
func (pm *ProposalManager) ResetProposalTimer() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.lastBlockTime = time.Now()
	logs.Info("[ProposalManager] Proposal timer reset to current time")
}
