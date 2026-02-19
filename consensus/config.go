package consensus

import "time"

// Config groups all consensus-related runtime settings.
type Config struct {
	Network   NetworkConfig
	Consensus ConsensusConfig
	Node      NodeConfig
	Sync      SyncConfig
	Gossip    GossipConfig
}

type NetworkConfig struct {
	NumNodes          int
	NumByzantineNodes int
	NetworkLatency    time.Duration
	PacketLossRate    float64
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
	CheckInterval   time.Duration
	BehindThreshold uint64
	BatchSize       uint64
	Timeout         time.Duration
	// DeepLagStateSyncThreshold 深度落后阈值（区块数）。
	// 当事件驱动同步检测到落后超过该值时，先走 stateDB-first 追赶路径，再进入常规追块流水线。
	DeepLagStateSyncThreshold uint64
	// StateSyncPeers 深度落后时用于分片 stateDB 拉取的最大 peer 数。
	StateSyncPeers int
	// StateSyncShardConcurrency 分片拉取并发度。
	StateSyncShardConcurrency int
	// StateSyncPageSize 单次分页拉取条目数。
	StateSyncPageSize int

	ShortSyncThreshold  uint64
	ParallelPeers       int
	SampleSize          int
	QuorumRatio         float64
	SampleTimeout       time.Duration
	SyncAlpha           int
	SyncBeta            int
	ChitSoftGap         uint64
	ChitHardGap         uint64
	ChitGracePeriod     time.Duration
	ChitCooldown        time.Duration
	ChitMinConfirmPeers int
}

type GossipConfig struct {
	Fanout   int
	Interval time.Duration
}

func DefaultConfig() *Config {
	return &Config{
		Network: NetworkConfig{
			NumNodes:          100,
			NumByzantineNodes: 10,
			NetworkLatency:    100 * time.Millisecond,
			PacketLossRate:    0.1,
		},
		Consensus: ConsensusConfig{
			K:                    20,
			Alpha:                14,
			Beta:                 15,
			QueryTimeout:         4 * time.Second,
			MaxConcurrentQueries: 8,
			NumHeights:           10,
			BlocksPerHeight:      5,
		},
		Node: NodeConfig{
			ProposalInterval: 3000 * time.Millisecond,
		},
		Sync: SyncConfig{
			CheckInterval:   30 * time.Second,
			BehindThreshold: 2,
			BatchSize:       50,
			Timeout:         10 * time.Second,
			// 落后超过 100 个块时，先进行 stateDB-first 追赶，再继续常规追块。
			DeepLagStateSyncThreshold: 100,
			// stateDB 分片同步默认并发参数（分担单节点压力）。
			StateSyncPeers:            4,
			StateSyncShardConcurrency: 8,
			StateSyncPageSize:         1000,

			ShortSyncThreshold:  20,
			ParallelPeers:       3,
			SampleSize:          15,
			QuorumRatio:         0.67,
			SampleTimeout:       2 * time.Second,
			SyncAlpha:           14,
			SyncBeta:            15,
			ChitSoftGap:         1,
			ChitHardGap:         3,
			ChitGracePeriod:     1 * time.Second,
			ChitCooldown:        1500 * time.Millisecond,
			ChitMinConfirmPeers: 2,
		},
		Gossip: GossipConfig{
			Fanout:   15,
			Interval: 50 * time.Millisecond,
		},
	}
}
