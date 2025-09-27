package consensus

import "sync"

// ============================================
// 节点统计
// ============================================

type NodeStats struct {
	mu               sync.Mutex
	QueriesSent      uint32
	QueriesReceived  uint32
	ChitsResponded   uint32
	queriesPerHeight map[uint64]uint32
	BlocksProposed   uint32
	GossipsReceived  uint32
	snapshotsUsed    uint32
	snapshotsServed  uint32
}

func NewNodeStats() *NodeStats {
	return &NodeStats{
		queriesPerHeight: make(map[uint64]uint32),
	}
}
