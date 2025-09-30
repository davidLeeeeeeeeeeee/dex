package consensus

import (
	"context"
	"dex/interfaces"
	"dex/logs"
	"dex/types"
	"sync"
	"time"
)

type ProposalManager struct {
	nodeID         types.NodeID
	node           *Node
	transport      interfaces.Transport
	store          interfaces.BlockStore
	config         *NodeConfig
	events         interfaces.EventBus
	proposedBlocks map[string]bool
	proposalRound  int
	mu             sync.Mutex
	proposer       interfaces.BlockProposer // 新增：注入的提案者接口
}

// NewProposalManager 创建新的提案管理器（使用默认提案者）
func NewProposalManager(nodeID types.NodeID, transport interfaces.Transport, store interfaces.BlockStore, config *NodeConfig, events interfaces.EventBus) *ProposalManager {
	return NewProposalManagerWithProposer(nodeID, transport, store, config, events, NewDefaultBlockProposer())
}

// NewProposalManagerWithProposer 创建新的提案管理器（可注入自定义提案者）
func NewProposalManagerWithProposer(nodeID types.NodeID, transport interfaces.Transport, store interfaces.BlockStore, config *NodeConfig, events interfaces.EventBus, proposer interfaces.BlockProposer) *ProposalManager {
	return &ProposalManager{
		nodeID:         nodeID,
		transport:      transport,
		store:          store,
		config:         config,
		events:         events,
		proposedBlocks: make(map[string]bool),
		proposer:       proposer,
	}
}

func (pm *ProposalManager) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(pm.config.ProposalInterval)
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

func (pm *ProposalManager) proposeBlock() {
	pm.mu.Lock()
	pm.proposalRound++
	currentRound := pm.proposalRound
	pm.mu.Unlock()

	lastAcceptedID, lastHeight := pm.store.GetLastAccepted()
	targetHeight := lastHeight + 1

	currentBlocks := len(pm.store.GetByHeight(targetHeight))

	if !pm.proposer.ShouldPropose(pm.nodeID, currentRound, currentBlocks) {
		return
	}

	block, err := pm.proposer.ProposeBlock(lastAcceptedID, targetHeight, pm.nodeID, currentRound)
	if err != nil {
		Logf("[Node %d] Failed to propose block: %v\n", pm.nodeID, err)
		return
	}
	if block == nil {
		logs.Debug("[ProposalManager] no block proposed (pending txs=0?) at height=%d round=%d",
			targetHeight, currentRound)
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
		pm.node.stats.mu.Lock()
		pm.node.stats.BlocksProposed++
		pm.node.stats.mu.Unlock()
	}

	Logf("[Node %d] Proposing %s on parent %s\n", pm.nodeID, block, lastAcceptedID)

	pm.events.Publish(types.BaseEvent{
		EventType: types.EventNewBlock,
		EventData: block,
	})

	// 提议者立即发起PushQuery来传播自己的区块
	if pm.node != nil && pm.node.queryManager != nil {
		go func() {
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
