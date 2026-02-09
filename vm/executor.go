package vm

import (
	iface "dex/interfaces"
	"dex/keys"
	"dex/logs"
	"dex/matching"
	"dex/pb"
	"dex/utils"
	"dex/witness"
	"encoding/json"
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
			if err := unmarshalProtoCompat(data, &info); err == nil {
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
			if err := unmarshalProtoCompat(data, &req); err == nil {
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
			if err := unmarshalProtoCompat(data, &record); err == nil {
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
	return x.preExecuteBlock(b, true)
}

func (x *Executor) preExecuteBlock(b *pb.Block, useCache bool) (*SpecResult, error) {
	if b == nil {
		return nil, ErrNilBlock
	}

	// 检查缓存
	if useCache {
		if cached, ok := x.Cache.Get(b.BlockHash); ok {
			return cached, nil
		}
	}

	if x.WitnessService != nil {
		x.WitnessService.SetCurrentHeight(b.Header.Height)
	}

	// 创建新的状态视图
	sv := NewStateView(x.ReadFn, x.ScanFn)
	receipts := make([]*Receipt, 0, len(b.Body))
	seenTxIDs := make(map[string]struct{}, len(b.Body))
	appliedCache := make(map[string]bool, len(b.Body))
	skippedDupInBlock := 0
	skippedApplied := 0

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
		if tx == nil {
			continue
		}

		txID := tx.GetTxId()
		if txID != "" {
			if _, exists := seenTxIDs[txID]; exists {
				skippedDupInBlock++
				continue
			}
			seenTxIDs[txID] = struct{}{}

			applied, ok := appliedCache[txID]
			if !ok {
				applied = x.isTxApplied(txID)
				appliedCache[txID] = applied
			}
			if applied {
				skippedApplied++
				continue
			}
		}
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
				// FAILED 交易也记录回滚后的余额快照
				if base := getBaseMessage(tx); base != nil && base.FromAddress != "" {
					fbBal := GetBalance(sv, base.FromAddress, "FB")
					if fbBal != nil {
						rc.FBBalanceAfter = balanceToJSON(fbBal)
					}
				}
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

			// 捕获交易执行后的 FB 余额快照（从 StateView 中读取，反映当前 tx 执行后的真实状态）
			if base := getBaseMessage(tx); base != nil && base.FromAddress != "" {
				fbBal := GetBalance(sv, base.FromAddress, "FB")
				if fbBal != nil {
					rc.FBBalanceAfter = balanceToJSON(fbBal)
				}
			}
		}
		receipts = append(receipts, rc)
	}
	if skippedDupInBlock > 0 || skippedApplied > 0 {
		logs.Debug(
			"[VM] skipped txs in block: height=%d hash=%s dup=%d applied=%d body=%d",
			b.Header.Height, b.BlockHash, skippedDupInBlock, skippedApplied, len(b.Body),
		)
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
				_ = unmarshalProtoCompat(minerAccountData, &minerAccount)
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
	if committed, blockHash := x.IsBlockCommitted(b.Header.Height); committed {
		if blockHash == b.BlockHash {
			return nil
		}
		return fmt.Errorf("block at height %d already committed with different hash: %s vs %s",
			b.Header.Height, blockHash, b.BlockHash)
	}

	// 缓存缺失：重新执行
	res, err := x.preExecuteBlock(b, false)
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
		// 濡偓濞村澶勯幋閿嬫纯閺傚府绱欓悽銊ょ艾閺囧瓨???stake index???
		if !w.Del && (w.Category == "account" || strings.HasPrefix(w.Key, "v1_account_")) {
			accountUpdates = append(accountUpdates, w)
		}

		// 閸愭瑥鍙嗛崚?DB
		if w.Del {
			x.DB.EnqueueDel(w.Key)
		} else {
			x.DB.EnqueueSet(w.Key, string(w.Value))
		}

		// 婵″倹鐏夐棁鈧憰浣告倱濮濄儱???StateDB閿涘矁顔囪ぐ鏇氱瑓???
		if w.SyncStateDB {
			stateDBUpdates = append(stateDBUpdates, w)
		}
	}

	// ========== 缁楊兛绗佸銉窗閺囧瓨???Stake Index ==========
	// ========== 第三步：更新 Stake Index ==========
	if len(accountUpdates) > 0 {
		// 鐏忔繆鐦亸?DBManager 鏉烆剚宕叉稉鍝勫徔???UpdateStakeIndex 閺傝纭堕惃鍕???
		type StakeIndexUpdater interface {
			UpdateStakeIndex(oldStake, newStake decimal.Decimal, address string) error
		}
		if updater, ok := x.DB.(StakeIndexUpdater); ok {
			for _, w := range accountUpdates {
				// ???key 娑擃厽褰侀崣鏍ф勾閸р偓閿涘牊鐗稿蹇ョ窗v1_account_<address>???
				address := extractAddressFromAccountKey(w.Key)
				if address == "" {
					continue
				}

				// 娴ｈ法鏁ら崚鍡欘瀲鐎涙ê鍋嶉惃鍕稇妫版繆顓哥粻?stake
				// 使用分离存储的余额计算 stake
				// 注意：这里需要从 StateDB 或 WriteOps 中读取 FB 余额

				// 鐠侊紕鐣婚弮?stake閿涘牅???StateDB session 鐠囪褰囬敍?
				oldStake := decimal.Zero
				if fbBalData, err := sess.Get(keys.KeyBalance(address, "FB")); err == nil && fbBalData != nil {
					var balRecord pb.TokenBalanceRecord
					if err := unmarshalProtoCompat(fbBalData, &balRecord); err == nil && balRecord.Balance != nil {
						oldStake, _ = decimal.NewFromString(balRecord.Balance.MinerLockedBalance)
					}
				}

				// 鐠侊紕鐣婚弬?stake閿涘牅绮犺ぐ鎾冲 WriteOps 娑擃厽鐓￠幍鎯ь嚠鎼存梻娈戞担娆擃杺閺囧瓨鏌婇敍?
				newStake := oldStake				// 计算新 stake（从当前 WriteOps 中查找对应的余额更新）
				balanceKey := keys.KeyBalance(address, "FB")
				for _, wop := range stateDBUpdates {
					if wop.Key == balanceKey && !wop.Del {
						var balRecord pb.TokenBalanceRecord
						if err := unmarshalProtoCompat(wop.Value, &balRecord); err == nil && balRecord.Balance != nil {
							newStake, _ = decimal.NewFromString(balRecord.Balance.MinerLockedBalance)
						}
						break
					}
				}

				// 婵″倹???stake 閸欐垹鏁撻崣妯哄閿涘本娲块弬?stake index
				if !oldStake.Equal(newStake) {
					if err := updater.UpdateStakeIndex(oldStake, newStake, address); err != nil {
						// 鐠佹澘缍嶉柨娆掝嚖娴ｅ棔绗夋稉顓熸焽閹绘劒???
						fmt.Printf("[VM] Warning: failed to update stake index for %s: %v\n", address, err)
					}
				}
			}
		}
	}

	// ========== 缁楊剙娲撳銉窗閸氬本顒為崚?StateDB ==========
	// ========== 第四步：同步到 StateDB ==========
	// 统一处理所有需要同步到 StateDB 的数据
	// 即使没有更新，也调用 ApplyStateUpdate 以确认当前高度的状态根
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

	// ========== 缁楊剙娲撳銉窗閸愭瑥鍙嗘禍銈嗘婢跺嫮鎮婇悩鑸碘偓?==========
	// ========== 第四步：写入交易处理状态 ==========
	for _, rc := range res.Receipts {
		// 娴溿倖妲楅悩鑸碘偓?
		statusKey := keys.KeyVMAppliedTx(rc.TxID)
		x.DB.EnqueueSet(statusKey, rc.Status)

		// 娴溿倖妲楅柨娆掝嚖娣団剝浼呴敍鍫濐洤閺嬫粍婀侀敍?
		if rc.Error != "" {
			errorKey := keys.KeyVMTxError(rc.TxID)
			x.DB.EnqueueSet(errorKey, rc.Error)
		}

		// 鐠佹澘缍嶆禍銈嗘閹碘偓閸︺劎娈戞妯哄
		heightKey := keys.KeyVMTxHeight(rc.TxID)
		x.DB.EnqueueSet(heightKey, fmt.Sprintf("%d", b.Header.Height))

		// 娣囨繂鐡ㄦ禍銈嗘???FB 娴ｆ瑩顤傝箛顐ゅ弾
		if rc.FBBalanceAfter != "" {
			fbKey := keys.KeyVMTxFBBalance(rc.TxID)
			x.DB.EnqueueSet(fbKey, rc.FBBalanceAfter)
		}
	}

	// ========== 缁楊剙娲撳銉ㄋ夐崗鍜冪窗閺囧瓨鏌婇崠鍝勬健娴溿倖妲楅悩鑸碘偓浣歌嫙娣囨繂鐡ㄩ崢鐔告瀮 ==========
	// ========== 第四步补充：更新区块交易状态并保存原文 ==========
	receiptMap := make(map[string]*Receipt, len(res.Receipts))
	for _, rc := range res.Receipts {
		receiptMap[rc.TxID] = rc
	}
	processedStatusTxs := make(map[string]struct{}, len(receiptMap))

	// 1. 閸忓牊娲块弬鏉垮隘閸фぞ鑵戦幍鈧張澶夋唉閺勬挾娈戦悩鑸碘偓浣告嫲閹笛嗩攽妤傛ê???
	// 1. 先更新区块中所有交易的状态和执行高度
	for _, tx := range b.Body {
		if tx == nil {
			continue
		}
		base := tx.GetBase()
		if base == nil || base.TxId == "" {
			continue
		}
		if _, done := processedStatusTxs[base.TxId]; done {
			continue
		}

		// 缂佺喍绔村▔銊ュ弳閹笛嗩攽妤傛ê???

		// 閺嶈宓侀幍褑顢戦弨鑸靛祦閺囧瓨鏌婇悩鑸碘偓?
		rc, ok := receiptMap[base.TxId]
		if !ok {
			continue
		}
		processedStatusTxs[base.TxId] = struct{}{}
		base.ExecutedHeight = b.Header.Height
		if rc.Status == "SUCCEED" || rc.Status == "" {
			base.Status = pb.Status_SUCCEED
		} else {
			base.Status = pb.Status_FAILED
		}
	}

	// 2. 鐏忓棙娲块弬鏉挎倵閻ㄥ嫪姘﹂弰鎾冲斧閺傚洣绻氱€涙ê鍩岄弫鐗堝祦鎼存搫绱欐稉宥呭讲閸欐﹫绱氶敍灞间簰娓氬灝鎮楃紒顓熺叀???
	// 2. 将更新后的交易原文保存到数据库（不可变），以便后续查询
	// 订单簿状态（订单状态、价格索引等）全部由 Diff 中的 WriteOp 控制
	type txRawSaver interface {
		SaveTxRaw(tx *pb.AnyTx) error
	}
	if saver, ok := x.DB.(txRawSaver); ok {
		savedRawTxs := make(map[string]struct{}, len(receiptMap))
		for _, tx := range b.Body {
			if tx == nil {
				continue
			}
			txID := tx.GetTxId()
			if txID == "" {
				continue
			}
			if _, done := savedRawTxs[txID]; done {
				continue
			}
			if _, executedThisBlock := receiptMap[txID]; !executedThisBlock {
				continue
			}
			// 娣囨繂鐡ㄦ禍銈嗘閸樼喐鏋冮敍鍫濆嚒閺囧瓨???Status ???ExecutedHeight???
			if err := saver.SaveTxRaw(tx); err != nil {
				// 鐠佹澘缍嶉柨娆掝嚖娴ｅ棔绗夋稉顓熸焽閹绘劒???
				fmt.Printf("[VM] Warning: failed to save tx raw %s: %v\n", txID, err)
			}
			savedRawTxs[txID] = struct{}{}
		}
	}

	// ========== 缁楊兛绨插銉窗閸愭瑥鍙嗛崠鍝勬健閹绘劒姘﹂弽鍥唶 ==========
	// ========== 第五步：写入区块提交标记 ==========
	commitKey := keys.KeyVMCommitHeight(b.Header.Height)
	x.DB.EnqueueSet(commitKey, b.BlockHash)

	// 閸栧搫娼℃妯哄缁便垹???
	blockHeightKey := keys.KeyVMBlockHeight(b.Header.Height)
	x.DB.EnqueueSet(blockHeightKey, b.BlockHash)

	// ========== 缁楊剙鍙氬銉窗閹绘劒姘︽导姘崇樈楠炶泛鎮撳?==========
	if err := sess.Commit(); err != nil {
		return fmt.Errorf("failed to commit db session: %v", err)
	}

	// 閺囧瓨鏌婇崘鍛摠娑擃厾娈戦悩鑸碘偓浣圭壌閿涘牏鈥樻穱婵嗘倵缂侇厽澧界悰宀冨厴閻鍩岄張鈧弬鎵閺堫剨???
	if b.Header.StateRoot != nil {
		x.DB.CommitRoot(b.Header.Height, b.Header.StateRoot)
	}

	// 瀵搫鍩楅崚閿嬫煀閸掔増鏆熼幑顔肩氨閿涘牏鏁ゆ禍搴ㄦ姜閻樿埖鈧焦鏆熼幑顔炬畱 EnqueueSet???
	return x.DB.ForceFlush()
}

// IsBlockCommitted 濡偓閺屻儱灏崸妤佹Ц閸氾箑鍑￠幓鎰唉
func (x *Executor) IsBlockCommitted(height uint64) (bool, string) {
	key := keys.KeyVMCommitHeight(height)
	blockID, err := x.DB.Get(key)
	if err != nil || blockID == nil {
		return false, ""
	}
	return true, string(blockID)
}

// extractAddressFromAccountKey 娴犲氦澶勯幋?key 娑擃厽褰侀崣鏍ф勾閸р偓
// extractAddressFromAccountKey 从账户 key 中提取地址
func extractAddressFromAccountKey(key string) string {
	// 缁夊娅庨悧鍫熸拱閸撳秶???
	prefixes := []string{"v1_account_", "account_"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(key, prefix) {
			return strings.TrimPrefix(key, prefix)
		}
	}
	return ""
}

// calcStake 鐠侊紕鐣荤拹锔藉煕???stake閿涘牅濞囬悽銊ュ瀻缁傝鐡ㄩ崒顭掔礆
// CalcStake = FB.miner_locked_balance
func calcStake(sv StateView, addr string) (decimal.Decimal, error) {
	fbBal := GetBalance(sv, addr, "FB")
	ml, err := decimal.NewFromString(fbBal.MinerLockedBalance)
	if err != nil {
		ml = decimal.Zero
	}
	return ml, nil
}

// GetTransactionStatus 閼惧嘲褰囨禍銈嗘閻樿埖???
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

func (x *Executor) isTxApplied(txID string) bool {
	if txID == "" {
		return false
	}
	key := keys.KeyVMAppliedTx(txID)
	status, err := x.DB.Get(key)
	if err != nil || status == nil {
		return false
	}
	return true
}

// GetTransactionError 閼惧嘲褰囨禍銈嗘闁挎瑨顕ゆ穱鈩冧紖
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

// CleanupCache 濞撳懐鎮婄紓鎾崇摠
func (x *Executor) CleanupCache(finalizedHeight uint64) {
	if finalizedHeight > 100 {
		// 娣囨繄鏆€閺堚偓???00娑擃亪鐝惔锔炬畱缂傛挸???
		x.Cache.EvictBelow(finalizedHeight - 100)
	}
}

// ValidateBlock 妤犲矁鐦夐崠鍝勬健閸╃儤婀版穱鈩冧紖
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

// collectPairsFromBlock 妫板嫭澹傞幓蹇撳隘閸ф绱濋弨鍫曟肠閹碘偓閺堝娓剁憰浣规尦閸氬牏娈戞禍銈嗘???
// collectPairsFromBlock 预扫描区块，收集所有需要撮合的交易对
func collectPairsFromBlock(b *pb.Block) []string {
	pairSet := make(map[string]struct{})

	for _, anyTx := range b.Body {
		// 閸欘亜顦╅悶?OrderTx
		orderTx := anyTx.GetOrderTx()
		if orderTx == nil {
			continue
		}

		// 閸欘亜顦╅悶?ADD 閹垮秳???
		if orderTx.Op != pb.OrderOp_ADD {
			continue
		}

		// 閻㈢喐鍨氭禍銈嗘???key
		pair := utils.GeneratePairKey(orderTx.BaseToken, orderTx.QuoteToken)
		pairSet[pair] = struct{}{}
	}

	// 鏉烆剚宕叉稉?slice 楠炶埖甯撴惔蹇ョ礉绾喕绻氱涵顔肩暰閹囦憾閸樺棝銆庢惔?
	pairs := make([]string, 0, len(pairSet))
	for pair := range pairSet {
		pairs = append(pairs, pair)
	}
	sort.Strings(pairs)

	return pairs
}

//	娑撯偓濞嗏剝鈧囧櫢瀵ょ儤澧嶉張澶夋唉閺勬挸顕惃鍕吂閸楁洜???
//
//
func (x *Executor) rebuildOrderBooksForPairs(pairs []string, sv StateView) (map[string]*matching.OrderBook, error) {
	if len(pairs) == 0 {
		return make(map[string]*matching.OrderBook), nil
	}

	pairBooks := make(map[string]*matching.OrderBook)

	for _, pair := range pairs {
		ob := matching.NewOrderBookWithSink(nil)

		// 2. 閸掑棗鍩嗛崝鐘烘祰娑旀壆娲忛崪灞藉礌閻╂娈戦張顏呭灇娴溿倛顓归崡?(is_filled:false)
		// 2. 分别加载买盘和卖盘的未成交订单 (is_filled:false)
		buyPrefix := keys.KeyOrderPriceIndexPrefix(pair, pb.OrderSide_BUY, false)
		buyOrders, err := x.DB.ScanKVWithLimitReverse(buyPrefix, 500)
		if err == nil {
			// ???keys 閹烘帒绨禒銉р€樻穱婵堚€樼€规碍鈧囦憾閸樺棝銆庢惔?
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

		// 閸旂姾娴囬崡鏍磸 Top 500
		sellPrefix := keys.KeyOrderPriceIndexPrefix(pair, pb.OrderSide_SELL, false)
		sellOrders, err := x.DB.ScanKVWithLimit(sellPrefix, 500)
		if err == nil {
			// ???keys 閹烘帒绨禒銉р€樻穱婵堚€樼€规碍鈧囦憾閸樺棝銆庢惔?
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

// 鏉堝懎濮弬瑙勭《閿涙矮绮犻弫鐗堝祦閸旂姾娴囩拋銏犲礋閸掓媽顓归崡鏇犵勘
func (x *Executor) loadOrderToBook(orderID string, indexData []byte, ob *matching.OrderBook, sv StateView) {
	// 鐏忔繆鐦禒?OrderState 鐠囪褰囬敍鍫熸付閺傛壆濮搁幀渚婄礆
	orderStateKey := keys.KeyOrderState(orderID)
	orderStateData, err := x.DB.GetKV(orderStateKey)
	if err == nil && len(orderStateData) > 0 {
		var orderState pb.OrderState
		if err := unmarshalProtoCompat(orderStateData, &orderState); err == nil {
			matchOrder, _ := convertOrderStateToMatchingOrder(&orderState)
			if matchOrder != nil {
				ob.AddOrderWithoutMatch(matchOrder)
				return
			}
		}
	}

	// 閸忕厧顔愰柅鏄忕帆閿涙矮???indexData ???sv 鐠囪???
	var orderTx pb.OrderTx
	if err := unmarshalProtoCompat(indexData, &orderTx); err == nil {
		matchOrder, _ := convertToMatchingOrderLegacy(&orderTx)
		if matchOrder != nil {
			ob.AddOrderWithoutMatch(matchOrder)
		}
	}
}

// extractOrderIDFromIndexKey 娴犲簼鐜弽鑲╁偍???key 娑擃厽褰侀崣?orderID
func extractOrderIDFromIndexKey(indexKey string) string {
	parts := strings.Split(indexKey, "|order_id:")
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}

// convertOrderStateToMatchingOrder ???pb.OrderState 鏉烆剚宕叉稉?matching.Order
func convertOrderStateToMatchingOrder(state *pb.OrderState) (*matching.Order, error) {
	if state == nil {
		return nil, fmt.Errorf("invalid order state")
	}

	priceBI, err := parsePositiveBalanceStrict("order price", state.Price)
	if err != nil {
		return nil, fmt.Errorf("invalid price: %w", err)
	}

	totalAmountBI, err := parsePositiveBalanceStrict("order amount", state.Amount)
	if err != nil {
		return nil, fmt.Errorf("invalid amount: %w", err)
	}

	filledBaseBI, err := parseBalanceStrict("order filled_base", state.FilledBase)
	if err != nil {
		return nil, fmt.Errorf("invalid filled_base: %w", err)
	}

	filledQuoteBI, err := parseBalanceStrict("order filled_quote", state.FilledQuote)
	if err != nil {
		return nil, fmt.Errorf("invalid filled_quote: %w", err)
	}

	filledTradeBI := filledBaseBI
	if state.BaseToken > state.QuoteToken {
		filledTradeBI = filledQuoteBI
	}

	remainingAmountBI, err := SafeSub(totalAmountBI, filledTradeBI)
	if err != nil || remainingAmountBI.Sign() <= 0 {
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
		Price:  balanceToDecimal(priceBI),
		Amount: balanceToDecimal(remainingAmountBI),
	}, nil
}

// convertToMatchingOrderLegacy 鐏忓棙妫悧?pb.OrderTx 鏉烆剚宕叉稉?matching.Order閿涘牆鍚嬬€硅鈧嶇礆
// 閺冄呭 OrderTx 濞屸剝???FilledBase/FilledQuote 鐎涙顔岄敍灞戒海鐠佹崘顓归崡鏇熸弓閹存劒???
func convertToMatchingOrderLegacy(ord *pb.OrderTx) (*matching.Order, error) {
	if ord == nil || ord.Base == nil {
		return nil, fmt.Errorf("invalid order")
	}

	priceBI, err := parsePositiveBalanceStrict("order price", ord.Price)
	if err != nil {
		return nil, fmt.Errorf("invalid price: %w", err)
	}

	amountBI, err := parsePositiveBalanceStrict("order amount", ord.Amount)
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
		Price:  balanceToDecimal(priceBI),
		Amount: balanceToDecimal(amountBI),
	}, nil
}

// getBaseMessage 鏉堝懎濮崙鑺ユ殶閿涙矮???AnyTx 娑擃厽褰侀崣?BaseMessage
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

// balanceToJSON ???TokenBalance 鎼村繐鍨崠鏍﹁礋 JSON 鐎涙顑佹稉璇х礄閻劋???Receipt 韫囶偆鍙庨敍?
func balanceToJSON(bal *pb.TokenBalance) string {
	if bal == nil {
		return ""
	}
	type snap struct {
		Balance       string `json:"balance"`
		MinerLocked   string `json:"miner_locked,omitempty"`
		WitnessLocked string `json:"witness_locked,omitempty"`
		OrderFrozen   string `json:"order_frozen,omitempty"`
		LiquidLocked  string `json:"liquid_locked,omitempty"`
	}
	s := snap{
		Balance:       bal.Balance,
		MinerLocked:   bal.MinerLockedBalance,
		WitnessLocked: bal.WitnessLockedBalance,
		OrderFrozen:   bal.OrderFrozenBalance,
		LiquidLocked:  bal.LiquidLockedBalance,
	}
	data, _ := json.Marshal(s)
	return string(data)
}
