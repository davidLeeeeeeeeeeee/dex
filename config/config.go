// config/config.go
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Config 主配置结构
type Config struct {
	Server   ServerConfig
	Database DatabaseConfig
	Network  NetworkConfig
	TxPool   TxPoolConfig
	Sender   SenderConfig
	Auth     AuthConfig
	Window   WindowConfig  // 新增：Window配置
	Frost    FrostConfig   // FROST门限签名配置
	Witness  WitnessConfig // 见证者模块配置
}

// ServerConfig HTTP/3服务器配置
type ServerConfig struct {
	// TLS配置
	TLSMinVersion string // "1.3"
	TLSMaxVersion string // "1.3"

	// QUIC配置
	QUICKeepAlivePeriod time.Duration // 10 * time.Second
	QUICMaxIdleTimeout  time.Duration // 5 * time.Minute
	QUICAllow0RTT       bool          // true

	// HTTP配置
	HTTPTimeout        time.Duration // 30 * time.Second
	MaxRequestBodySize int64         // 10 << 20 (10MB)

	// 证书配置
	CertValidityDays    int // 365
	TLSSessionCacheSize int // 128
}

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	// BadgerDB配置
	ValueLogFileSize int64         // 64 << 20 (64MB)
	BaseTableSize    int64         // 基础表大小
	MemTableSize     int64         // 内存表大小
	MaxBatchSize     int           // 100
	FlushInterval    time.Duration // 200 * time.Millisecond
	// 最终化落盘策略：不再每块都强制 flush，而是按块数/时间窗口触发
	FinalizationForceFlushEveryN   int           // 0=关闭按块数触发；1=每块强刷（旧行为）
	FinalizationForceFlushInterval time.Duration // 0=关闭按时间触发

	// 写队列配置
	WriteQueueSize      int
	WriteBatchSoftLimit int64 // 8 * 1024 * 1024 (8MB)
	MaxCountPerTxn      int   // 500
	PerEntryOverhead    int   // 32

	// 缓存配置
	BlockCacheSize    int    // 10
	SequenceBandwidth uint64 // 1000

	// 内存限制配置（新增）
	IndexCacheSize          int64 // 索引缓存大小（字节），限制 Ristretto 缓存
	BlockCacheSizeDB        int64 // 块缓存大小（字节），限制数据块缓存
	NumMemtables            int   // MemTable 数量，减少内存占用
	NumCompactors           int   // 后台压缩线程数，设为 1 可平抑内存峰值
	VerkleKVLogEnabled      bool  // 是否启用 Verkle 每高度全量 KV 日志（高开销，仅排障使用）
	VerkleDisableRootCommit bool  // 统一开关：禁用 Verkle 根承诺计算并跳过 Verkle 树写入（仅用于排查）
}

// NetworkConfig 网络配置
type NetworkConfig struct {
	// 基础网络配置
	BasePort        int // 6000
	MaxNodes        int // 100
	DefaultNumNodes int // 100
	ByzantineNodes  int // 0

	// 连接池配置
	PeerSampleSize     int // 10
	RandomMinerCount   int // 3
	BroadcastPeerCount int // 5
	MaxBroadcastPeers  int // 20

	// 超时配置
	NetworkLatency    time.Duration // 100 * time.Millisecond
	ConnectionTimeout time.Duration // 5 * time.Second
	HandshakeTimeout  time.Duration // 10 * time.Second

	// 网络模拟配置（用于测试和开发）
	PacketLossRate float64       // 丢包率 (0.0-1.0)
	MinLatency     time.Duration // 最小延迟
	MaxLatency     time.Duration // 最大延迟
}

// TxPoolConfig 交易池配置
type TxPoolConfig struct {
	// 缓存大小
	PendingTxCacheSize      int // 100000
	ShortPendingTxCacheSize int // 100000
	CacheTxSize             int // 100000
	ShortTxCacheSize        int // 100000

	// 队列配置
	MessageQueueSize int // 10000
	MaxPendingTxs    int // 10000

	// 交易处理
	MaxTxsPerBlock      int // 2500 (ShortTxs 切换阈值)
	MaxTxsLimitPerBlock int // 5000 (区块最大交易上限)
	TxPerMerkleTree     int // 1000
	ShortHashSize       int // 8
	MinTxsForProposal   int // 1

	// 时间配置
	TxExpirationTime time.Duration // 24 * time.Hour
}

// SenderConfig 发送器配置
type SenderConfig struct {
	// 队列配置
	WorkerCount   int // 100
	QueueCapacity int // 10000

	// 按目标并发限流（<=0 表示不限制）
	ControlMaxInflightPerTarget   int           // 控制面每目标最大在途请求
	DataMaxInflightPerTarget      int           // 数据面每目标最大在途请求
	ImmediateMaxInflightPerTarget int           // 紧急面每目标最大在途请求
	InflightRequeueDelay          time.Duration // 因目标过载而回队的基础延迟

	// 重试配置
	DefaultMaxRetries int           // 3
	ControlMaxRetries int           // 2
	DataMaxRetries    int           // 1
	JitterFactor      float64       // 0.3 (±30% 随机抖动)
	TaskExpireTimeout time.Duration // 3s，超过此时间的任务直接丢弃

	// 超时配置
	BaseRetryDelay      time.Duration // 100ms (指数退避基础延迟)
	MaxRetryDelay       time.Duration // 2s (最大退避延迟)
	ControlTaskTimeout  time.Duration // 80 * time.Millisecond
	DataTaskDropTimeout time.Duration // 0 (立即丢弃)
}

