package consensus

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// DefaultBlockProposer 默认的区块提案者实现（保持原有逻辑）
type DefaultBlockProposer struct {
	maxBlocksPerHeight int
	proposalDenom      int
}

func NewDefaultBlockProposer() BlockProposer {
	return &DefaultBlockProposer{
		maxBlocksPerHeight: 3,
		proposalDenom:      33,
	}
}

func (p *DefaultBlockProposer) ProposeBlock(parentID string, height uint64, proposer NodeID, round int) (*Block, error) {
	blockID := fmt.Sprintf("block-%d-%d-r%d", height, proposer, round)

	block := &Block{
		ID:       blockID,
		Height:   height,
		ParentID: parentID,
		Data:     fmt.Sprintf("Height %d, Proposer %d, Round %d", height, proposer, round),
		Proposer: int(proposer),
		Round:    round,
	}

	return block, nil
}

func (p *DefaultBlockProposer) ShouldPropose(nodeID NodeID, round int, currentBlocks int) bool {
	// 如果当前高度已有足够多的区块，不再提案
	if currentBlocks >= p.maxBlocksPerHeight {
		return false
	}

	// 使用原有的随机选择逻辑
	denom := p.proposalDenom
	if denom <= 0 {
		denom = 1
	}

	return int(nodeID+NodeID(round))%denom == 0
}

type ProposalManager struct {
	nodeID         NodeID
	node           *Node
	transport      Transport
	store          BlockStore
	config         *NodeConfig
	events         EventBus
	proposedBlocks map[string]bool
	proposalRound  int
	mu             sync.Mutex
	proposer       BlockProposer // 新增：注入的提案者接口
}

// NewProposalManager 创建新的提案管理器（使用默认提案者）
func NewProposalManager(nodeID NodeID, transport Transport, store BlockStore, config *NodeConfig, events EventBus) *ProposalManager {
	return NewProposalManagerWithProposer(nodeID, transport, store, config, events, NewDefaultBlockProposer())
}

// NewProposalManagerWithProposer 创建新的提案管理器（可注入自定义提案者）
func NewProposalManagerWithProposer(nodeID NodeID, transport Transport, store BlockStore, config *NodeConfig, events EventBus, proposer BlockProposer) *ProposalManager {
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

	// 获取当前高度的区块数量
	currentBlocks := len(pm.store.GetByHeight(targetHeight))

	// 使用接口判断是否应该提案
	if !pm.proposer.ShouldPropose(pm.nodeID, currentRound, currentBlocks) {
		return
	}

	// 使用接口生成区块
	block, err := pm.proposer.ProposeBlock(lastAcceptedID, targetHeight, pm.nodeID, currentRound)
	if err != nil {
		Logf("[Node %d] Failed to propose block: %v\n", pm.nodeID, err)
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
		pm.node.stats.blocksProposed++
		pm.node.stats.mu.Unlock()
	}

	Logf("[Node %d] Proposing %s on parent %s\n", pm.nodeID, block, lastAcceptedID)

	pm.events.Publish(BaseEvent{
		eventType: EventNewBlock,
		data:      block,
	})
}

// SetProposer 允许运行时更换提案者实现
func (pm *ProposalManager) SetProposer(proposer BlockProposer) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.proposer = proposer
}
