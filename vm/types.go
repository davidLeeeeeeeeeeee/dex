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
	Key      string // 完整的 key（包括命名空间前缀）
	Value    []byte // 序列化后的值
	Del      bool   // true表示删除操作
	Category string // 数据分类：account, token, order, receipt, meta 等，便于追踪和调试
}

// ========== WriteOp 方法 ==========

// GetKey 获取 key
func (w *WriteOp) GetKey() string {
	return w.Key
}

// GetValue 获取 value
func (w *WriteOp) GetValue() []byte {
	return w.Value
}

// IsDel 是否删除操作
func (w *WriteOp) IsDel() bool {
	return w.Del
}

// 记录执行结果
type Receipt struct {
	TxID           string
	Status         string // "SUCCEED" or "FAILED"
	Error          string
	BlockHeight    uint64
	Timestamp      int64
	GasUsed        uint64
	Fee            string
	Logs           []string
	WriteCount     int
	FBBalanceAfter string // 执行后发送方的 FB 余额快照 (JSON)
}

// SpecResult 执行结果
type SpecResult struct {
	BlockID  string
	ParentID string
	Height   uint64
	// BaseCommittedHeight/BaseCommittedHash capture finalized base state observed during pre-exec.
	BaseCommittedHeight uint64
	BaseCommittedHash   string
	Valid               bool
	Reason              string     // 无效时的原因
	Receipts            []*Receipt // 交易执行结果
	Diff                []WriteOp  // 状态变更集合
}