// AuthConfig 认证配置
type AuthConfig struct {
	AuthEnabled     bool   // false
	ServerChallenge string // "server_challenge_123456"
}

// MinerConfig 矿工相关配置
type MinerConfig struct {
	// 索引管理
	MaxMinerIndex  uint64 // 可配置的最大矿工索引
	IndexCacheSize int    // 索引缓存大小

	// Stake配置
	MaxStake            string // "1e30"
	StakeIndexPadLength int    // 32

	// BLS签名缓存
	BLSSignCacheSize int // 100
}

// WindowStage 时间窗口阶段配置
type WindowStage struct {
	Duration    time.Duration // 阶段持续时间
	Probability float64       // 出块概率（0.0-1.0）
}

// WindowConfig 时间窗口配置
type WindowConfig struct {
	Enabled bool          // 是否启用Window机制
	Stages  []WindowStage // 各个阶段的配置
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			TLSMinVersion:       "1.3",
			TLSMaxVersion:       "1.3",
			QUICKeepAlivePeriod: 10 * time.Second,
			QUICMaxIdleTimeout:  5 * time.Minute,
			QUICAllow0RTT:       true,
			HTTPTimeout:         30 * time.Second,
			MaxRequestBodySize:  10 << 20,
			CertValidityDays:    365,
			TLSSessionCacheSize: 128,
		},
		Database: DatabaseConfig{
			ValueLogFileSize:               16 << 20,
			BaseTableSize:                  16 << 20, // 恢复到 16MB 以匹配 ValueThreshold
			MemTableSize:                   8 << 20,  // 设置为 8MB
			MaxBatchSize:                   1000,
			FlushInterval:                  500 * time.Millisecond,
			FinalizationForceFlushEveryN:   3,
			FinalizationForceFlushInterval: 10 * time.Second,
			WriteQueueSize:                 5000,
			WriteBatchSoftLimit:            8 * 1024 * 1024,
			MaxCountPerTxn:                 500,
			PerEntryOverhead:               32,
			BlockCacheSize:                 10,
			SequenceBandwidth:              1000,
			// 内存限制配置
			// 适配模拟器多实例环境（20节点 * 2实例 = 40个数据库）
			// 必须大幅调低单实例缓存，否则总内存会爆炸
			IndexCacheSize:          16 << 20, // 降低到 16MB
			BlockCacheSizeDB:        32 << 20, // 降低到 32MB
			NumMemtables:            3,        // 减少 MemTable 数量
			NumCompactors:           2,
			VerkleKVLogEnabled:      true, // false,
			VerkleDisableRootCommit: false,
		},
		Network: NetworkConfig{
			BasePort:           6000,
			MaxNodes:           100,
			DefaultNumNodes:    20,
			ByzantineNodes:     0,
			PeerSampleSize:     10,
			RandomMinerCount:   3,
			BroadcastPeerCount: 5,
			MaxBroadcastPeers:  20,
			NetworkLatency:     100 * time.Millisecond,
			ConnectionTimeout:  10 * time.Second,
			HandshakeTimeout:   10 * time.Second,
			PacketLossRate:     0.0,
			MinLatency:         0,
			MaxLatency:         0,
		},
		TxPool: TxPoolConfig{
			PendingTxCacheSize:      10000,
			ShortPendingTxCacheSize: 10000,
			CacheTxSize:             10000,
			ShortTxCacheSize:        10000,
			MessageQueueSize:        10000,
			MaxPendingTxs:           5000,
			MaxTxsPerBlock:          2500,
			MaxTxsLimitPerBlock:     2500,
			TxPerMerkleTree:         1000,
			ShortHashSize:           8,
			MinTxsForProposal:       1,
			TxExpirationTime:        24 * time.Hour,
		},
		Sender: SenderConfig{
			WorkerCount:                   200,
			QueueCapacity:                 200,
			ControlMaxInflightPerTarget:   3,
			DataMaxInflightPerTarget:      6,
			ImmediateMaxInflightPerTarget: 2,
			InflightRequeueDelay:          25 * time.Millisecond,
			DefaultMaxRetries:             0, // 不重试，依赖 QueryManager 周期性发起新 Query
			ControlMaxRetries:             0, // 共识消息不重试
			DataMaxRetries:                0, // 数据消息不重试
			JitterFactor:                  0.3,
			TaskExpireTimeout:             10 * time.Second, // 超过 10s 的任务直接丢弃
			BaseRetryDelay:                100 * time.Millisecond,
			MaxRetryDelay:                 2 * time.Second,
			ControlTaskTimeout:            180 * time.Millisecond,
			DataTaskDropTimeout:           0,
		},
		Auth: AuthConfig{
			AuthEnabled:     false,
			ServerChallenge: "server_challenge_123456",
		},
		Window: WindowConfig{
			Enabled: true,
			Stages: []WindowStage{
				{Duration: 5 * time.Second, Probability: 0.05},  // Window 0: 0-5s, 5%概率
				{Duration: 5 * time.Second, Probability: 0.15},  // Window 1: 5-10s, 15%概率
				{Duration: 10 * time.Second, Probability: 0.30}, // Window 2: 10-20s, 30%概率
				{Duration: 0, Probability: 0.50},                // Window 3: 20s+, 50%概率（Duration=0表示无限）
			},
		},
		Frost:   DefaultFrostConfig(),
		Witness: DefaultWitnessConfig(),
	}
}

