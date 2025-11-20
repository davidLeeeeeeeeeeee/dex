package consensus

import (
	"dex/interfaces"
	"dex/types"
	"fmt"
	"time"
)

// DefaultBlockProposer 默认的区块提案者实现（保持原有逻辑）
type DefaultBlockProposer struct {
	maxBlocksPerHeight int
	proposalDenom      int
}

func NewDefaultBlockProposer() interfaces.BlockProposer {
	return &DefaultBlockProposer{
		maxBlocksPerHeight: 3,
		proposalDenom:      33,
	}
}

func (p *DefaultBlockProposer) ProposeBlock(parentID string, height uint64, proposer types.NodeID, window int) (*types.Block, error) {
	blockID := fmt.Sprintf("block-%d-%d-w%d", height, proposer, window)
	block := &types.Block{
		ID:        blockID,
		Height:    height,
		ParentID:  parentID,
		Data:      fmt.Sprintf("Height %d, Proposer %d, Window %d", height, proposer, window),
		Proposer:  string(proposer),
		Window:    window,
		VRFProof:  nil, // 模拟模式不生成VRF
		VRFOutput: nil,
	}
	return block, nil
}

func (p *DefaultBlockProposer) ShouldPropose(nodeID types.NodeID, window int, currentBlocks int, currentHeight int, proposeHeight int, lastBlockTime time.Time) bool {
	// 新增的高度检查逻辑：当前高度必须是要提议高度减1
	if currentHeight != proposeHeight-1 {
		// 当前高度不是 proposeHeight-1，不允许提议
		return false
	}

	// 如果当前高度已有足够多的区块，不再提案
	if currentBlocks >= p.maxBlocksPerHeight {
		return false
	}

	// 使用原有的随机选择逻辑（基于window而不是round）
	denom := p.proposalDenom
	if denom <= 0 {
		denom = 1
	}

	return int(nodeID.Last2Mod100()+window)%denom == 0
}
