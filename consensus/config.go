package consensus

import "time"

// Config groups all consensus-related runtime settings.
type Config struct {
	Network   NetworkConfig
	Consensus ConsensusConfig
	Node      NodeConfig
	Sync      SyncConfig
	Gossip    GossipConfig
	Snapshot  SnapshotConfig
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
	CheckInterval       time.Duration
	BehindThreshold     uint64
	BatchSize           uint64
	Timeout             time.Duration
	SnapshotThreshold   uint64
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

type SnapshotConfig struct {
	Interval     uint64
	MaxSnapshots int
	Enabled      bool
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
			CheckInterval:       30 * time.Second,
			BehindThreshold:     2,
			BatchSize:           50,
			Timeout:             10 * time.Second,
			SnapshotThreshold:   100,
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
		Snapshot: SnapshotConfig{
			Interval:     100,
			MaxSnapshots: 10,
			Enabled:      true,
		},
	}
}