// LoadFromFile 从文件加载配置（可选实现）
func LoadFromFile(path string) (*Config, error) {
	cfg := DefaultConfig()

	// 尝试加载 Frost 配置
	frostCfg, err := LoadFrostConfig("config/frost_default.json")
	if err == nil {
		cfg.Frost = frostCfg
		fmt.Println("📜 Loaded frost config from config/frost_default.json")
	}

	// 尝试加载 Witness 配置
	witnessCfg, err := LoadWitnessConfig("config/witness_default.json")
	if err == nil {
		cfg.Witness = witnessCfg
		fmt.Println("📜 Loaded witness config from config/witness_default.json")
	}

	configPath := strings.TrimSpace(path)
	if configPath == "" {
		configPath = "config/config.json"
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		// If default path is missing, keep defaults.
		if os.IsNotExist(err) && strings.TrimSpace(path) == "" {
			return cfg, nil
		}
		return nil, fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	// File values override defaults.
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", configPath, err)
	}
	fmt.Printf("Loaded config from %s\n", configPath)

	return cfg, nil
}

// Validate 验证配置合法性
func (c *Config) Validate() error {
	// 添加配置验证逻辑
	if c.Network.MaxNodes <= 0 {
		return fmt.Errorf("MaxNodes must be positive")
	}
	if c.TxPool.MaxTxsPerBlock <= 0 {
		return fmt.Errorf("MaxTxsPerBlock must be positive")
	}
	if c.TxPool.MaxTxsLimitPerBlock <= 0 {
		return fmt.Errorf("MaxTxsLimitPerBlock must be positive")
	}
	// ... 其他验证
	return nil
}

// ===================== Genesis 配置 =====================

// GenesisConfig 创世配置
type GenesisConfig struct {
	// 系统代币
	Tokens []GenesisToken `json:"tokens"`
	// 预分配账户余额
	Alloc map[string]GenesisAlloc `json:"alloc"`
}

// GenesisToken 创世代币定义
type GenesisToken struct {
	Address     string `json:"address"`      // 代币地址（系统代币可用符号如 "FB"）
	Symbol      string `json:"symbol"`       // 代币符号
	Name        string `json:"name"`         // 代币名称
	TotalSupply string `json:"total_supply"` // 总供应量
	Owner       string `json:"owner"`        // 拥有者地址（"0x0" 表示无拥有者）
	CanMint     bool   `json:"can_mint"`     // 是否可增发
}

// GenesisAlloc 创世余额分配
type GenesisAlloc struct {
	Balances map[string]GenesisBalance `json:"balances"` // key 为代币地址/符号
}

// GenesisBalance 代币余额
type GenesisBalance struct {
	Balance            string `json:"balance"`                        // 可用余额
	MinerLockedBalance string `json:"miner_locked_balance,omitempty"` // 挖矿锁定余额
}

// DefaultGenesisConfig 返回默认创世配置
func DefaultGenesisConfig() *GenesisConfig {
	return &GenesisConfig{
		Tokens: []GenesisToken{
			{
				Address:     "FB",
				Symbol:      "FB",
				Name:        "FrostBit Native Token",
				TotalSupply: "1000000000000",
				Owner:       "0x0",
				CanMint:     false,
			},
			{
				Address:     "USDT",
				Symbol:      "USDT",
				Name:        "Tether USD",
				TotalSupply: "1000000000000",
				Owner:       "0x0",
				CanMint:     true,
			},
		},
		Alloc: make(map[string]GenesisAlloc),
	}
}

// LoadGenesisConfig 从文件加载创世配置
func LoadGenesisConfig(path string) (*GenesisConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 文件不存在，返回默认配置
			return DefaultGenesisConfig(), nil
		}
		return nil, fmt.Errorf("failed to read genesis config: %w", err)
	}

	var genesis GenesisConfig
	if err := json.Unmarshal(data, &genesis); err != nil {
		return nil, fmt.Errorf("failed to parse genesis config: %w", err)
	}

	return &genesis, nil
}

// SaveGenesisConfig 保存创世配置到文件
func SaveGenesisConfig(path string, genesis *GenesisConfig) error {
	data, err := json.MarshalIndent(genesis, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal genesis config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write genesis config: %w", err)
	}

	return nil
}
