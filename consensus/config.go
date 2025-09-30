package consensus

import "time"

// ============================================
// 配置管理
// ============================================

type Config struct {
	Network   NetworkConfig
	Consensus ConsensusConfig
	Node      NodeConfig
	Sync      SyncConfig
	Gossip    GossipConfig
	Snapshot  SnapshotConfig // 新增
}

type NetworkConfig struct {
	NumNodes          int
	NumByzantineNodes int
	NetworkLatency    time.Duration
}

type ConsensusConfig struct {
	K                    int
	Alpha                int
	Beta                 int
	QueryTimeout         time.Duration
	MaxConcurrentQueries int
	NumHeights           int
	BlocksPerHeight      int
}

type NodeConfig struct {
	ProposalInterval time.Duration
}

type SyncConfig struct {
	CheckInterval     time.Duration
	BehindThreshold   uint64
	BatchSize         uint64
	Timeout           time.Duration
	SnapshotThreshold uint64 // 新增：落后多少高度时使用快照
}

type GossipConfig struct {
	Fanout   int
	Interval time.Duration
}

// 新增：快照配置
type SnapshotConfig struct {
	Interval     uint64 // 每多少个区块创建一次快照
	MaxSnapshots int    // 最多保留多少个快照
	Enabled      bool   // 是否启用快照功能
}

func DefaultConfig() *Config {
	return &Config{
		Network: NetworkConfig{
			NumNodes:          100,
			NumByzantineNodes: 10,
			NetworkLatency:    100 * time.Millisecond,
		},
		Consensus: ConsensusConfig{
			K:                    20,
			Alpha:                15,
			Beta:                 15,
			QueryTimeout:         3 * time.Second,
			MaxConcurrentQueries: 20,
			NumHeights:           10,
			BlocksPerHeight:      5,
		},
		Node: NodeConfig{
			ProposalInterval: 1200 * time.Millisecond,
		},
		Sync: SyncConfig{
			CheckInterval:     2 * time.Second,
			BehindThreshold:   2,
			BatchSize:         10,
			Timeout:           5 * time.Second,
			SnapshotThreshold: 100, // 新增：落后100个高度时使用快照
		},
		Gossip: GossipConfig{
			Fanout:   15,
			Interval: 50 * time.Millisecond,
		},
		Snapshot: SnapshotConfig{
			Interval:     100,  // 每100个区块一个快照
			MaxSnapshots: 10,   // 最多保留10个快照
			Enabled:      true, // 启用快照
		},
	}
}
