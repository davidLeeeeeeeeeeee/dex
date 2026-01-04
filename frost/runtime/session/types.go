// frost/runtime/session/types.go
// ROAST 签名会话类型定义

package session

import (
	"time"
)

// ========== 常量 ==========

const (
	// DefaultNonceTimeout nonce 收集超时
	DefaultNonceTimeout = 5 * time.Second
	// DefaultShareTimeout 签名份额收集超时
	DefaultShareTimeout = 5 * time.Second
	// DefaultMaxRetries 最大重试次数
	DefaultMaxRetries = 3
	// DefaultMinSigners 最小签名者数量（t+1）
	DefaultMinSigners = 2
)

// ========== 状态定义 ==========

// SignSessionState 签名会话状态
type SignSessionState int

const (
	// SignSessionStateInit 初始状态
	SignSessionStateInit SignSessionState = iota
	// SignSessionStateCollectingNonces 收集 nonce 承诺
	SignSessionStateCollectingNonces
	// SignSessionStateCollectingShares 收集签名份额
	SignSessionStateCollectingShares
	// SignSessionStateComplete 签名完成
	SignSessionStateComplete
	// SignSessionStateFailed 签名失败
	SignSessionStateFailed
)

func (s SignSessionState) String() string {
	switch s {
	case SignSessionStateInit:
		return "INIT"
	case SignSessionStateCollectingNonces:
		return "COLLECTING_NONCES"
	case SignSessionStateCollectingShares:
		return "COLLECTING_SHARES"
	case SignSessionStateComplete:
		return "COMPLETE"
	case SignSessionStateFailed:
		return "FAILED"
	default:
		return "UNKNOWN"
	}
}

// ========== 参与者信息 ==========

// Participant 签名参与者
type Participant struct {
	Index   uint16 // 参与者索引（1-based）
	Address string // 节点地址
	IP      string // 节点 IP
}

// ========== Nonce 承诺 ==========

// NonceCommitment nonce 承诺
type NonceCommitment struct {
	ParticipantIndex uint16
	HidingNonce      []byte // 32 bytes
	BindingNonce     []byte // 32 bytes
	ReceivedAt       time.Time
}

// ========== 签名份额 ==========

// SignatureShare 签名份额
type SignatureShare struct {
	ParticipantIndex uint16
	Share            []byte // 32 bytes
	ReceivedAt       time.Time
}

// ========== 会话配置 ==========

// SignSessionConfig 签名会话配置
type SignSessionConfig struct {
	// 超时配置
	NonceTimeout time.Duration
	ShareTimeout time.Duration
	MaxRetries   int

	// 阈值配置
	Threshold  int // t
	MinSigners int // t+1
	TotalNodes int // n

	// 聚合者配置
	AggregatorIndex uint16 // 当前聚合者索引
}

// DefaultSignSessionConfig 默认配置
func DefaultSignSessionConfig() *SignSessionConfig {
	return &SignSessionConfig{
		NonceTimeout:    DefaultNonceTimeout,
		ShareTimeout:    DefaultShareTimeout,
		MaxRetries:      DefaultMaxRetries,
		Threshold:       1,
		MinSigners:      DefaultMinSigners,
		TotalNodes:      3,
		AggregatorIndex: 1,
	}
}

// ========== 会话事件 ==========

// SignSessionEvent 会话事件类型
type SignSessionEvent int

const (
	// SignSessionEventNonceReceived 收到 nonce 承诺
	SignSessionEventNonceReceived SignSessionEvent = iota
	// SignSessionEventShareReceived 收到签名份额
	SignSessionEventShareReceived
	// SignSessionEventTimeout 超时
	SignSessionEventTimeout
	// SignSessionEventRetry 重试
	SignSessionEventRetry
	// SignSessionEventAggregatorSwitch 聚合者切换
	SignSessionEventAggregatorSwitch
)
