package consensus

import "sync"

// ============================================
// 节点统计
// ============================================

type NodeStats struct {
	mu               sync.Mutex
	queriesSent      uint32
	queriesReceived  uint32
	chitsResponded   uint32
	queriesPerHeight map[uint64]uint32
	blocksProposed   uint32
	gossipsReceived  uint32
	snapshotsUsed    uint32 // 新增
	snapshotsServed  uint32 // 新增
}

func NewNodeStats() *NodeStats {
	return &NodeStats{
		queriesPerHeight: make(map[uint64]uint32),
	}
}
