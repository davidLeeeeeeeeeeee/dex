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
	PacketLossRate    float64 //丢包率，范围 0.0 到 1.0
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
	CheckInterval      time.Duration
	BehindThreshold    uint64
	BatchSize          uint64
	Timeout            time.Duration
	SnapshotThreshold  uint64        // 落后多少高度时使用快照
	ShortSyncThreshold uint64        // 短期落后阈值（低于此值用ShortTxs，高于此值用完整区块）
	ParallelPeers      int           // 分片并行请求的节点数（默认 3）
	SampleSize         int           // 采样节点数量（默认 15）
	QuorumRatio        float64       // Quorum 比例（默认 0.67，即 67%）
	SampleTimeout      time.Duration // 采样超时时间
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
			PacketLossRate:    0.1, // 10% 丢包率
		},
		Consensus: ConsensusConfig{
			K:                    20,
			Alpha:                14,
			Beta:                 15,
			QueryTimeout:         4 * time.Second,
			MaxConcurrentQueries: 20,
			NumHeights:           10,
			BlocksPerHeight:      5,
		},
		Node: NodeConfig{
			ProposalInterval: 3000 * time.Millisecond,
		},
		Sync: SyncConfig{
			CheckInterval:      30 * time.Second, // 降频作为兜底（事件驱动为主）
			BehindThreshold:    2,
			BatchSize:          50,              // 增大批量以加速追赶
			Timeout:            3 * time.Second, // 缩短超时减少等待
			SnapshotThreshold:  100,
			ShortSyncThreshold: 20,              // 落后<=20块用ShortTxs，>20块用完整区块
			ParallelPeers:      3,               // 并行向3个节点请求不同高度范围
			SampleSize:         15,              // 采样 15 个节点
			QuorumRatio:        0.67,            // 67% 拜占庭容错
			SampleTimeout:      2 * time.Second, // 2 秒采样超时
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
