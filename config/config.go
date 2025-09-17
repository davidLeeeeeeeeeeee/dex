// config/config.go
package config

import (
	"time"
)

type Config struct {
	// 基础配置
	DataPath   string
	Port       int
	PrivateKey string

	// 数据库配置
	DB DBConfig

	// 交易池配置
	TxPool TxPoolConfig

	// 网络配置
	Network NetworkConfig

	// 共识配置
	Consensus ConsensusConfig
}

type DBConfig struct {
	Path           string
	WriteQueueSize int
	FlushInterval  time.Duration
}

type TxPoolConfig struct {
	MaxPendingTxs    int
	MaxShortCacheTxs int
	SyncInterval     time.Duration
	QueueSize        int
}

type NetworkConfig struct {
	MaxPeers         int
	HandshakeTimeout time.Duration
}

type ConsensusConfig struct {
	BlockInterval time.Duration
	MaxTxPerBlock int
	UseSnowman    bool
}
