package consensus

import (
	"context"
)

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
	if context, ok := ctx.(context.Context); ok {
		// 运行共识主循环
		<-context.Done()
	}
}
