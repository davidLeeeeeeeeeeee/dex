package consensus

import (
	"crypto/sha256"
	"dex/interfaces"
	"dex/pb"
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
	// 生成一个简单的 hash 以确保 blockID 格式与 RealBlockProposer 一致
	// 这样 selectByMinHash 才能正确提取并比较 hash
	hashInput := fmt.Sprintf("%s-%d-%s-%d", parentID, height, proposer, window)
	hash := sha256.Sum256([]byte(hashInput))
	hashStr := fmt.Sprintf("%x", hash[:4]) // 取前 4 字节作为 8 字符的 hex

	blockID := fmt.Sprintf("block-%d-%s-w%d-%s", height, string(proposer), window, hashStr)
	block := &types.Block{
		ID: blockID,
		Header: types.BlockHeader{
			Height:    height,
			ParentID:  parentID,
			Proposer:  string(proposer),
			Window:    window,
			VRFProof:  nil, // 模拟模式不生成VRF
			VRFOutput: nil,
		},
	}

	// 关键：同时创建对应的 pb.Block 并填充 BlockCache
	// 这样共识内核的数据完整性检查才能通过
	pbBlock := &pb.Block{
		BlockHash: blockID,
		Header: &pb.BlockHeader{
			Height:        height,
			PrevBlockHash: parentID,
			Miner:         string(proposer),
			Window:        int32(window),
		},
		Body: []*pb.AnyTx{}, // 模拟模式无交易
	}
	CacheBlock(pbBlock)

	return block, nil
}

func (p *DefaultBlockProposer) ShouldPropose(nodeID types.NodeID, window int, currentBlocks int, currentHeight int, proposeHeight int, lastBlockTime time.Time, parentID string) bool {
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
