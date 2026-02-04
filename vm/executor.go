package vm

import (
	iface "dex/interfaces"
	"dex/keys"
	"dex/logs"
	"dex/matching"
	"dex/pb"
	"dex/utils"
	"dex/witness"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
)

// Executor VM执行器
type Executor struct {
	mu             sync.RWMutex
	DB             iface.DBManager
	Reg            *HandlerRegistry
	Cache          SpecExecCache
	KFn            KindFn
	ReadFn         ReadThroughFn
	ScanFn         ScanFn
	WitnessService *witness.Service // 见证者服务（纯内存计算模块）
}

func NewExecutor(db iface.DBManager, reg *HandlerRegistry, cache SpecExecCache) *Executor {
	return NewExecutorWithWitness(db, reg, cache, nil)
}

// NewExecutorWithWitnessService 使用已有的见证者服务创建子执行器
func NewExecutorWithWitnessService(db iface.DBManager, reg *HandlerRegistry, cache SpecExecCache, witnessSvc *witness.Service) *Executor {
	if reg == nil {
		reg = NewHandlerRegistry()
	}
	if cache == nil {
		cache = NewSpecExecLRU(64)
	}

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

// NewExecutorWithWitness 创建带见证者服务的执行器
func NewExecutorWithWitness(db iface.DBManager, reg *HandlerRegistry, cache SpecExecCache, witnessConfig *witness.Config) *Executor {
	// 创建并启动见证者服务
	witnessSvc := witness.NewService(witnessConfig)
	_ = witnessSvc.Start()

	executor := NewExecutorWithWitnessService(db, reg, cache, witnessSvc)

	// 初始化 Witness 状态
	if err := executor.LoadWitnessState(); err != nil {
		fmt.Printf("[VM] Warning: failed to load witness state: %v\n", err)
	}

	return executor
}

// LoadWitnessState 从数据库加载所有活跃的见证者、入账请求和挑战记录到 WitnessService 内存中
func (x *Executor) LoadWitnessState() error {
	if x.WitnessService == nil {
		return nil
	}

	// 1. 加载见证者信息（所有活跃见证者都需要在内存中以便进行随机选择）
	winfoMap, err := x.DB.Scan(keys.KeyWitnessInfoPrefix())
	if err == nil {
		count := 0
		for _, data := range winfoMap {
			var info pb.WitnessInfo
			if err := proto.Unmarshal(data, &info); err == nil {
				x.WitnessService.LoadWitness(&info)
				count++
			}
		}
		if count > 0 {
			fmt.Printf("[VM] Loaded %d witnesses into memory\n", count)
		}
	}

	// 2. 加载非终态的入账请求
	requestMap, err := x.DB.Scan(keys.KeyRechargeRequestPrefix())
	if err == nil {
		count := 0
		for _, data := range requestMap {
			var req pb.RechargeRequest
			if err := proto.Unmarshal(data, &req); err == nil {
				// 只有非终态（PENDING, VOTING, CONSENSUS_PASS, CHALLENGE_PERIOD, CHALLENGED, ARBITRATION, SHELVED）需要加载
				if req.Status != pb.RechargeRequestStatus_RECHARGE_FINALIZED &&
					req.Status != pb.RechargeRequestStatus_RECHARGE_REJECTED {
					x.WitnessService.LoadRequest(&req)
					count++
				}
			}
		}
		if count > 0 {
			fmt.Printf("[VM] Loaded %d active recharge requests into memory\n", count)
		}
	}

	// 3. 加载非终态的挑战记录
	challengeMap, err := x.DB.Scan(keys.KeyChallengeRecordPrefix())
	if err == nil {
		count := 0
		for _, data := range challengeMap {
			var record pb.ChallengeRecord
			if err := proto.Unmarshal(data, &record); err == nil {
				if !record.Finalized {
					x.WitnessService.LoadChallenge(&record)
					count++
				}
			}
		}
		if count > 0 {
			fmt.Printf("[VM] Loaded %d active challenges into memory\n", count)
		}
	}

	return nil
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
		x.WitnessService.SetCurrentHeight(b.Header.Height)
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
			ParentID: b.Header.PrevBlockHash,
			Height:   b.Header.Height,
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
				ParentID: b.Header.PrevBlockHash,
				Height:   b.Header.Height,
				Valid:    false,
				Reason:   fmt.Sprintf("tx %d has invalid structure: %v", idx, err),
			}, nil
		}

		// 统一注入当前执行高度到交易 BaseMessage 中，供 Handler 使用
		if base := getBaseMessage(tx); base != nil {
			base.ExecutedHeight = b.Header.Height
		}

		// 获取对应的Handler
		h, ok := x.Reg.Get(kind)
		if !ok {
			return &SpecResult{
				BlockID:  b.BlockHash,
				ParentID: b.Header.PrevBlockHash,
				Height:   b.Header.Height,
				Valid:    false,
				Reason:   fmt.Sprintf("no handler for tx %d (kind: %s)", idx, kind),
			}, nil
		}

		// Step 4: 如果是 OrderTxHandler，注入 pairBooks
		// 特别注意：因为 HandlerRegistry 中存储的是单例，在并发执行多个区块时
		// 不能直接修改单例的状态。必须克隆一个局部实例，确保本次执行使用正确的订单簿。
		if orderHandler, ok := h.(*OrderTxHandler); ok {
			localHandler := *orderHandler // 浅拷贝，因为 OrderTxHandler 结构简单，只需拷贝 map 指针字段
			localHandler.SetOrderBooks(pairBooks)
			h = &localHandler
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
			// 如果 Handler 返回了 Receipt，说明是一个可以标记为失败的“业务错误”（如余额不足）
			// 这种情况下不应挂掉整个区块，而是记录失败状态并继续
			if rc != nil {
				// 回滚状态到该交易执行前
				sv.Revert(snapshot)

				// 确保状态标识为 FAILED
				rc.Status = "FAILED"
				if rc.Error == "" {
					rc.Error = err.Error()
				}

				// 填充 Receipt 元数据并收集
				// 使用区块时间戳而非本地时间，确保多节点确定性
				rc.BlockHeight = b.Header.Height
				rc.Timestamp = b.Header.Timestamp
				receipts = append(receipts, rc)

				logs.Info("[VM] Tx %s mark as FAILED in block %d: %v", rc.TxID, b.Header.Height, err)
				continue
			}

			// 真正的协议/系统级错误（如无效交易格式），回滚并拒绝区块
			sv.Revert(snapshot)
			return &SpecResult{
				BlockID:  b.BlockHash,
				ParentID: b.Header.PrevBlockHash,
				Height:   b.Header.Height,
				Valid:    false,
				Reason:   fmt.Sprintf("tx %d protocol error: %v", idx, err),
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
		// 使用区块时间戳而非本地时间，确保多节点确定性
		if rc != nil {
			rc.BlockHeight = b.Header.Height
			rc.Timestamp = b.Header.Timestamp
		}
		receipts = append(receipts, rc)
	}

	if err := x.applyWitnessFinalizedEvents(sv, b.Header.Height); err != nil {
		return nil, err
	}

	// ========== 区块奖励与手续费分发 ==========
	if b.Header.Miner != "" {
		// 1. 计算区块基础奖励（系统增发）
		blockReward := CalculateBlockReward(b.Header.Height, DefaultBlockRewardParams)

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
			// 确定性改进：如果矿工账户不存在，自动创建新账户
			// 这确保所有节点在执行奖励时产生相同的 WriteOp
			minerAccountKey := keys.KeyAccount(b.Header.Miner)
			minerAccountData, exists, err := sv.Get(minerAccountKey)
			var minerAccount pb.Account
			if err == nil && exists {
				_ = proto.Unmarshal(minerAccountData, &minerAccount)
			} else {
				// 矿工账户不存在，创建新账户（不再包含 Balances 字段）
				minerAccount = pb.Account{
					Address: b.Header.Miner,
				}
			}
			// 保存矿工账户（不包含余额）
			rewardedAccountData, _ := proto.Marshal(&minerAccount)
			sv.Set(minerAccountKey, rewardedAccountData)

			// 使用分离存储更新矿工余额
			const RewardToken = "FB"
			minerFBBal := GetBalance(sv, b.Header.Miner, RewardToken)
			currentBal, _ := ParseBalance(minerFBBal.Balance)
			newBal, _ := SafeAdd(currentBal, minerReward)
			minerFBBal.Balance = newBal.String()
			SetBalance(sv, b.Header.Miner, RewardToken, minerFBBal)

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
			// 使用区块时间戳而非本地时间，确保多节点确定性
			rewardRecord := &pb.BlockReward{
				Height:        b.Header.Height,
				Miner:         b.Header.Miner,
				MinerReward:   minerReward.String(),
				WitnessReward: witnessReward.String(),
				TotalFees:     totalFees.String(),
				BlockReward:   blockReward.String(),
				Timestamp:     b.Header.Timestamp,
			}
			rewardData, _ := proto.Marshal(rewardRecord)
			sv.Set(keys.KeyBlockReward(b.Header.Height), rewardData)
		}
	}

	// 创建执行结果
	res := &SpecResult{
		BlockID:  b.BlockHash,
		ParentID: b.Header.PrevBlockHash,
		Height:   b.Header.Height,
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
func (x *Executor) applyResult(res *SpecResult, b *pb.Block) (err error) {
	// 开启数据库会话
	sess, err := x.DB.NewSession()
	if err != nil {
		return fmt.Errorf("failed to open db session: %v", err)
	}
	defer sess.Close()

	// ========== 第一步：检查幂等性 ==========
	// 防止同一区块被重复提交
	if committed, blockHash := x.IsBlockCommitted(b.Header.Height); committed {
		if blockHash == b.BlockHash {
			return nil
		}
		return fmt.Errorf("block at height %d already committed with different hash: %s vs %s",
			b.Header.Height, blockHash, b.BlockHash)
	}

	// ========== 第二步：应用所有状态变更 ==========
	// 遍历 Diff 中的所有写操作
	stateDBUpdates := make([]*WriteOp, 0) // 用于收集需要同步到 StateDB 的更新
	accountUpdates := make([]*WriteOp, 0) // 用于收集账户更新，用于更新 stake index

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

				// 使用分离存储的余额计算 stake
				// 注意：这里需要从 StateDB 或 WriteOps 中读取 FB 余额
				// 由于余额已分离存储，我们直接从 stateDBUpdates 中查找对应的余额更新

				// 计算旧 stake（从 StateDB session 读取）
				oldStake := decimal.Zero
				if fbBalData, err := sess.Get(keys.KeyBalance(address, "FB")); err == nil && fbBalData != nil {
					var balRecord pb.TokenBalanceRecord
					if err := proto.Unmarshal(fbBalData, &balRecord); err == nil && balRecord.Balance != nil {
						oldStake, _ = decimal.NewFromString(balRecord.Balance.MinerLockedBalance)
					}
				}

				// 计算新 stake（从当前 WriteOps 中查找对应的余额更新）
				newStake := oldStake // 默认不变
				balanceKey := keys.KeyBalance(address, "FB")
				for _, wop := range stateDBUpdates {
					if wop.Key == balanceKey && !wop.Del {
						var balRecord pb.TokenBalanceRecord
						if err := proto.Unmarshal(wop.Value, &balRecord); err == nil && balRecord.Balance != nil {
							newStake, _ = decimal.NewFromString(balRecord.Balance.MinerLockedBalance)
						}
						break
					}
				}

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
	// 即使没有更新，也调用 ApplyStateUpdate 以确认当前高度的状态根
	// 转换为 []interface{} 以满足接口要求
	stateDBUpdatesIface := make([]interface{}, len(stateDBUpdates))
	for i, w := range stateDBUpdates {
		stateDBUpdatesIface[i] = w
	}
	stateRoot, err := sess.ApplyStateUpdate(b.Header.Height, stateDBUpdatesIface)
	if err != nil {
		fmt.Printf("[VM] Warning: StateDB sync failed via session: %v\n", err)
	} else if stateRoot != nil {
		b.Header.StateRoot = stateRoot
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
		x.DB.EnqueueSet(heightKey, fmt.Sprintf("%d", b.Header.Height))
	}

	// ========== 第四步补充：更新区块交易状态并保存原文 ==========
	// 创建 TxID 到 Receipt 的映射，加速查找
	receiptMap := make(map[string]*Receipt, len(res.Receipts))
	for _, rc := range res.Receipts {
		receiptMap[rc.TxID] = rc
	}

	// 1. 先更新区块中所有交易的状态和执行高度
	// 修改对象引用，会反映到传入的 pb.Block 中
	for _, tx := range b.Body {
		if tx == nil {
			continue
		}
		base := tx.GetBase()
		if base == nil {
			continue
		}

		// 统一注入执行高度
		base.ExecutedHeight = b.Header.Height

		// 根据执行收据更新状态
		if rc, ok := receiptMap[base.TxId]; ok {
			if rc.Status == "SUCCEED" || rc.Status == "" {
				base.Status = pb.Status_SUCCEED
			} else {
				base.Status = pb.Status_FAILED
			}
		}
	}

	// 2. 将更新后的交易原文保存到数据库（不可变），以便后续查询
	// 订单簿状态（订单状态、价格索引等）全部由 Diff 中的 WriteOp 控制
	// 注意：OrderTx 在"交易查询"里是不可变原文；在"订单簿"里是可变状态，两者彻底分离
	type txRawSaver interface {
		SaveTxRaw(tx *pb.AnyTx) error
	}
	if saver, ok := x.DB.(txRawSaver); ok {
		for _, tx := range b.Body {
			if tx == nil {
				continue
			}
			// 保存交易原文（已更新 Status 和 ExecutedHeight）
			if err := saver.SaveTxRaw(tx); err != nil {
				// 记录错误但不中断提交
				fmt.Printf("[VM] Warning: failed to save tx raw %s: %v\n", tx.GetTxId(), err)
			}
		}
	}

	// ========== 第五步：写入区块提交标记 ==========
	// 用于幂等性检查
	commitKey := keys.KeyVMCommitHeight(b.Header.Height)
	x.DB.EnqueueSet(commitKey, b.BlockHash)

	// 区块高度索引
	blockHeightKey := keys.KeyVMBlockHeight(b.Header.Height)
	x.DB.EnqueueSet(blockHeightKey, b.BlockHash)

	// ========== 第六步：提交会话并同步 ==========
	if err := sess.Commit(); err != nil {
		return fmt.Errorf("failed to commit db session: %v", err)
	}

	// 更新内存中的状态根（确保后续执行能看到最新版本）
	if b.Header.StateRoot != nil {
		x.DB.CommitRoot(b.Header.Height, b.Header.StateRoot)
	}

	// 强制刷新到数据库（用于非状态数据的 EnqueueSet）
	return x.DB.ForceFlush()
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

// calcStake 计算账户的 stake（使用分离存储）
// CalcStake = FB.miner_locked_balance
func calcStake(sv StateView, addr string) (decimal.Decimal, error) {
	fbBal := GetBalance(sv, addr, "FB")
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
	if b.Header.Height == 0 && b.Header.PrevBlockHash != "" {
		return fmt.Errorf("genesis block should not have parent")
	}
	if b.Header.Height > 0 && b.Header.PrevBlockHash == "" {
		return fmt.Errorf("non-genesis block should have parent")
	}
	return nil
}

// collectPairsFromBlock 预扫描区块，收集所有需要撮合的交易对
// 确定性改进：返回排序后的交易对列表，确保多节点处理顺序一致
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

	// 转换为 slice 并排序，确保确定性遍历顺序
	pairs := make([]string, 0, len(pairSet))
	for pair := range pairSet {
		pairs = append(pairs, pair)
	}
	sort.Strings(pairs)

	return pairs
}

//	一次性重建所有交易对的订单簿
//
// 返回：map[pair]*matching.OrderBook
func (x *Executor) rebuildOrderBooksForPairs(pairs []string, sv StateView) (map[string]*matching.OrderBook, error) {
	if len(pairs) == 0 {
		return make(map[string]*matching.OrderBook), nil
	}

	pairBooks := make(map[string]*matching.OrderBook)

	for _, pair := range pairs {
		ob := matching.NewOrderBookWithSink(nil)

		// 2. 分别加载买盘和卖盘的未成交订单 (is_filled:false)
		// 加载买盘 Top 500
		buyPrefix := keys.KeyOrderPriceIndexPrefix(pair, pb.OrderSide_BUY, false)
		buyOrders, err := x.DB.ScanKVWithLimitReverse(buyPrefix, 500)
		if err == nil {
			// 对 keys 排序以确保确定性遍历顺序
			buyKeys := make([]string, 0, len(buyOrders))
			for k := range buyOrders {
				buyKeys = append(buyKeys, k)
			}
			sort.Strings(buyKeys)
			for _, indexKey := range buyKeys {
				data := buyOrders[indexKey]
				orderID := extractOrderIDFromIndexKey(indexKey)
				if orderID == "" {
					continue
				}
				x.loadOrderToBook(orderID, data, ob, sv)
			}
		}

		// 加载卖盘 Top 500
		sellPrefix := keys.KeyOrderPriceIndexPrefix(pair, pb.OrderSide_SELL, false)
		sellOrders, err := x.DB.ScanKVWithLimit(sellPrefix, 500)
		if err == nil {
			// 对 keys 排序以确保确定性遍历顺序
			sellKeys := make([]string, 0, len(sellOrders))
			for k := range sellOrders {
				sellKeys = append(sellKeys, k)
			}
			sort.Strings(sellKeys)
			for _, indexKey := range sellKeys {
				data := sellOrders[indexKey]
				orderID := extractOrderIDFromIndexKey(indexKey)
				if orderID == "" {
					continue
				}
				x.loadOrderToBook(orderID, data, ob, sv)
			}
		}

		pairBooks[pair] = ob
	}

	return pairBooks, nil
}

// 辅助方法：从数据加载订单到订单簿
func (x *Executor) loadOrderToBook(orderID string, indexData []byte, ob *matching.OrderBook, sv StateView) {
	// 尝试从 OrderState 读取（最新状态）
	orderStateKey := keys.KeyOrderState(orderID)
	orderStateData, err := x.DB.GetKV(orderStateKey)
	if err == nil && len(orderStateData) > 0 {
		var orderState pb.OrderState
		if err := proto.Unmarshal(orderStateData, &orderState); err == nil {
			matchOrder, _ := convertOrderStateToMatchingOrder(&orderState)
			if matchOrder != nil {
				ob.AddOrderWithoutMatch(matchOrder)
				return
			}
		}
	}

	// 兼容逻辑：从 indexData 或 sv 读取
	var orderTx pb.OrderTx
	if err := proto.Unmarshal(indexData, &orderTx); err == nil {
		matchOrder, _ := convertToMatchingOrderLegacy(&orderTx)
		if matchOrder != nil {
			ob.AddOrderWithoutMatch(matchOrder)
		}
	}
}

// extractOrderIDFromIndexKey 从价格索引 key 中提取 orderID
func extractOrderIDFromIndexKey(indexKey string) string {
	parts := strings.Split(indexKey, "|order_id:")
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}

// convertOrderStateToMatchingOrder 将 pb.OrderState 转换为 matching.Order
func convertOrderStateToMatchingOrder(state *pb.OrderState) (*matching.Order, error) {
	if state == nil {
		return nil, fmt.Errorf("invalid order state")
	}

	price, err := decimal.NewFromString(state.Price)
	if err != nil {
		return nil, fmt.Errorf("invalid price: %w", err)
	}

	totalAmount, err := decimal.NewFromString(state.Amount)
	if err != nil {
		return nil, fmt.Errorf("invalid amount: %w", err)
	}

	filledBase, err := decimal.NewFromString(state.FilledBase)
	if err != nil {
		filledBase = decimal.Zero
	}

	filledQuote, err := decimal.NewFromString(state.FilledQuote)
	if err != nil {
		filledQuote = decimal.Zero
	}

	// 计算剩余数量
	filledTrade := filledBase
	if state.BaseToken > state.QuoteToken {
		filledTrade = filledQuote
	}

	remainingAmount := totalAmount.Sub(filledTrade)
	if remainingAmount.LessThanOrEqual(decimal.Zero) {
		return nil, fmt.Errorf(
			"order already filled (orderId=%s, amount=%s, filledBase=%s, filledQuote=%s, isFilled=%v)",
			state.OrderId, state.Amount, state.FilledBase, state.FilledQuote, state.IsFilled,
		)
	}

	var side matching.OrderSide
	if state.Side == pb.OrderSide_SELL {
		side = matching.SELL
	} else {
		side = matching.BUY
	}

	return &matching.Order{
		ID:     state.OrderId,
		Side:   side,
		Price:  price,
		Amount: remainingAmount,
	}, nil
}

// convertToMatchingOrderLegacy 将旧版 pb.OrderTx 转换为 matching.Order（兼容性）
// 旧版 OrderTx 没有 FilledBase/FilledQuote 字段，假设订单未成交
func convertToMatchingOrderLegacy(ord *pb.OrderTx) (*matching.Order, error) {
	if ord == nil || ord.Base == nil {
		return nil, fmt.Errorf("invalid order")
	}

	price, err := decimal.NewFromString(ord.Price)
	if err != nil {
		return nil, fmt.Errorf("invalid price: %w", err)
	}

	amount, err := decimal.NewFromString(ord.Amount)
	if err != nil {
		return nil, fmt.Errorf("invalid amount: %w", err)
	}

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
		Amount: amount,
	}, nil
}

