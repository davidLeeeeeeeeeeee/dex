// consensus/simple_consensus.go
package consensus

import (
	"dex/config"
	"dex/db"
	"dex/interfaces"
	"fmt"
)

type SimpleConsensus struct {
	config    config.ConsensusConfig
	dbManager interfaces.DBManager
}

func NewSimpleConsensus(cfg config.ConsensusConfig, dbManager interfaces.DBManager) *SimpleConsensus {
	return &SimpleConsensus{
		config:    cfg,
		dbManager: dbManager,
	}
}

// Start 实现 interfaces.Consensus
func (c *SimpleConsensus) Start() error {
	// 实现启动逻辑
	return nil
}

// Stop 实现 interfaces.Consensus
func (c *SimpleConsensus) Stop() error {
	// 实现停止逻辑
	return nil
}

// Run 实现 interfaces.Consensus
func (c *SimpleConsensus) Run(ctx interface{}) {
	// 类型断言为 context.Context
	if _, ok := ctx.(context.Context); ok {
		// 运行共识主循环
		// <-context.Done()
	}
}

// CheckAnyTx 实现 interfaces.Consensus - 接受 interface{}
func (c *SimpleConsensus) CheckAnyTx(tx interface{}) error {
	// 类型断言为具体类型
	anyTx, ok := tx.(*db.AnyTx)
	if !ok {
		return fmt.Errorf("invalid tx type")
	}

	// 简单的验证逻辑
	base := anyTx.GetBase()
	if base == nil {
		return fmt.Errorf("missing base message")
	}

	if base.TxId == "" {
		return fmt.Errorf("empty tx id")
	}

	if base.FromAddress == "" {
		return fmt.Errorf("empty from address")
	}

	return nil
}

// DistributeRewards 实现 interfaces.Consensus
func (c *SimpleConsensus) DistributeRewards(blockHash string, minerAddress string) error {
	// 奖励分发逻辑
	return nil
}

// ProposeBlock 实现 interfaces.Consensus - 返回 interface{}
func (c *SimpleConsensus) ProposeBlock(height uint64) (interface{}, error) {
	// 区块提案逻辑
	return &db.Block{Height: height}, nil
}

// ValidateBlock 实现 interfaces.Consensus - 接受 interface{}
func (c *SimpleConsensus) ValidateBlock(block interface{}) error {
	// 类型断言
	blk, ok := block.(*db.Block)
	if !ok {
		return fmt.Errorf("invalid block type")
	}

	// 区块验证逻辑
	if blk.Height == 0 {
		return fmt.Errorf("invalid block height")
	}

	return nil
}

// NewSnowmanConsensus 创建 Snowman 共识（占位实现）
func NewSnowmanConsensus(cfg config.ConsensusConfig, dbManager interfaces.DBManager, network interfaces.Network) interfaces.Consensus {
	// 暂时返回 SimpleConsensus，后续可以实现真正的 Snowman
	return &SimpleConsensus{
		config:    cfg,
		dbManager: dbManager,
	}
}
