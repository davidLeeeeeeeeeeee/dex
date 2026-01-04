package vm

import (
	"dex/pb"
	"errors"
	"fmt"
	"sync"
)

// HandlerRegistry Handler注册表
type HandlerRegistry struct {
	mu sync.RWMutex
	m  map[string]TxHandler
}

// NewHandlerRegistry 创建新的注册表
func NewHandlerRegistry() *HandlerRegistry {
	return &HandlerRegistry{m: make(map[string]TxHandler)}
}

// 注册Handler
func (r *HandlerRegistry) Register(h TxHandler) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if h == nil {
		return errors.New("nil handler")
	}

	kind := h.Kind()
	if kind == "" {
		return errors.New("empty handler kind")
	}

	if _, ok := r.m[kind]; ok {
		return fmt.Errorf("duplicate handler kind: %s", kind)
	}
	r.m[kind] = h
	return nil
}

// Get 获取Handler
func (r *HandlerRegistry) Get(kind string) (TxHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.m[kind]
	return h, ok
}

// List 列出所有已注册的Handler类型
func (r *HandlerRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	kinds := make([]string, 0, len(r.m))
	for k := range r.m {
		kinds = append(kinds, k)
	}
	return kinds
}

// DefaultKindFn 默认的KindFn实现，根据pb.AnyTx的oneof结构提取交易类型
func DefaultKindFn(tx *pb.AnyTx) (string, error) {
	if tx == nil {
		return "", ErrNilTx
	}

	// 根据 pb.AnyTx 的 oneof content 字段判断交易类型
	switch tx.GetContent().(type) {
	case *pb.AnyTx_IssueTokenTx:
		return "issue_token", nil
	case *pb.AnyTx_FreezeTx:
		return "freeze", nil
	case *pb.AnyTx_Transaction:
		return "transfer", nil
	case *pb.AnyTx_OrderTx:
		return "order", nil
	case *pb.AnyTx_CandidateTx:
		return "candidate", nil
	case *pb.AnyTx_MinerTx:
		return "miner", nil
	// Witness 相关交易类型
	case *pb.AnyTx_WitnessStakeTx:
		return "witness_stake", nil
	case *pb.AnyTx_WitnessRequestTx:
		return "witness_request", nil
	case *pb.AnyTx_WitnessVoteTx:
		return "witness_vote", nil
	case *pb.AnyTx_WitnessChallengeTx:
		return "witness_challenge", nil
	case *pb.AnyTx_ArbitrationVoteTx:
		return "arbitration_vote", nil
	case *pb.AnyTx_WitnessClaimRewardTx:
		return "witness_claim_reward", nil
	// Frost 相关交易类型
	case *pb.AnyTx_FrostWithdrawRequestTx:
		return "frost_withdraw_request", nil
	case *pb.AnyTx_FrostWithdrawSignedTx:
		return "frost_withdraw_signed", nil
	default:
		return "", fmt.Errorf("unknown tx type: %v", tx.GetTxId())
	}
}
