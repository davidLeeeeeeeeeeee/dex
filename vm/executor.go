package vm

import (
	"dex/keys"
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
	ScanFn ScanFn
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

	// 设置ScanFn
	executor.ScanFn = func(prefix string) (map[string][]byte, error) {
		return db.Scan(prefix)
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
	sv := NewStateView(x.ReadFn, x.ScanFn)
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
				// 使用 SetWithMeta 保留元数据
				if svWithMeta, ok := sv.(interface {
					SetWithMeta(string, []byte, bool, string)
				}); ok {
					svWithMeta.SetWithMeta(w.Key, w.Value, w.SyncStateDB, w.Category)
				} else {
					sv.Set(w.Key, w.Value)
				}
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

// applyResult 应用执行结果到数据库（统一提交入口）
// 这是唯一的最终化提交点，所有状态变化都在这里处理
func (x *Executor) applyResult(res *SpecResult, b *pb.Block) error {
	// ========== 第一步：检查幂等性 ==========
	// 防止同一区块被重复提交
	if committed, blockHash := x.IsBlockCommitted(b.Height); committed {
		if blockHash == b.BlockHash {
			// 已提交过相同的区块，直接返回成功（幂等）
			return nil
		}
		// 不同的区块哈希，说明有冲突
		return fmt.Errorf("block at height %d already committed with different hash: %s vs %s",
			b.Height, blockHash, b.BlockHash)
	}

	// ========== 第二步：应用所有状态变更 ==========
	// 遍历 Diff 中的所有写操作
	stateDBUpdates := make([]interface{}, 0) // 用于收集需要同步到 StateDB 的更新

	for _, w := range res.Diff {
		// 写入到 DB
		if w.Del {
			x.DB.EnqueueDel(w.Key)
		} else {
			x.DB.EnqueueSet(w.Key, string(w.Value))
		}

		// 如果需要同步到 StateDB，记录下来
		if w.SyncStateDB {
			stateDBUpdates = append(stateDBUpdates, w)
		}
	}

	// ========== 第三步：同步到 StateDB ==========
	// 统一处理所有需要同步到 StateDB 的数据
	if len(stateDBUpdates) > 0 && x.DB != nil {
		if err := x.syncToStateDB(b.Height, stateDBUpdates); err != nil {
			// StateDB 同步失败，记录错误但不中断提交
			// 因为 Badger 已经写入了，StateDB 是可选的加速层
			fmt.Printf("[VM] Warning: StateDB sync failed: %v\n", err)
		}
	}

	// ========== 第四步：写入交易处理状态 ==========
	// 记录每个交易的执行状态和错误信息
	for _, rc := range res.Receipts {
		// 交易状态
		statusKey := keys.KeyVMAppliedTx(rc.TxID)
		x.DB.EnqueueSet(statusKey, rc.Status)

		// 交易错误信息（如果有）
		if rc.Error != "" {
			errorKey := keys.KeyVMTxError(rc.TxID)
			x.DB.EnqueueSet(errorKey, rc.Error)
		}

		// 记录交易所在的高度
		heightKey := keys.KeyVMTxHeight(rc.TxID)
		x.DB.EnqueueSet(heightKey, fmt.Sprintf("%d", b.Height))
	}

	// ========== 第五步：写入区块提交标记 ==========
	// 用于幂等性检查
	commitKey := keys.KeyVMCommitHeight(b.Height)
	x.DB.EnqueueSet(commitKey, b.BlockHash)

	// 区块高度索引
	blockHeightKey := keys.KeyVMBlockHeight(b.Height)
	x.DB.EnqueueSet(blockHeightKey, b.BlockHash)

	// ========== 第六步：原子提交 ==========
	// 强制刷新到数据库，确保所有写操作原子性提交
	return x.DB.ForceFlush()
}

// syncToStateDB 同步状态变化到 StateDB
func (x *Executor) syncToStateDB(height uint64, updates []interface{}) error {
	// 检查 DB 是否支持 StateDB 同步
	if x.DB == nil {
		return fmt.Errorf("DB manager is nil")
	}

	// 尝试调用 DB 的 StateDB 同步方法
	// 这里假设 DBManager 接口有 SyncToStateDB 方法
	// 如果没有，可以通过类型断言来调用
	if syncable, ok := x.DB.(interface{ SyncToStateDB(uint64, []interface{}) error }); ok {
		return syncable.SyncToStateDB(height, updates)
	}

	// 如果 DB 不支持 StateDB 同步，返回 nil（不是错误）
	return nil
}

// IsBlockCommitted 检查区块是否已提交
func (x *Executor) IsBlockCommitted(height uint64) (bool, string) {
	key := keys.KeyVMCommitHeight(height)
	blockID, err := x.DB.Get(key)
	if err != nil || blockID == nil {
		return false, ""
	}
	return true, string(blockID)
}

// GetTransactionStatus 获取交易状态
func (x *Executor) GetTransactionStatus(txID string) (string, error) {
	key := keys.KeyVMAppliedTx(txID)
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
	key := keys.KeyVMTxError(txID)
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
