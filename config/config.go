// config/config.go
package config

import (
	"fmt"
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
	MaxBatchSize     int           // 100
	FlushInterval    time.Duration // 200 * time.Millisecond

	// 写队列配置
	WriteQueueSize      int   // 100000
	WriteBatchSoftLimit int64 // 8 * 1024 * 1024 (8MB)
	MaxCountPerTxn      int   // 500
	PerEntryOverhead    int   // 32

	// 缓存配置
	BlockCacheSize    int    // 10
	SequenceBandwidth uint64 // 1000
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
	MaxTxsPerBlock    int // 2500
	TxPerMerkleTree   int // 1000
	ShortHashSize     int // 8
	MinTxsForProposal int // 1

	// 时间配置
	TxExpirationTime time.Duration // 24 * time.Hour
}

// SenderConfig 发送器配置
type SenderConfig struct {
	// 队列配置
	WorkerCount   int // 100
	QueueCapacity int // 10000

	// 重试配置
	DefaultMaxRetries int // 3
	ControlMaxRetries int // 2
	DataMaxRetries    int // 1

	// 超时配置
	BaseRetryDelay      time.Duration // 1 * time.Second
	MaxRetryDelay       time.Duration // 30 * time.Second
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
			ValueLogFileSize:    64 << 20,
			MaxBatchSize:        100,
			FlushInterval:       200 * time.Millisecond,
			WriteQueueSize:      100000,
			WriteBatchSoftLimit: 8 * 1024 * 1024,
			MaxCountPerTxn:      500,
			PerEntryOverhead:    32,
			BlockCacheSize:      10,
			SequenceBandwidth:   1000,
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
			ConnectionTimeout:  5 * time.Second,
			HandshakeTimeout:   10 * time.Second,
		},
		TxPool: TxPoolConfig{
			PendingTxCacheSize:      100000,
			ShortPendingTxCacheSize: 100000,
			CacheTxSize:             100000,
			ShortTxCacheSize:        100000,
			MessageQueueSize:        10000,
			MaxPendingTxs:           10000,
			MaxTxsPerBlock:          2500,
			TxPerMerkleTree:         1000,
			ShortHashSize:           8,
			MinTxsForProposal:       1,
			TxExpirationTime:        24 * time.Hour,
		},
		Sender: SenderConfig{
			WorkerCount:         10000,
			QueueCapacity:       10000,
			DefaultMaxRetries:   3,
			ControlMaxRetries:   2,
			DataMaxRetries:      1,
			BaseRetryDelay:      1 * time.Second,
			MaxRetryDelay:       30 * time.Second,
			ControlTaskTimeout:  180 * time.Millisecond,
			DataTaskDropTimeout: 0,
		},
		Auth: AuthConfig{
			AuthEnabled:     false,
			ServerChallenge: "server_challenge_123456",
		},
	}
}

// LoadFromFile 从文件加载配置（可选实现）
func LoadFromFile(path string) (*Config, error) {
	// 可以实现从JSON/YAML文件加载配置
	// 这里仅返回默认配置作为示例
	return DefaultConfig(), nil
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
	// ... 其他验证
	return nil
}
