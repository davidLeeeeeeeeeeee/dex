package vm

import (
	"dex/pb"
	"fmt"
	"sync"
)

// Executor VM执行器
type Executor struct {
	mu     sync.RWMutex
	DB     DBManager
	Reg    *HandlerRegistry
	Cache  SpecExecCache
	KFn    KindFn
	ReadFn ReadThroughFn
}

// 创建新的执行器
func NewExecutor(db DBManager, reg *HandlerRegistry, cache SpecExecCache) *Executor {
	if reg == nil {
		reg = NewHandlerRegistry()
	}
	if cache == nil {
		cache = NewSpecExecLRU(1024)
	}

	executor := &Executor{
		DB:    db,
		Reg:   reg,
		Cache: cache,
		KFn:   DefaultKindFn,
	}

	// 设置ReadFn
	executor.ReadFn = func(key string) ([]byte, error) {
		return db.Get(key)
	}

	return executor
}

// SetKindFn 设置Kind提取函数
func (x *Executor) SetKindFn(fn KindFn) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.KFn = fn
}

// PreExecuteBlock 预执行区块（不写数据库）
func (x *Executor) PreExecuteBlock(b *pb.Block) (*SpecResult, error) {
	if b == nil {
		return nil, ErrNilBlock
	}

	// 检查缓存
	if cached, ok := x.Cache.Get(b.BlockHash); ok {
		return cached, nil
	}

	// 创建新的状态视图
	sv := NewStateView(x.ReadFn)
	receipts := make([]*Receipt, 0, len(b.Body))

	// 遍历执行每个交易
	for idx, tx := range b.Body {
		// 提取交易类型
		kind, err := x.KFn(tx)
		if err != nil {
			return &SpecResult{
				BlockID:  b.BlockHash,
				ParentID: b.PrevBlockHash,
				Height:   b.Height,
				Valid:    false,
				Reason:   fmt.Sprintf("tx %d: %v", idx, err),
			}, nil
		}

		// 获取对应的Handler
		h, ok := x.Reg.Get(kind)
		if !ok {
			return &SpecResult{
				BlockID:  b.BlockHash,
				ParentID: b.PrevBlockHash,
				Height:   b.Height,
				Valid:    false,
				Reason:   fmt.Sprintf("no handler for tx %d (kind: %s)", idx, kind),
			}, nil
		}

		// 创建快照点，用于失败时回滚
		snapshot := sv.Snapshot()

		// 执行交易
		ws, rc, err := h.DryRun(tx, sv)
		if err != nil {
			// 回滚状态
			sv.Revert(snapshot)
			return &SpecResult{
				BlockID:  b.BlockHash,
				ParentID: b.PrevBlockHash,
				Height:   b.Height,
				Valid:    false,
				Reason:   fmt.Sprintf("tx %d invalid: %v", idx, err),
			}, nil
		}

		// 将ws应用到overlay（如果DryRun没有直接写sv）
		for _, w := range ws {
			if w.Del {
				sv.Del(w.Key)
			} else {
				sv.Set(w.Key, w.Value)
			}
		}
		receipts = append(receipts, rc)
	}

	// 创建执行结果
	res := &SpecResult{
		BlockID:  b.BlockHash,
		ParentID: b.PrevBlockHash,
		Height:   b.Height,
		Valid:    true,
		Receipts: receipts,
		Diff:     sv.Diff(),
	}

	// 缓存结果
	x.Cache.Put(res)
	return res, nil
}

// CommitFinalizedBlock 最终化提交（写入数据库）
func (x *Executor) CommitFinalizedBlock(b *pb.Block) error {
	if b == nil {
		return ErrNilBlock
	}

	x.mu.Lock()
	defer x.mu.Unlock()

	// 优先使用缓存的执行结果
	if res, ok := x.Cache.Get(b.BlockHash); ok && res.Valid {
		return x.applyResult(res, b)
	}

	// 缓存缺失：重新执行
	res, err := x.PreExecuteBlock(b)
	if err != nil {
		return fmt.Errorf("re-execute block failed: %v", err)
	}

	if !res.Valid {
		return fmt.Errorf("block invalid: %s", res.Reason)
	}

	return x.applyResult(res, b)
}

// applyResult 应用执行结果到数据库
func (x *Executor) applyResult(res *SpecResult, b *pb.Block) error {
	// 批量写入状态变更
	for _, w := range res.Diff {
		if w.Del {
			x.DB.EnqueueDel(w.Key)
		} else {
			x.DB.EnqueueSet(w.Key, string(w.Value))
		}
	}

	// 写入幂等标记：高度提交
	heightKey := fmt.Sprintf("vm_commit_h_%d", b.Height)
	x.DB.EnqueueSet(heightKey, b.BlockHash)

	// 写入交易处理状态
	for _, rc := range res.Receipts {
		txKey := fmt.Sprintf("vm_applied_tx_%s", rc.TxID)
		x.DB.EnqueueSet(txKey, rc.Status)

		// 可选：写入完整的Receipt
		if rc.Error != "" {
			errKey := fmt.Sprintf("vm_tx_error_%s", rc.TxID)
			x.DB.EnqueueSet(errKey, rc.Error)
		}
	}

	// 强制刷新到数据库
	return x.DB.ForceFlush()
}

// IsBlockCommitted 检查区块是否已提交
func (x *Executor) IsBlockCommitted(height uint64) (bool, string) {
	key := fmt.Sprintf("vm_commit_h_%d", height)
	blockID, err := x.DB.Get(key)
	if err != nil || blockID == nil {
		return false, ""
	}
	return true, string(blockID)
}

// GetTransactionStatus 获取交易状态
func (x *Executor) GetTransactionStatus(txID string) (string, error) {
	key := fmt.Sprintf("vm_applied_tx_%s", txID)
	status, err := x.DB.Get(key)
	if err != nil {
		return "", err
	}
	if status == nil {
		return "PENDING", nil
	}
	return string(status), nil
}

// GetTransactionError 获取交易错误信息
func (x *Executor) GetTransactionError(txID string) (string, error) {
	key := fmt.Sprintf("vm_tx_error_%s", txID)
	errMsg, err := x.DB.Get(key)
	if err != nil {
		return "", err
	}
	if errMsg == nil {
		return "", nil
	}
	return string(errMsg), nil
}

// CleanupCache 清理缓存
func (x *Executor) CleanupCache(finalizedHeight uint64) {
	if finalizedHeight > 100 {
		// 保留最近100个高度的缓存
		x.Cache.EvictBelow(finalizedHeight - 100)
	}
}

// ValidateBlock 验证区块基本信息
func ValidateBlock(b *pb.Block) error {
	if b == nil {
		return ErrNilBlock
	}
	if b.BlockHash == "" {
		return fmt.Errorf("empty block hash")
	}
	if b.Height == 0 && b.PrevBlockHash != "" {
		return fmt.Errorf("genesis block should not have parent")
	}
	if b.Height > 0 && b.PrevBlockHash == "" {
		return fmt.Errorf("non-genesis block should have parent")
	}
	return nil
}
