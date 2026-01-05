// frost/runtime/session/types.go
// ROAST 签名会话类型定义

package session

// SignSessionState 签名会话状态
type SignSessionState int

const (
	// SignSessionStateInit 初始状态
	SignSessionStateInit SignSessionState = iota
	// SignSessionStateCollectingNonces 收集 nonce 承诺
	SignSessionStateCollectingNonces
	// SignSessionStateCollectingShares 收集签名份额
	SignSessionStateCollectingShares
	// SignSessionStateAggregating 聚合签名
	SignSessionStateAggregating
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
	case SignSessionStateAggregating:
		return "AGGREGATING"
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
	ID      string // 节点 ID（用于会话标识与路由）
	Index   int    // 参与者索引（0-based，等同于 committee 下标）
	Address string // 节点地址（可选）
	IP      string // 节点 IP（可选）
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
