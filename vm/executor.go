package vm

import (
	dbpkg "dex/db"
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
	"time"

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

type stateDBSyncer interface {
	SyncStateUpdates(height uint64, updates []interface{}) (int, error)
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

func (x *Executor) preExecuteBlock(b *pb.Block, useCache bool) (res *SpecResult, err error) {
	deepProbeEnabled := isVMDeepProbeEnabled()
	startAt := time.Now()
	var (
		durRebuild      time.Duration
		durAppliedCheck time.Duration
		durDryRun       time.Duration
		durApplyWS      time.Duration
		durWitness      time.Duration
		durRewards      time.Duration
		durDiff         time.Duration
		txSeen          int
		txExecuted      int
		wsOps           int
		dryRunErrors    int
		diffOps         int
		pairCount       int
		failedTxCount   int
		pairBooks       map[string]*matching.OrderBook
		rebuildStats    *orderBookRebuildStats
	)
	failureReasons := make(map[string]int)
	failedSamples := make([]string, 0, 5)
	defer func() {
		total := time.Since(startAt)
		height := uint64(0)
		hash := ""
		valid := false
		reason := ""
		if b != nil {
			height = b.Header.Height
			hash = b.BlockHash
		}
		if res != nil {
			valid = res.Valid
			reason = res.Reason
			diffOps = len(res.Diff)
		}
		if err != nil {
			reason = err.Error()
		}

		vmProbe.preBlocks.Add(1)
		vmProbe.preTxSeen.Add(uint64(txSeen))
		vmProbe.preTxExecuted.Add(uint64(txExecuted))
		vmProbe.prePairs.Add(uint64(pairCount))
		vmProbe.preDiffOps.Add(uint64(diffOps))
		vmProbe.preWSOps.Add(uint64(wsOps))
		vmProbe.preDryRunErrors.Add(uint64(dryRunErrors))
		addProbeDuration(&vmProbe.preTotalNs, total)
		addProbeDuration(&vmProbe.preRebuildNs, durRebuild)
		addProbeDuration(&vmProbe.preAppliedCheckNs, durAppliedCheck)
		addProbeDuration(&vmProbe.preDryRunNs, durDryRun)
		addProbeDuration(&vmProbe.preApplyWSNs, durApplyWS)
		addProbeDuration(&vmProbe.preWitnessNs, durWitness)
		addProbeDuration(&vmProbe.preRewardsNs, durRewards)
		addProbeDuration(&vmProbe.preDiffNs, durDiff)

		if total >= vmProbeSlowPreExecute {
			logs.Warn(
				"[VM][Probe][SlowPreExecute] height=%d hash=%s total=%s rebuild=%s applied=%s dryrun=%s applyWS=%s witness=%s rewards=%s diff=%s txSeen=%d txExec=%d pairs=%d wsOps=%d diffOps=%d dryErr=%d valid=%v reason=%s",
				height, hash, total, durRebuild, durAppliedCheck, durDryRun, durApplyWS, durWitness, durRewards, durDiff,
				txSeen, txExecuted, pairCount, wsOps, diffOps, dryRunErrors, valid, reason,
			)
			if deepProbeEnabled && rebuildStats != nil {
				logs.Warn(
					"[VM][Probe][SlowPreExecute][RebuildEx] height=%d hash=%s pairs=%d cand=%d stateKeys=%d stateHits=%d loadedState=%d loadedLegacy=%d scan=%s stateLoad=%s loadBook=%s",
					height,
					hash,
					rebuildStats.pairs,
					rebuildStats.candidates,
					rebuildStats.stateKeys,
					rebuildStats.stateHits,
					rebuildStats.loadedFromState,
					rebuildStats.loadedLegacy,
					rebuildStats.durScan,
					rebuildStats.durStateLoad,
					rebuildStats.durLoadBook,
				)
			}
		}
		maybeLogVMProbeSummary(time.Now())
	}()

	if b == nil {
		return nil, ErrNilBlock
	}

	baseCommittedHeight, baseCommittedHash := x.resolveBaseCommittedState(b)

	// 检查缓存
	if useCache {
		if cached, ok := x.Cache.Get(b.BlockHash); ok {
			if x.isCachedSpecUsable(b, cached) {
				return cached, nil
			}
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
	appliedCheckAt := time.Now()
	for txID, applied := range x.prefetchAppliedTxStatus(b) {
		appliedCache[txID] = applied
	}
	durAppliedCheck += time.Since(appliedCheckAt)
	skippedDupInBlock := 0
	skippedApplied := 0

	// Step 1: 预扫描区块，收集所有交易对
	pairs := collectPairsFromBlock(b)
	pairCount = len(pairs)
	pairOrderBounds := collectOrderPriceBoundsFromBlock(b)

	// Step 2 & 3: 一次性重建所有订单簿
	rebuildAt := time.Now()
	pairBooks, rebuildStats, err = x.rebuildOrderBooksForPairs(pairs, pairOrderBounds, deepProbeEnabled)
	durRebuild += time.Since(rebuildAt)
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
		txSeen++

		txID := tx.GetTxId()
		if txID != "" {
			if _, exists := seenTxIDs[txID]; exists {
				skippedDupInBlock++
				continue
			}
			seenTxIDs[txID] = struct{}{}

			applied, ok := appliedCache[txID]
			if !ok {
				appliedCheckAt := time.Now()
				applied = x.isTxApplied(txID)
				durAppliedCheck += time.Since(appliedCheckAt)
				appliedCache[txID] = applied
			}
			if applied {
				skippedApplied++
				continue
			}
		}
		txExecuted++
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
		dryRunAt := time.Now()
		ws, rc, err := h.DryRun(tx, sv)
		durDryRun += time.Since(dryRunAt)
		if err != nil {
			dryRunErrors++
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
				failedTxCount++
				failureReasons[classifyTxFailureReason(err)]++
				if len(failedSamples) < cap(failedSamples) {
					failedSamples = append(failedSamples, fmt.Sprintf("%s:%s", rc.TxID, err.Error()))
				}
				logs.Debug("[VM] Tx %s mark as FAILED in block %d hash=%s: %v",
					rc.TxID, b.Header.Height, b.BlockHash, err)
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
		applyWSAt := time.Now()
		for _, w := range ws {
			wsOps++
			if w.Del {
				sv.Del(w.Key)
			} else {
				sv.Set(w.Key, w.Value)
			}
		}
		durApplyWS += time.Since(applyWSAt)
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
	if failedTxCount > 0 {
		summary := fmt.Sprintf(
			"[VM] failed tx summary: height=%d hash=%s failed=%d uniqueReasons=%d topReasons=%s samples=%s",
			b.Header.Height,
			b.BlockHash,
			failedTxCount,
			len(failureReasons),
			formatTopFailureReasons(failureReasons, 3),
			strings.Join(failedSamples, "; "),
		)
		if shouldLogVMFailedSummaryAtInfo(b.BlockHash) {
			logs.Info(summary)
		} else {
			logs.Debug(summary)
		}
	}

	witnessAt := time.Now()
	if err := x.applyWitnessFinalizedEvents(sv, b.Header.Height); err != nil {
		durWitness += time.Since(witnessAt)
		return nil, err
	}
	durWitness += time.Since(witnessAt)

	// ========== 区块奖励与手续费分发 ==========
	rewardAt := time.Now()
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
	durRewards += time.Since(rewardAt)

	diffAt := time.Now()
	diff := sv.Diff()
	durDiff += time.Since(diffAt)
	res = &SpecResult{
		BlockID:             b.BlockHash,
		ParentID:            b.Header.PrevBlockHash,
		Height:              b.Header.Height,
		BaseCommittedHeight: baseCommittedHeight,
		BaseCommittedHash:   baseCommittedHash,
		Valid:               true,
		Receipts:            receipts,
		Diff:                diff,
	}

	// 缓存结果
	x.Cache.Put(res)
	return res, nil
}

// CommitFinalizedBlock 最终化提交（写入数据库）
func (x *Executor) CommitFinalizedBlock(b *pb.Block) (err error) {
	startAt := time.Now()
	var (
		durReexec time.Duration
		durApply  time.Duration
		usedCache bool
	)
	defer func() {
		total := time.Since(startAt)
		height := uint64(0)
		hash := ""
		if b != nil {
			height = b.Header.Height
			hash = b.BlockHash
		}
		vmProbe.commitCalls.Add(1)
		addProbeDuration(&vmProbe.commitTotalNs, total)
		addProbeDuration(&vmProbe.commitReexecNs, durReexec)
		addProbeDuration(&vmProbe.commitApplyNs, durApply)

		if total >= vmProbeSlowCommitBlock {
			logs.Warn(
				"[VM][Probe][SlowCommit] height=%d hash=%s total=%s reexec=%s apply=%s cacheHit=%v err=%v",
				height, hash, total, durReexec, durApply, usedCache, err,
			)
		}
		maybeLogVMProbeSummary(time.Now())
	}()

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

	// 延迟执行：始终全量执行（Add 阶段已跳过 PreExecuteBlock）
	var res *SpecResult
	reexecAt := time.Now()
	res, err = x.preExecuteBlock(b, false)
	durReexec += time.Since(reexecAt)
	if err != nil {
		return fmt.Errorf("re-execute block failed: %v", err)
	}

	if !res.Valid {
		return fmt.Errorf("block invalid: %s", res.Reason)
	}

	applyAt := time.Now()
	err = x.applyResult(res, b)
	durApply += time.Since(applyAt)
	return err
}

// applyResult 应用执行结果到数据库（统一提交入口）
// 这是唯一的最终化提交点，所有状态变化都在这里处理
func (x *Executor) applyResult(res *SpecResult, b *pb.Block) (err error) {
	startAt := time.Now()
	var (
		durDiffLoop  time.Duration
		durStake     time.Duration
		durStateSync time.Duration
		durFlush     time.Duration
		writeOps     int
		accountOps   int
		stateOps     int
	)
	defer func() {
		total := time.Since(startAt)
		height := uint64(0)
		hash := ""
		if b != nil {
			height = b.Header.Height
			hash = b.BlockHash
		}
		vmProbe.applyCalls.Add(1)
		vmProbe.applyWriteOps.Add(uint64(writeOps))
		vmProbe.applyAccountOps.Add(uint64(accountOps))
		vmProbe.applyStateOps.Add(uint64(stateOps))
		addProbeDuration(&vmProbe.applyTotalNs, total)
		addProbeDuration(&vmProbe.applyDiffNs, durDiffLoop)
		addProbeDuration(&vmProbe.applyStakeNs, durStake)
		addProbeDuration(&vmProbe.applySyncNs, durStateSync)
		addProbeDuration(&vmProbe.applyFlushNs, durFlush)

		if total >= vmProbeSlowApplyResult {
			logs.Warn(
				"[VM][Probe][SlowApply] height=%d hash=%s total=%s diffLoop=%s stake=%s sync=%s flush=%s writeOps=%d accountOps=%d stateOps=%d err=%v",
				height, hash, total, durDiffLoop, durStake, durStateSync, durFlush, writeOps, accountOps, stateOps, err,
			)
		}
		maybeLogVMProbeSummary(time.Now())
	}()
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
	// 在主库队列写入前，先同步 stateDB，避免主库已排队但 stateDB 失败。
	_, strictStateSeparation := x.DB.(*dbpkg.Manager)
	stateSynced := false
	if syncer, ok := x.DB.(stateDBSyncer); ok && len(res.Diff) > 0 {
		updates := make([]interface{}, 0, len(res.Diff))
		for i := range res.Diff {
			updates = append(updates, &res.Diff[i])
		}
		syncAt := time.Now()
		stateOps, err = syncer.SyncStateUpdates(b.Header.Height, updates)
		durStateSync += time.Since(syncAt)
		if err != nil {
			return fmt.Errorf("sync stateDB updates failed: %w", err)
		}
		stateSynced = true
	}

	// 遍历 Diff 中的所有写操作
	accountUpdates := make([]*WriteOp, 0)
	writeOps = len(res.Diff)
	diffLoopAt := time.Now()
	for i := range res.Diff {
		w := &res.Diff[i] // 使用指针，因为 WriteOp 的方法是指针接收器
		// 第一步：如果是账户更新，标记为需要更新 stake index
		if !w.Del && (w.Category == "account" || strings.HasPrefix(w.Key, "v1_account_")) {
			accountUpdates = append(accountUpdates, w)
		}

		// strict key separation:
		// once state diff has been persisted to stateDB, do not enqueue stateful keys into KV main DB.
		if strictStateSeparation && stateSynced && keys.IsStatefulKey(w.Key) {
			continue
		}

		// 第二步：写入数据库
		if w.Del {
			x.DB.EnqueueDel(w.Key)
		} else {
			x.DB.EnqueueSet(w.Key, string(w.Value))
		}
	}

	// ========== 更新 Stake Index ==========
	// ========== 第三步：更新 Stake Index ==========
	durDiffLoop += time.Since(diffLoopAt)
	accountOps = len(accountUpdates)

	stakeAt := time.Now()
	if len(accountUpdates) > 0 {
		// 检查 DBManager 是否实现了 UpdateStakeIndex 接口
		type StakeIndexUpdater interface {
			UpdateStakeIndex(oldStake, newStake decimal.Decimal, address string) error
		}
		if updater, ok := x.DB.(StakeIndexUpdater); ok {
			for _, w := range accountUpdates {
				// 从账户 key 中提取地址 (v1_account_<address>)
				address := extractAddressFromAccountKey(w.Key)
				if address == "" {
					continue
				}

				// 计算更新前后的质押量 (stake)
				// 使用分离存储的余额计算 stake
				// 注意：这里需要从 StateDB 或 WriteOps 中读取 FB 余额

				// 获取旧的 stake（非事务读，避免扩大主事务读集合）
				oldStake := decimal.Zero
				if fbBalData, err := x.DB.Get(keys.KeyBalance(address, "FB")); err == nil && fbBalData != nil {
					var balRecord pb.TokenBalanceRecord
					if err := unmarshalProtoCompat(fbBalData, &balRecord); err == nil && balRecord.Balance != nil {
						oldStake, _ = decimal.NewFromString(balRecord.Balance.MinerLockedBalance)
					}
				}

				// 获取新的 stake (通过在 WriteOps 中查找对应的余额更新)
				newStake := oldStake // 计算新 stake（从当前 WriteOps 中查找对应的余额更新）
				balanceKey := keys.KeyBalance(address, "FB")
				for _, wop := range res.Diff {
					if wop.Key == balanceKey && !wop.Del {
						var balRecord pb.TokenBalanceRecord
						if err := unmarshalProtoCompat(wop.Value, &balRecord); err == nil && balRecord.Balance != nil {
							newStake, _ = decimal.NewFromString(balRecord.Balance.MinerLockedBalance)
						}
						break
					}
				}

				// 如果 stake 发生变化，更新 stake index
				if !oldStake.Equal(newStake) {
					if err := updater.UpdateStakeIndex(oldStake, newStake, address); err != nil {
						// 记录更新索引失败的警告
						fmt.Printf("[VM] Warning: failed to update stake index for %s: %v\n", address, err)
					}
				}
			}
		}
	}
	durStake += time.Since(stakeAt)

	// ========== 第四步：写入交易处理状态 ==========
	for _, rc := range res.Receipts {
		// 写入交易成功/失败状态
		statusKey := keys.KeyVMAppliedTx(rc.TxID)
		x.DB.EnqueueSet(statusKey, rc.Status)

		// 写入具体的错误信息（如果有）
		if rc.Error != "" {
			errorKey := keys.KeyVMTxError(rc.TxID)
			x.DB.EnqueueSet(errorKey, rc.Error)
		}

		// 写入交易所属的区块高度
		heightKey := keys.KeyVMTxHeight(rc.TxID)
		x.DB.EnqueueSet(heightKey, fmt.Sprintf("%d", b.Header.Height))

		// 写入交易执行后的 FB 余额快照
		if rc.FBBalanceAfter != "" {
			fbKey := keys.KeyVMTxFBBalance(rc.TxID)
			x.DB.EnqueueSet(fbKey, rc.FBBalanceAfter)
		}
	}

	// ========== 第四步补充：更新区块交易状态并保存原文 ==========
	receiptMap := make(map[string]*Receipt, len(res.Receipts))
	for _, rc := range res.Receipts {
		receiptMap[rc.TxID] = rc
	}

	// 将更新后的交易原文保存到数据库（不可变），以便后续查询。
	// 注意：不要原地修改 b.Body 里的交易对象，避免多节点共享同一 *pb.Block 时产生状态污染。
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
			rc := receiptMap[txID]

			// 仅在副本上写执行结果，确保输入区块对象保持只读语义。
			txForSave := proto.Clone(tx).(*pb.AnyTx)
			if base := txForSave.GetBase(); base != nil {
				base.ExecutedHeight = b.Header.Height
				if rc.Status == "SUCCEED" || rc.Status == "" {
					base.Status = pb.Status_SUCCEED
				} else {
					base.Status = pb.Status_FAILED
				}
			}

			// 如果保存原文失败，记录警告
			if err := saver.SaveTxRaw(txForSave); err != nil {
				// 记录更新索引失败的警告
				fmt.Printf("[VM] Warning: failed to save tx raw %s: %v\n", txID, err)
			}
			savedRawTxs[txID] = struct{}{}
		}
	}

	// ========== 第五步：写入区块提交标记 ==========
	commitKey := keys.KeyVMCommitHeight(b.Header.Height)
	x.DB.EnqueueSet(commitKey, b.BlockHash)

	// 记录区块高度到哈希的映射
	blockHeightKey := keys.KeyVMBlockHeight(b.Header.Height)
	x.DB.EnqueueSet(blockHeightKey, b.BlockHash)

	// ========== 提交数据库事务并强制刷新 ==========

	// 强制刷盘，确保数据落盘成功后再返回给上层共识模块
	flushAt := time.Now()
	err = x.DB.ForceFlush()
	durFlush += time.Since(flushAt)
	return err
}

// IsBlockCommitted 检查指定高度的区块是否已经提交过
func (x *Executor) IsBlockCommitted(height uint64) (bool, string) {
	key := keys.KeyVMCommitHeight(height)
	blockID, err := x.DB.Get(key)
	if err != nil || blockID == nil {
		return false, ""
	}
	return true, string(blockID)
}

// extractAddressFromAccountKey 从账户 key 中提取地址
func extractAddressFromAccountKey(key string) string {
	// 尝试匹配不同的账户前缀
	prefixes := []string{"v1_account_", "account_"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(key, prefix) {
			return strings.TrimPrefix(key, prefix)
		}
	}
	return ""
}

// calcStake 计算一个地址当前的有效质押量
// 目前定义为：CalcStake = FB.miner_locked_balance
func calcStake(sv StateView, addr string) (decimal.Decimal, error) {
	fbBal := GetBalance(sv, addr, "FB")
	ml, err := decimal.NewFromString(fbBal.MinerLockedBalance)
	if err != nil {
		ml = decimal.Zero
	}
	return ml, nil
}

// GetTransactionStatus 获取交易执行状态
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

func (x *Executor) resolveBaseCommittedState(b *pb.Block) (uint64, string) {
	if b == nil || b.Header.Height == 0 {
		return 0, ""
	}
	parentHeight := b.Header.Height - 1
	if ok, hash := x.IsBlockCommitted(parentHeight); ok {
		return parentHeight, hash
	}
	return 0, ""
}

func (x *Executor) isCachedSpecUsable(b *pb.Block, cached *SpecResult) bool {
	if b == nil || cached == nil {
		return false
	}
	if !cached.Valid {
		return false
	}
	if cached.BlockID != b.BlockHash || cached.ParentID != b.Header.PrevBlockHash || cached.Height != b.Header.Height {
		return false
	}

	baseHeight, baseHash := x.resolveBaseCommittedState(b)
	if baseHeight == 0 && baseHash == "" {
		return cached.BaseCommittedHeight == 0 && cached.BaseCommittedHash == ""
	}
	return cached.BaseCommittedHeight == baseHeight && cached.BaseCommittedHash == baseHash
}

func collectUniqueTxIDsFromBlock(b *pb.Block) []string {
	if b == nil || len(b.Body) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(b.Body))
	txIDs := make([]string, 0, len(b.Body))
	for _, tx := range b.Body {
		if tx == nil {
			continue
		}
		txID := tx.GetTxId()
		if txID == "" {
			continue
		}
		if _, ok := seen[txID]; ok {
			continue
		}
		seen[txID] = struct{}{}
		txIDs = append(txIDs, txID)
	}
	return txIDs
}

func (x *Executor) prefetchAppliedTxStatus(b *pb.Block) map[string]bool {
	txIDs := collectUniqueTxIDsFromBlock(b)
	applied := make(map[string]bool, len(txIDs))
	if len(txIDs) == 0 {
		return applied
	}

	fetchKeys := make([]string, 0, len(txIDs))
	keyToTxID := make(map[string]string, len(txIDs))
	for _, txID := range txIDs {
		key := keys.KeyVMAppliedTx(txID)
		fetchKeys = append(fetchKeys, key)
		keyToTxID[key] = txID
	}

	if batchGetter, ok := x.DB.(kvBatchGetter); ok {
		if kvs, err := batchGetter.GetKVs(fetchKeys); err == nil {
			for key, val := range kvs {
				if len(val) == 0 {
					continue
				}
				if txID, exists := keyToTxID[key]; exists {
					applied[txID] = true
				}
			}
			return applied
		}
	}

	for _, txID := range txIDs {
		applied[txID] = x.isTxApplied(txID)
	}
	return applied
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

// GetTransactionError 获取交易具体的错误消息
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
		// 清理 100 个区块高度之前的缓存
		x.Cache.EvictBelow(finalizedHeight - 100)
	}
}

// ValidateBlock 简单的区块结构校验

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
func collectPairsFromBlock(b *pb.Block) []string {
	pairSet := make(map[string]struct{})

	for _, anyTx := range b.Body {
		// 仅处理 OrderTx
		orderTx := anyTx.GetOrderTx()
		if orderTx == nil {
			continue
		}

		// 仅处理 ADD 操作（新增挂单）
		if orderTx.Op != pb.OrderOp_ADD {
			continue
		}

		// 生成交易对的唯一标识 key
		pair := utils.GeneratePairKey(orderTx.BaseToken, orderTx.QuoteToken)
		pairSet[pair] = struct{}{}
	}

	// 转化为 slice 并排序，确保多节点执行顺序一致
	pairs := make([]string, 0, len(pairSet))
	for pair := range pairSet {
		pairs = append(pairs, pair)
	}
	sort.Strings(pairs)

	return pairs
}

func collectOrderPriceBoundsFromBlock(b *pb.Block) map[string]pairOrderPriceBounds {
	result := make(map[string]pairOrderPriceBounds)
	if b == nil {
		return result
	}

	for _, anyTx := range b.Body {
		orderTx := anyTx.GetOrderTx()
		if orderTx == nil || orderTx.Op != pb.OrderOp_ADD {
			continue
		}

		priceKey67, err := dbpkg.PriceToKey128(orderTx.Price)
		if err != nil {
			continue
		}

		pair := utils.GeneratePairKey(orderTx.BaseToken, orderTx.QuoteToken)
		bounds := result[pair]
		switch orderTx.Side {
		case pb.OrderSide_BUY:
			if !bounds.hasBuy || priceKey67 > bounds.maxBuyPriceKey {
				bounds.maxBuyPriceKey = priceKey67
			}
			bounds.hasBuy = true
		case pb.OrderSide_SELL:
			if !bounds.hasSell || priceKey67 < bounds.minSellPriceKey {
				bounds.minSellPriceKey = priceKey67
			}
			bounds.hasSell = true
		}
		result[pair] = bounds
	}

	return result
}

// convertOrderStateToMatchingOrder 将 pb.OrderState 转换为撮合引擎所需的 Order 结构
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

// convertToMatchingOrderLegacy 将旧版的 pb.OrderTx 转换为撮合引擎所需的 Order 结构
// 注意：旧版 OrderTx 不包含 FilledBase/FilledQuote，因此假定其为全量未成交订单
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

// getBaseMessage 从统一封装的 AnyTx 中提取具体交易的基础元数据 BaseMessage
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

// balanceToJSON 将 TokenBalance 的关键快照字段转为 JSON 字符串供 Receipt 存储
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
