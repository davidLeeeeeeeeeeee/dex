package vm

import "errors"

// ========== 错误定义 ==========

var (
	ErrNotImplemented  = errors.New("not implemented")
	ErrNilBlock        = errors.New("nil block")
	ErrNilTx           = errors.New("nil transaction")
	ErrInvalidSnapshot = errors.New("invalid snapshot index")
)

// ========== 基础类型定义 ==========

// “要怎么改状态”的清单
type WriteOp struct {
	Key   string
	Value []byte
	Del   bool // true表示删除操作
}

// 记录执行结果
type Receipt struct {
	TxID       string
	Status     string // "SUCCEED" or "FAILED"
	Error      string
	WriteCount int
}

// SpecResult 执行结果
type SpecResult struct {
	BlockID  string
	ParentID string
	Height   uint64
	Valid    bool
	Reason   string     // 无效时的原因
	Receipts []*Receipt // 交易执行结果
	Diff     []WriteOp  // 状态变更集合
}