// getBaseMessage 辅助函数：从 AnyTx 中提取 BaseMessage
func getBaseMessage(tx *pb.AnyTx) *pb.BaseMessage {
	if tx == nil {
		return nil
	}
	switch v := tx.Content.(type) {
	case *pb.AnyTx_Transaction:
		return v.Transaction.Base
	case *pb.AnyTx_OrderTx:
		return v.OrderTx.Base
	case *pb.AnyTx_MinerTx:
		return v.MinerTx.Base
	case *pb.AnyTx_IssueTokenTx:
		return v.IssueTokenTx.Base
	case *pb.AnyTx_FreezeTx:
		return v.FreezeTx.Base
	case *pb.AnyTx_WitnessStakeTx:
		return v.WitnessStakeTx.Base
	case *pb.AnyTx_WitnessRequestTx:
		return v.WitnessRequestTx.Base
	case *pb.AnyTx_WitnessVoteTx:
		return v.WitnessVoteTx.Base
	case *pb.AnyTx_WitnessChallengeTx:
		return v.WitnessChallengeTx.Base
	case *pb.AnyTx_ArbitrationVoteTx:
		return v.ArbitrationVoteTx.Base
	case *pb.AnyTx_WitnessClaimRewardTx:
		return v.WitnessClaimRewardTx.Base
	case *pb.AnyTx_FrostWithdrawRequestTx:
		return v.FrostWithdrawRequestTx.Base
	case *pb.AnyTx_FrostWithdrawSignedTx:
		return v.FrostWithdrawSignedTx.Base
	case *pb.AnyTx_FrostVaultDkgCommitTx:
		return v.FrostVaultDkgCommitTx.Base
	case *pb.AnyTx_FrostVaultDkgShareTx:
		return v.FrostVaultDkgShareTx.Base
	case *pb.AnyTx_FrostVaultDkgComplaintTx:
		return v.FrostVaultDkgComplaintTx.Base
	case *pb.AnyTx_FrostVaultDkgRevealTx:
		return v.FrostVaultDkgRevealTx.Base
	case *pb.AnyTx_FrostVaultDkgValidationSignedTx:
		return v.FrostVaultDkgValidationSignedTx.Base
	case *pb.AnyTx_FrostVaultTransitionSignedTx:
		return v.FrostVaultTransitionSignedTx.Base
	}
	return nil
}
