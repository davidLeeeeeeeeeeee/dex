package vm

import (
	"dex/keys"
	"dex/matching"
	"dex/pb"
	"dex/utils"
	"dex/witness"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
)

// Executor VM执行器
type Executor struct {
	mu             sync.RWMutex
	DB             DBManager
	Reg            *HandlerRegistry
	Cache          SpecExecCache
	KFn            KindFn
	ReadFn         ReadThroughFn
	ScanFn         ScanFn
	WitnessService *witness.Service // 见证者服务（纯内存计算模块）
}

// 创建新的执行器
func NewExecutor(db DBManager, reg *HandlerRegistry, cache SpecExecCache) *Executor {
	return NewExecutorWithWitness(db, reg, cache, nil)
}

// NewExecutorWithWitness 创建带见证者服务的执行器
func NewExecutorWithWitness(db DBManager, reg *HandlerRegistry, cache SpecExecCache, witnessConfig *witness.Config) *Executor {
	if reg == nil {
		reg = NewHandlerRegistry()
	}
	if cache == nil {
		cache = NewSpecExecLRU(1024)
	}

	// 创建见证者服务
	witnessSvc := witness.NewService(witnessConfig)
	_ = witnessSvc.Start()

	executor := &Executor{
		DB:             db,
		Reg:            reg,
		Cache:          cache,
		KFn:            DefaultKindFn,
		WitnessService: witnessSvc,
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

// SetWitnessService 设置见证者服务（用于测试或自定义配置）
func (x *Executor) SetWitnessService(svc *witness.Service) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.WitnessService = svc
}

// GetWitnessService 获取见证者服务
func (x *Executor) GetWitnessService() *witness.Service {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.WitnessService
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

	if x.WitnessService != nil {
		x.WitnessService.SetCurrentHeight(b.Height)
	}

	// 创建新的状态视图
	sv := NewStateView(x.ReadFn, x.ScanFn)
	receipts := make([]*Receipt, 0, len(b.Body))

	// Step 1: 预扫描区块，收集所有交易对
	pairs := collectPairsFromBlock(b)

	// Step 2 & 3: 一次性重建所有订单簿
	pairBooks, err := x.rebuildOrderBooksForPairs(pairs, sv)
	if err != nil {
		return &SpecResult{
			BlockID:  b.BlockHash,
			ParentID: b.PrevBlockHash,
			Height:   b.Height,
			Valid:    false,
			Reason:   fmt.Sprintf("failed to rebuild order books: %v", err),
		}, nil
	}

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

		// Step 4: 如果是 OrderTxHandler，注入 pairBooks
		if orderHandler, ok := h.(*OrderTxHandler); ok {
			orderHandler.SetOrderBooks(pairBooks)
		}

		// Step 5: 如果是 WitnessServiceAware handler，注入 WitnessService
		if witnessAware, ok := h.(WitnessServiceAware); ok && x.WitnessService != nil {
			witnessAware.SetWitnessService(x.WitnessService)
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
		// 填充 Receipt 元数据
		if rc != nil {
			rc.BlockHeight = b.Height
			rc.Timestamp = time.Now().Unix() // 实际应取区块时间，这里简化
		}
		receipts = append(receipts, rc)
	}

	if err := x.applyWitnessFinalizedEvents(sv, b.Height); err != nil {
		return nil, err
	}

	// ========== 区块奖励与手续费分发 ==========
	if b.Miner != "" {
		// 1. 计算区块基础奖励（系统增发）
		blockReward := CalculateBlockReward(b.Height, DefaultBlockRewardParams)

		// 2. 汇总交易手续费
		totalFees := big.NewInt(0)
		for _, rc := range receipts {
			if rc.Fee != "" {
				fee, err := ParseBalance(rc.Fee)
				if err == nil {
					totalFees, _ = SafeAdd(totalFees, fee)
				}
			}
		}

		// 3. 计算总奖励
		totalReward, _ := SafeAdd(blockReward, totalFees)

		if totalReward.Sign() > 0 {
			// 4. 按比例分配
			ratio := DefaultBlockRewardParams.WitnessRewardRatio
			totalRewardDec := decimal.NewFromBigInt(totalReward, 0)

			witnessRewardDec := totalRewardDec.Mul(ratio)
			minerRewardDec := totalRewardDec.Sub(witnessRewardDec)

			witnessReward := witnessRewardDec.BigInt()
			minerReward := minerRewardDec.BigInt()

			// 5. 分配给矿工 (70%)
			minerAccountKey := keys.KeyAccount(b.Miner)
			minerAccountData, exists, err := sv.Get(minerAccountKey)
			if err == nil && exists {
				var minerAccount pb.Account
				if err := proto.Unmarshal(minerAccountData, &minerAccount); err == nil {
					if minerAccount.Balances == nil {
						minerAccount.Balances = make(map[string]*pb.TokenBalance)
					}
					const RewardToken = "FB"
					if minerAccount.Balances[RewardToken] == nil {
						minerAccount.Balances[RewardToken] = &pb.TokenBalance{Balance: "0"}
					}
					currentBal, _ := ParseBalance(minerAccount.Balances[RewardToken].Balance)
					newBal, _ := SafeAdd(currentBal, minerReward)
					minerAccount.Balances[RewardToken].Balance = newBal.String()

					rewardedData, _ := proto.Marshal(&minerAccount)
					sv.Set(minerAccountKey, rewardedData)
				}
			}

			// 6. 分配给见证者奖励池 (30%)
			if witnessReward.Sign() > 0 {
				rewardPoolKey := keys.KeyWitnessRewardPool()
				currentPoolData, exists, _ := sv.Get(rewardPoolKey)
				currentPool := big.NewInt(0)
				if exists {
					currentPool, _ = ParseBalance(string(currentPoolData))
				}
				newPool, _ := SafeAdd(currentPool, witnessReward)
				sv.Set(rewardPoolKey, []byte(newPool.String()))
			}

			// 7. 保存奖励记录
			rewardRecord := &pb.BlockReward{
				Height:        b.Height,
				Miner:         b.Miner,
				MinerReward:   minerReward.String(),
				WitnessReward: witnessReward.String(),
				TotalFees:     totalFees.String(),
				BlockReward:   blockReward.String(),
				Timestamp:     time.Now().Unix(),
			}
			rewardData, _ := proto.Marshal(rewardRecord)
			sv.Set(keys.KeyBlockReward(b.Height), rewardData)
		}
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
	accountUpdates := make([]*WriteOp, 0)    // 用于收集账户更新，用于更新 stake index

	for i := range res.Diff {
		w := &res.Diff[i] // 使用指针，因为 WriteOp 的方法是指针接收器

		// 检测账户更新（用于更新 stake index）
		if !w.Del && (w.Category == "account" || strings.HasPrefix(w.Key, "v1_account_")) {
			accountUpdates = append(accountUpdates, w)
		}

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

	// ========== 第三步：更新 Stake Index ==========
	// 对于账户更新，需要更新 stake index
	if len(accountUpdates) > 0 {
		// 尝试将 DBManager 转换为具有 UpdateStakeIndex 方法的类型
		type StakeIndexUpdater interface {
			UpdateStakeIndex(oldStake, newStake decimal.Decimal, address string) error
		}
		if updater, ok := x.DB.(StakeIndexUpdater); ok {
			for _, w := range accountUpdates {
				// 从 key 中提取地址（格式：v1_account_<address>）
				address := extractAddressFromAccountKey(w.Key)
				if address == "" {
					continue
				}

				// 读取旧的账户数据（在应用 WriteOp 之前）
				oldAccountData, err := x.DB.Get(w.Key)
				var oldAccount pb.Account
				var oldStake decimal.Decimal
				if err == nil && oldAccountData != nil {
					if err := proto.Unmarshal(oldAccountData, &oldAccount); err == nil {
						oldStake, _ = calcStake(&oldAccount)
					}
				}

				// 从新的 WriteOp.Value 中解析新的账户数据
				var newAccount pb.Account
				if err := proto.Unmarshal(w.Value, &newAccount); err != nil {
					continue
				}
				newStake, _ := calcStake(&newAccount)

				// 如果 stake 发生变化，更新 stake index
				if !oldStake.Equal(newStake) {
					if err := updater.UpdateStakeIndex(oldStake, newStake, address); err != nil {
						// 记录错误但不中断提交
						fmt.Printf("[VM] Warning: failed to update stake index for %s: %v\n", address, err)
					}
				}
			}
		}
	}

	// ========== 第四步：同步到 StateDB ==========
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

	// ========== 第四步补充：保存完整交易数据 ==========
	// 将区块中的交易保存到数据库，以便后续查询
	// 使用类型断言检查 DB 是否支持 SaveAnyTx
	type anyTxSaver interface {
		SaveAnyTx(tx *pb.AnyTx) error
	}
	if saver, ok := x.DB.(anyTxSaver); ok {
		for _, tx := range b.Body {
			if tx == nil {
				continue
			}
			base := tx.GetBase()
			if base == nil {
				continue
			}
			// 更新交易状态为已执行
			base.ExecutedHeight = b.Height
			// 从 receipts 中查找状态
			for _, rc := range res.Receipts {
				if rc.TxID == base.TxId {
					if rc.Status == "SUCCEED" || rc.Status == "" {
						base.Status = pb.Status_SUCCEED
					} else {
						base.Status = pb.Status_FAILED
					}
					break
				}
			}
			// 保存交易
			if err := saver.SaveAnyTx(tx); err != nil {
				// 记录错误但不中断提交
				fmt.Printf("[VM] Warning: failed to save tx %s: %v\n", base.TxId, err)
			}
		}
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
	if syncable, ok := x.DB.(interface {
		SyncToStateDB(uint64, []interface{}) error
	}); ok {
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

// extractAddressFromAccountKey 从账户 key 中提取地址
// 格式：v1_account_<address> 或 account_<address>
func extractAddressFromAccountKey(key string) string {
	// 移除版本前缀
	prefixes := []string{"v1_account_", "account_"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(key, prefix) {
			return strings.TrimPrefix(key, prefix)
		}
	}
	return ""
}

// calcStake 计算账户的 stake（与 db.CalcStake 逻辑一致）
// CalcStake = FB.miner_locked_balance
func calcStake(acc *pb.Account) (decimal.Decimal, error) {
	fbBal, ok := acc.Balances["FB"]
	if !ok {
		// 说明没有任何FB余额，锁定余额也为0
		return decimal.Zero, nil
	}
	ml, err := decimal.NewFromString(fbBal.MinerLockedBalance)
	if err != nil {
		ml = decimal.Zero
	}
	return ml, nil
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

// collectPairsFromBlock 预扫描区块，收集所有需要撮合的交易对
func collectPairsFromBlock(b *pb.Block) []string {
	pairSet := make(map[string]struct{})

	for _, anyTx := range b.Body {
		// 只处理 OrderTx
		orderTx := anyTx.GetOrderTx()
		if orderTx == nil {
			continue
		}

		// 只处理 ADD 操作
		if orderTx.Op != pb.OrderOp_ADD {
			continue
		}

		// 生成交易对 key
		pair := utils.GeneratePairKey(orderTx.BaseToken, orderTx.QuoteToken)
		pairSet[pair] = struct{}{}
	}

	// 转换为 slice
	pairs := make([]string, 0, len(pairSet))
	for pair := range pairSet {
		pairs = append(pairs, pair)
	}

	return pairs
}

// rebuildOrderBooksForPairs 一次性重建所有交易对的订单簿
// 返回：map[pair]*matching.OrderBook
func (x *Executor) rebuildOrderBooksForPairs(pairs []string, sv StateView) (map[string]*matching.OrderBook, error) {
	if len(pairs) == 0 {
		return make(map[string]*matching.OrderBook), nil
	}

	// 一次性从 DB 扫描所有交易对的订单索引
	rawData, err := x.DB.ScanOrdersByPairs(pairs)
	if err != nil {
		return nil, fmt.Errorf("failed to scan orders: %w", err)
	}

	pairBooks := make(map[string]*matching.OrderBook)

	// 为每个交易对重建订单簿
	for _, pair := range pairs {
		// 创建新的订单簿（暂时不设置 sink）
		ob := matching.NewOrderBookWithSink(nil)

		// 获取该交易对的所有订单索引
		indexMap := rawData[pair]

		// 遍历索引，加载订单并添加到订单簿
		for indexKey := range indexMap {
			// 从 indexKey 中解析 orderID
			orderID := extractOrderIDFromIndexKey(indexKey)
			if orderID == "" {
				continue
			}

			// 从 StateView 读取完整订单
			orderKey := keys.KeyOrder(orderID)
			orderData, exists, err := sv.Get(orderKey)
			if err != nil || !exists {
				continue
			}

			// 反序列化订单
			var orderTx pb.OrderTx
			if err := proto.Unmarshal(orderData, &orderTx); err != nil {
				continue
			}

			// 转换为 matching.Order 并添加到订单簿
			matchOrder, err := convertToMatchingOrder(&orderTx)
			if err != nil {
				continue
			}

			// 使用 AddOrderWithoutMatch 避免重建时触发撮合
			// 这些订单已经在数据库中存在，不应该在重建时被撮合
			ob.AddOrderWithoutMatch(matchOrder)
		}

		pairBooks[pair] = ob
	}

	return pairBooks, nil
}

// extractOrderIDFromIndexKey 从价格索引 key 中提取 orderID
func extractOrderIDFromIndexKey(indexKey string) string {
	parts := strings.Split(indexKey, "|order_id:")
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}

// convertToMatchingOrder 将 pb.OrderTx 转换为 matching.Order
func convertToMatchingOrder(ord *pb.OrderTx) (*matching.Order, error) {
	if ord == nil || ord.Base == nil {
		return nil, fmt.Errorf("invalid order")
	}

	price, err := decimal.NewFromString(ord.Price)
	if err != nil {
		return nil, fmt.Errorf("invalid price: %w", err)
	}

	totalAmount, err := decimal.NewFromString(ord.Amount)
	if err != nil {
		return nil, fmt.Errorf("invalid amount: %w", err)
	}

	filledBase, err := decimal.NewFromString(ord.FilledBase)
	if err != nil {
		filledBase = decimal.Zero
	}

	// 计算剩余数量
	filledQuote, err := decimal.NewFromString(ord.FilledQuote)
	if err != nil {
		filledQuote = decimal.Zero
	}

	filledTrade := filledBase
	if ord.BaseToken > ord.QuoteToken {
		filledTrade = filledQuote
	}

	remainingAmount := totalAmount.Sub(filledTrade)
	if remainingAmount.LessThanOrEqual(decimal.Zero) {
		return nil, fmt.Errorf(
			"order already filled (txId=%s, amount=%s, filledBase=%s, filledQuote=%s, isFilled=%v)",
			ord.Base.TxId, ord.Amount, ord.FilledBase, ord.FilledQuote, ord.IsFilled,
		)
	}

	// 确定订单方向：直接使用 Side 字段
	var side matching.OrderSide
	if ord.Side == pb.OrderSide_SELL {
		side = matching.SELL
	} else {
		side = matching.BUY
	}

	return &matching.Order{
		ID:     ord.Base.TxId,
		Side:   side,
		Price:  price,
		Amount: remainingAmount,
	}, nil
}
