// frost/runtime/planning/job_window_planner.go
// Job 窗口规划器：支持 FIFO 批量规划（最多 maxInFlightPerChainAsset 个 job）

package planning

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	chainpkg "dex/frost/chain"
	"dex/keys"
	"dex/logs"
	"dex/pb"

	"google.golang.org/protobuf/proto"
)

// JobWindowPlanner Job 窗口规划器
type JobWindowPlanner struct {
	stateReader    ChainStateReader
	adapterFactory chainpkg.ChainAdapterFactory
	maxInFlight    int // 最多并发 job 数（maxInFlightPerChainAsset）
	transientLogs  map[string][]*pb.FrostPlanningLog
}

// NewJobWindowPlanner 创建新的 Job 窗口规划器
func NewJobWindowPlanner(stateReader ChainStateReader, adapterFactory chainpkg.ChainAdapterFactory, maxInFlight int) *JobWindowPlanner {
	if maxInFlight <= 0 {
		maxInFlight = 1 // 默认值
	}
	return &JobWindowPlanner{
		stateReader:    stateReader,
		adapterFactory: adapterFactory,
		maxInFlight:    maxInFlight,
		transientLogs:  make(map[string][]*pb.FrostPlanningLog),
	}
}

// PlanJobWindow 规划 Job 窗口（最多 maxInFlight 个 job）
// 返回：Job 列表和产生的临时日志（针对未生成 Job 的 withdraw）
func (p *JobWindowPlanner) PlanJobWindow(chain, asset string) ([]*Job, map[string][]*pb.FrostPlanningLog, error) {
	// 清空旧日志
	p.transientLogs = make(map[string][]*pb.FrostPlanningLog)

	// 1. 扫描连续的 QUEUED withdraw（最多 maxInFlight 个）
	withdraws, err := p.scanQueuedWithdraws(chain, asset, p.maxInFlight)
	if err != nil {
		return nil, nil, err
	}
	if len(withdraws) == 0 {
		return nil, nil, nil
	}

	// 2. 根据链类型选择规划策略
	var jobs []*Job
	if chain == "btc" {
		// BTC：批量规划（1 个 input 支付 N 个 withdraw outputs）
		jobs, err = p.planBTCJobWindow(chain, asset, withdraws)
	} else {
		// 账户链/合约链：为每个 withdraw 规划 job（或批量规划）
		jobs, err = p.planAccountChainJobWindow(chain, asset, withdraws)
	}

	if err != nil {
		return nil, nil, err
	}
	return jobs, p.transientLogs, nil
}

// planBTCJobWindow BTC 批量规划（贪心装箱：1 个 input 支付 N 个 withdraw）
func (p *JobWindowPlanner) planBTCJobWindow(chain, asset string, withdraws []*ScanResult) ([]*Job, error) {
	jobs := make([]*Job, 0)
	consumedWithdraws := make(map[string]bool) // 已规划的 withdraw

	// 贪心装箱：从队首开始，尽可能多地将 withdraw 打包到一个 job
	for i := 0; i < len(withdraws); i++ {
		if consumedWithdraws[withdraws[i].WithdrawID] {
			continue
		}

		// 收集连续的 withdraw 直到达到上限或余额不足
		batchWithdraws := make([]*ScanResult, 0)
		var totalAmount uint64

		// 选择 Vault（需要知道总金额，先估算）
		// 简化：先选择第一个 ACTIVE Vault，然后检查余额
		vaultID, keyEpoch, err := p.selectVault(chain, asset, 0) // 先不检查余额
		if err != nil {
			logs.Warn("[JobWindowPlanner] failed to select vault: %v", err)
			continue
		}

		// 贪心装箱：尽可能多地收集 withdraw
		maxOutputs := 10 // 配置项：最多输出数量
		for j := i; j < len(withdraws) && len(batchWithdraws) < maxOutputs; j++ {
			if consumedWithdraws[withdraws[j].WithdrawID] {
				break
			}

			// 读取 withdraw 金额
			withdrawKey := keys.KeyFrostWithdraw(withdraws[j].WithdrawID)
			withdrawData, exists, err := p.stateReader.Get(withdrawKey)
			if err != nil || !exists {
				break
			}

			state := &pb.FrostWithdrawState{}
			if err := proto.Unmarshal(withdrawData, state); err != nil {
				break
			}

			amount := parseAmount(state.Amount)
			totalAmount += amount
			batchWithdraws = append(batchWithdraws, withdraws[j])
		}

		if len(batchWithdraws) == 0 {
			continue
		}

		// 规划 BTC job（选择 UTXO，构建模板）
		job, err := p.planBTCJob(chain, asset, vaultID, keyEpoch, batchWithdraws, totalAmount)
		if err != nil {
			logs.Warn("[JobWindowPlanner] failed to plan BTC job: %v", err)
			// 记录失败日志给每一个 withdraw
			for _, wd := range batchWithdraws {
				p.reportTransientLog(wd.WithdrawID, "PlanBTCJob", "FAILED", fmt.Sprintf("Failed to plan BTC job: %v", err))
			}
			// 如果批量规划失败，尝试单个规划
			for _, wd := range batchWithdraws {
				singleJob, err := p.planJobForWithdraw(wd)
				if err == nil && singleJob != nil {
					jobs = append(jobs, singleJob)
					consumedWithdraws[wd.WithdrawID] = true
				}
			}
			continue
		}

		if job != nil {
			jobs = append(jobs, job)
			// 标记已规划的 withdraw
			for _, wd := range batchWithdraws {
				consumedWithdraws[wd.WithdrawID] = true
			}
		}
	}

	return jobs, nil
}

// planAccountChainJobWindow 账户链/合约链规划（支持批量和 CompositeJob）
func (p *JobWindowPlanner) planAccountChainJobWindow(chain, asset string, withdraws []*ScanResult) ([]*Job, error) {
	jobs := make([]*Job, 0, len(withdraws))
	consumedWithdraws := make(map[string]bool) // 已规划的 withdraw

	for i := 0; i < len(withdraws); i++ {
		if consumedWithdraws[withdraws[i].WithdrawID] {
			continue
		}

		// 读取队首 withdraw 详情
		withdrawKey := keys.KeyFrostWithdraw(withdraws[i].WithdrawID)
		withdrawData, exists, err := p.stateReader.Get(withdrawKey)
		if err != nil || !exists {
			continue
		}

		state := &pb.FrostWithdrawState{}
		if err := proto.Unmarshal(withdrawData, state); err != nil {
			continue
		}

		amount := parseAmount(state.Amount)

		// 尝试单 Vault 规划
		vaultID, _, err := p.selectVault(chain, asset, amount)
		if err != nil {
			logs.Warn("[JobWindowPlanner] failed to select vault: %v", err)
			// 特殊情况：记录到该 withdraw 的日志中（此处没有 job 对象，需要通过其他方式返回或直接记录）
			// 我们给每一个 withdraw 记录一个 SelectVault FAILED 的日志
			p.reportTransientLog(withdraws[i].WithdrawID, "SelectVault", "FAILED", fmt.Sprintf("Failed to select vault: %v", err))
			continue
		}

		// 检查是否有单个 Vault 能覆盖
		available, err := p.calculateVaultAvailableBalance(chain, asset, vaultID)
		if err != nil {
			logs.Warn("[JobWindowPlanner] failed to calculate balance: %v", err)
			continue
		}

		if available >= amount {
			// 单 Vault 可以覆盖，使用普通规划
			job, err := p.planJobForWithdraw(withdraws[i])
			if err != nil {
				logs.Warn("[JobWindowPlanner] failed to plan job for withdraw %s: %v", withdraws[i].WithdrawID, err)
				continue
			}
			if job != nil {
				job.Logs = append(job.Logs, &pb.FrostPlanningLog{
					Step:      "SelectVault",
					Status:    "OK",
					Message:   fmt.Sprintf("Vault %d has sufficient balance", vaultID),
					Timestamp: uint64(time.Now().UnixMilli()),
				})
				jobs = append(jobs, job)
				consumedWithdraws[withdraws[i].WithdrawID] = true
			}
		} else {
			// 单 Vault 无法覆盖，尝试 CompositeJob（跨 Vault 组合支付）
			compositeJob, err := p.planCompositeJob(chain, asset, withdraws[i], amount)
			if err != nil {
				logs.Warn("[JobWindowPlanner] failed to plan composite job for withdraw %s: %v", withdraws[i].WithdrawID, err)
				// 如果 CompositeJob 规划失败，跳过该 withdraw（等待后续余额增加）
				continue
			}
			if compositeJob != nil {
				jobs = append(jobs, compositeJob)
				consumedWithdraws[withdraws[i].WithdrawID] = true
			}
		}
	}

	return jobs, nil
}

// planBTCJob 规划 BTC job（批量：多个 withdraw，选择 UTXO）
func (p *JobWindowPlanner) planBTCJob(chain, asset string, vaultID uint32, keyEpoch uint64, withdraws []*ScanResult, totalAmount uint64) (*Job, error) {
	// 1. 读取所有 withdraw 详情
	outputs := make([]chainpkg.WithdrawOutput, 0, len(withdraws))
	withdrawIDs := make([]string, 0, len(withdraws))
	var firstSeq uint64

	for i, wd := range withdraws {
		withdrawKey := keys.KeyFrostWithdraw(wd.WithdrawID)
		withdrawData, exists, err := p.stateReader.Get(withdrawKey)
		if err != nil || !exists {
			return nil, err
		}

		state := &pb.FrostWithdrawState{}
		if err := proto.Unmarshal(withdrawData, state); err != nil {
			return nil, err
		}

		if i == 0 {
			firstSeq = wd.Seq
		}

		outputs = append(outputs, chainpkg.WithdrawOutput{
			WithdrawID: wd.WithdrawID,
			To:         state.To,
			Amount:     parseAmount(state.Amount),
		})
		withdrawIDs = append(withdrawIDs, wd.WithdrawID)
	}

	// 2. 选择 UTXO（按 confirm_height 升序，FIFO）
	utxos, err := p.selectBTCUTXOs(chain, vaultID, totalAmount)
	if err != nil {
		return nil, err
	}
	if len(utxos) == 0 {
		return nil, fmt.Errorf("no available UTXOs for vault %d", vaultID)
	}

	// 3. 计算手续费和找零
	fee := p.estimateBTCFee(len(utxos), len(outputs))
	var totalInput uint64
	for _, utxo := range utxos {
		totalInput += utxo.Amount
	}

	changeAmount := uint64(0)
	if totalInput > totalAmount+fee {
		changeAmount = totalInput - totalAmount - fee
		// 如果找零小于粉尘限制，不创建找零输出（吃进手续费）
		if changeAmount < 546 { // DustLimit
			changeAmount = 0
		}
	}

	// 4. 获取链适配器并构建模板
	adapter, err := p.adapterFactory.Adapter(chain)
	if err != nil {
		return nil, err
	}

	params := chainpkg.WithdrawTemplateParams{
		Chain:         chain,
		Asset:         asset,
		VaultID:       vaultID,
		KeyEpoch:      keyEpoch,
		WithdrawIDs:   withdrawIDs,
		Outputs:       outputs,
		Inputs:        utxos,
		Fee:           fee,
		ChangeAmount:  changeAmount,
		ChangeAddress: "", // TODO: 从 VaultState 获取 treasury 地址
	}

	result, err := adapter.BuildWithdrawTemplate(params)
	if err != nil {
		return nil, err
	}

	// 5. 生成 job_id
	jobID := generateJobID(chain, asset, vaultID, firstSeq, result.TemplateHash, keyEpoch)

	return &Job{
		JobID:        jobID,
		Chain:        chain,
		Asset:        asset,
		VaultID:      vaultID,
		KeyEpoch:     keyEpoch,
		WithdrawIDs:  withdrawIDs,
		TemplateHash: result.TemplateHash,
		TemplateData: result.TemplateData,
		FirstSeq:     firstSeq,
	}, nil
}

// selectBTCUTXOs 选择 BTC UTXO（按 confirm_height 升序，FIFO）
func (p *JobWindowPlanner) selectBTCUTXOs(chain string, vaultID uint32, needAmount uint64) ([]chainpkg.UTXO, error) {
	// 1. 获取 VaultState 以获取 groupPubKey (用于 scriptPubKey)
	vaultStateKey := keys.KeyFrostVaultState(chain, vaultID)
	vaultStateData, exists, err := p.stateReader.Get(vaultStateKey)
	if err != nil || !exists {
		return nil, fmt.Errorf("vault state not found for vault %d (chain %s)", vaultID, chain)
	}
	var vaultState pb.FrostVaultState
	if err := proto.Unmarshal(vaultStateData, &vaultState); err != nil {
		return nil, fmt.Errorf("failed to unmarshal vault state: %v", err)
	}

	// Taproot scriptPubKey = OP_1 <32-byte-X-only-pubkey>
	var scriptPubKey []byte
	if len(vaultState.GroupPubkey) == 32 {
		scriptPubKey = make([]byte, 34)
		scriptPubKey[0] = 0x51
		scriptPubKey[1] = 0x20
		copy(scriptPubKey[2:], vaultState.GroupPubkey)
	}

	// 2. 扫描该 Vault 的所有 UTXO
	utxoPrefix := fmt.Sprintf("v1_frost_btc_utxo_%d_", vaultID)
	utxos := make([]chainpkg.UTXO, 0)

	err = p.stateReader.Scan(utxoPrefix, func(k string, v []byte) bool {
		// 解析 UTXO key: v1_frost_btc_utxo_<vault_id>_<txid>_<vout>
		parts := strings.Split(k, "_")
		if len(parts) < 6 {
			return true // 继续扫描
		}

		txid := parts[4]
		voutStr := parts[5]
		vout, err := strconv.ParseUint(voutStr, 10, 32)
		if err != nil {
			return true
		}

		// 检查是否已锁定
		lockKey := keys.KeyFrostBtcLockedUtxo(vaultID, txid, uint32(vout))
		_, locked, _ := p.stateReader.Get(lockKey)
		if locked {
			return true // 已锁定，跳过
		}

		// 解析 UTXO 数据
		var protoUtxo pb.FrostUtxo
		if err := proto.Unmarshal(v, &protoUtxo); err != nil {
			return true
		}

		utxo := chainpkg.UTXO{
			TxID:          txid,
			Vout:          uint32(vout),
			Amount:        parseAmount(protoUtxo.Amount),
			ScriptPubKey:  scriptPubKey,
			ConfirmHeight: protoUtxo.FinalizeHeight,
		}

		utxos = append(utxos, utxo)
		return true
	})

	if err != nil {
		return nil, err
	}

	// 按 confirm_height 升序排序（FIFO），同高度按 txid:vout 字典序
	sort.Slice(utxos, func(i, j int) bool {
		if utxos[i].ConfirmHeight != utxos[j].ConfirmHeight {
			return utxos[i].ConfirmHeight < utxos[j].ConfirmHeight
		}
		if utxos[i].TxID != utxos[j].TxID {
			return utxos[i].TxID < utxos[j].TxID
		}
		return utxos[i].Vout < utxos[j].Vout
	})

	// 贪心选择：累加直到 >= needAmount
	selected := make([]chainpkg.UTXO, 0)
	var total uint64
	for _, utxo := range utxos {
		if utxo.Amount == 0 {
			continue // 跳过金额为 0 的 UTXO
		}
		selected = append(selected, utxo)
		total += utxo.Amount
		if total >= needAmount {
			break
		}
	}

	if total < needAmount {
		return nil, fmt.Errorf("insufficient UTXOs: need %d, got %d", needAmount, total)
	}

	return selected, nil
}

// estimateBTCFee 估算 BTC 手续费（简化：基于 input/output 数量）
func (p *JobWindowPlanner) estimateBTCFee(inputCount, outputCount int) uint64 {
	// 简化估算：每个 input 约 148 bytes，每个 output 约 34 bytes
	// 假设费率 10 sat/vbyte
	baseSize := 10 // 交易基础大小
	inputSize := inputCount * 148
	outputSize := outputCount * 34
	witnessSize := inputCount * 64 // Taproot witness
	totalVBytes := baseSize + inputSize + outputSize + witnessSize/4
	feeRate := uint64(10) // sat/vbyte
	return uint64(totalVBytes) * feeRate
}

// scanQueuedWithdraws 扫描连续的 QUEUED withdraw
func (p *JobWindowPlanner) scanQueuedWithdraws(chain, asset string, maxCount int) ([]*ScanResult, error) {
	// 1. 读取当前 head
	headKey := keys.KeyFrostWithdrawFIFOHead(chain, asset)
	headData, exists, err := p.stateReader.Get(headKey)
	if err != nil {
		return nil, err
	}

	var head uint64 = 1
	if exists && len(headData) > 0 {
		head, _ = strconv.ParseUint(string(headData), 10, 64)
		if head == 0 {
			head = 1
		}
	}

	// 2. 读取最大 seq
	seqKey := keys.KeyFrostWithdrawFIFOSeq(chain, asset)
	seqData, exists, err := p.stateReader.Get(seqKey)
	if err != nil {
		return nil, err
	}
	if !exists || len(seqData) == 0 {
		return nil, nil
	}

	maxSeq, _ := strconv.ParseUint(string(seqData), 10, 64)
	if head > maxSeq {
		return nil, nil
	}

	// 3. 扫描连续的 QUEUED withdraw
	results := make([]*ScanResult, 0, maxCount)
	for seq := head; seq <= maxSeq && len(results) < maxCount; seq++ {
		// 读取 withdraw_id
		indexKey := keys.KeyFrostWithdrawFIFOIndex(chain, asset, seq)
		withdrawID, exists, err := p.stateReader.Get(indexKey)
		if err != nil {
			return nil, err
		}
		if !exists || len(withdrawID) == 0 {
			break // 遇到空位，停止扫描
		}

		// 读取 withdraw 状态
		withdrawKey := keys.KeyFrostWithdraw(string(withdrawID))
		withdrawData, exists, err := p.stateReader.Get(withdrawKey)
		if err != nil {
			return nil, err
		}
		if !exists || len(withdrawData) == 0 {
			break
		}

		state := &pb.FrostWithdrawState{}
		if err := proto.Unmarshal(withdrawData, state); err != nil {
			return nil, err
		}

		// 只收集 QUEUED 状态的 withdraw
		if state.Status != "QUEUED" {
			break // 遇到非 QUEUED，停止扫描（FIFO 要求连续）
		}

		results = append(results, &ScanResult{
			Chain:      chain,
			Asset:      asset,
			WithdrawID: string(withdrawID),
			Seq:        seq,
		})
	}

	return results, nil
}

// planJobForWithdraw 为单个 withdraw 规划 job
func (p *JobWindowPlanner) planJobForWithdraw(scanResult *ScanResult) (*Job, error) {
	// 1. 读取 withdraw 详情
	withdrawKey := keys.KeyFrostWithdraw(scanResult.WithdrawID)
	withdrawData, exists, err := p.stateReader.Get(withdrawKey)
	if err != nil {
		return nil, err
	}
	if !exists || len(withdrawData) == 0 {
		return nil, nil
	}

	state := &pb.FrostWithdrawState{}
	if err := proto.Unmarshal(withdrawData, state); err != nil {
		return nil, err
	}

	// 2. 选择 Vault（需要传入金额以检查余额）
	amount := parseAmount(state.Amount)
	vaultID, keyEpoch, err := p.selectVault(scanResult.Chain, scanResult.Asset, amount)
	if err != nil {
		return nil, err
	}

	// 3. 获取链适配器
	adapter, err := p.adapterFactory.Adapter(scanResult.Chain)
	if err != nil {
		return nil, err
	}

	// 4. 构建模板参数
	params := chainpkg.WithdrawTemplateParams{
		Chain:       scanResult.Chain,
		Asset:       scanResult.Asset,
		VaultID:     vaultID,
		KeyEpoch:    keyEpoch,
		WithdrawIDs: []string{scanResult.WithdrawID},
		Outputs: []chainpkg.WithdrawOutput{
			{
				WithdrawID: scanResult.WithdrawID,
				To:         state.To,
				Amount:     parseAmount(state.Amount),
			},
		},
	}

	// 5. 构建模板
	result, err := adapter.BuildWithdrawTemplate(params)
	if err != nil {
		return nil, err
	}

	// 6. 生成 job_id
	jobID := generateJobID(scanResult.Chain, scanResult.Asset, vaultID, scanResult.Seq, result.TemplateHash, keyEpoch)

	return &Job{
		JobID:        jobID,
		Chain:        scanResult.Chain,
		Asset:        scanResult.Asset,
		VaultID:      vaultID,
		KeyEpoch:     keyEpoch,
		WithdrawIDs:  []string{scanResult.WithdrawID},
		TemplateHash: result.TemplateHash,
		TemplateData: result.TemplateData,
		FirstSeq:     scanResult.Seq,
	}, nil
}

// selectVault 选择 Vault（按 vault_id 升序，检查余额和 lifecycle）
func (p *JobWindowPlanner) selectVault(chain, asset string, amount uint64) (vaultID uint32, keyEpoch uint64, err error) {
	// 读取 VaultConfig 获取 vault_count
	vaultCfgKey := keys.KeyFrostVaultConfig(chain, 0)
	vaultCfgData, exists, err := p.stateReader.Get(vaultCfgKey)
	if err != nil {
		return 0, 1, err
	}

	var vaultCount uint32 = 1 // 默认值
	if exists && len(vaultCfgData) > 0 {
		var cfg pb.FrostVaultConfig
		if err := proto.Unmarshal(vaultCfgData, &cfg); err == nil {
			vaultCount = cfg.VaultCount
			if vaultCount == 0 {
				vaultCount = 1
			}
		}
	}

	// 按 vault_id 升序遍历，选择第一个 ACTIVE 的 Vault
	for id := uint32(0); id < vaultCount; id++ {
		vaultStateKey := keys.KeyFrostVaultState(chain, id)
		vaultStateData, exists, err := p.stateReader.Get(vaultStateKey)
		if err != nil {
			continue
		}
		if !exists || len(vaultStateData) == 0 {
			continue
		}

		var state pb.FrostVaultState
		if err := proto.Unmarshal(vaultStateData, &state); err != nil {
			continue
		}

		// 检查 status
		if state.Status != "ACTIVE" {
			continue
		}

		// 检查该 Vault 的可用余额是否足够
		available, err := p.calculateVaultAvailableBalance(chain, asset, id)
		if err != nil {
			logs.Warn("[JobWindowPlanner] failed to calculate balance for vault %d: %v", id, err)
			continue
		}

		if available >= amount {
			// 余额足够，返回该 Vault
			return id, state.KeyEpoch, nil
		}
		// 余额不足，继续下一个 Vault
	}

	// 如果没有找到 ACTIVE 的 Vault，返回默认值
	return 0, 1, nil
}

// calculateVaultAvailableBalance 计算 Vault 的可用余额
func (p *JobWindowPlanner) calculateVaultAvailableBalance(chain, asset string, vaultID uint32) (uint64, error) {
	if chain == "btc" {
		// BTC：扫描 UTXO 集合，累加未锁定的 UTXO 金额
		return p.calculateBTCBalance(vaultID)
	} else {
		// 账户链/合约链：遍历 FundsLedger lot FIFO，累加金额
		return p.calculateAccountChainBalance(chain, asset, vaultID)
	}
}

// calculateBTCBalance 计算 BTC Vault 的可用余额（未锁定的 UTXO）
func (p *JobWindowPlanner) calculateBTCBalance(vaultID uint32) (uint64, error) {
	utxoPrefix := fmt.Sprintf("v1_frost_btc_utxo_%d_", vaultID)
	var total uint64

	err := p.stateReader.Scan(utxoPrefix, func(k string, v []byte) bool {
		// 解析 UTXO key
		parts := strings.Split(k, "_")
		if len(parts) < 6 {
			return true
		}

		txid := parts[4]
		voutStr := parts[5]
		vout, err := strconv.ParseUint(voutStr, 10, 32)
		if err != nil {
			return true
		}

		// 检查是否已锁定
		lockKey := keys.KeyFrostBtcLockedUtxo(vaultID, txid, uint32(vout))
		_, locked, _ := p.stateReader.Get(lockKey)
		if locked {
			return true // 已锁定，跳过
		}

		// TODO: 从 v 中解析 UTXO 金额
		// 这里简化处理，假设可以从 RechargeRequest 中获取
		// 实际应该从 UTXO 存储的数据中解析
		// total += utxo.Amount

		return true
	})

	if err != nil {
		return 0, err
	}

	return total, nil
}

// calculateAccountChainBalance 计算账户链 Vault 的可用余额（FIFO lot）
func (p *JobWindowPlanner) calculateAccountChainBalance(chain, asset string, vaultID uint32) (uint64, error) {
	// 从 FundsLedger lot FIFO 累加金额
	// 简化实现：扫描所有 lot，累加金额
	// 实际应该从 head 开始按 FIFO 顺序累加
	lotPrefix := fmt.Sprintf("v1_frost_funds_lot_%s_%s_%d_", chain, asset, vaultID)
	var total uint64

	err := p.stateReader.Scan(lotPrefix, func(k string, v []byte) bool {
		// v 中存储的是 request_id，需要从 RechargeRequest 获取金额
		requestID := string(v)
		if requestID == "" {
			return true
		}

		rechargeKey := keys.KeyRechargeRequest(requestID)
		rechargeData, exists, _ := p.stateReader.Get(rechargeKey)
		if !exists {
			return true
		}

		var recharge pb.RechargeRequest
		if err := proto.Unmarshal(rechargeData, &recharge); err != nil {
			return true
		}

		amount, _ := strconv.ParseUint(recharge.Amount, 10, 64)
		total += amount
		return true
	})

	if err != nil {
		return 0, err
	}

	return total, nil
}

// planCompositeJob 规划跨 Vault 组合支付（仅限合约链/账户链）
// 当队首提现无法由单个 Vault 覆盖时，启用跨 Vault 组合模式
func (p *JobWindowPlanner) planCompositeJob(chain, asset string, firstWithdraw *ScanResult, totalAmount uint64) (*Job, error) {
	// 1. 读取 withdraw 详情
	withdrawKey := keys.KeyFrostWithdraw(firstWithdraw.WithdrawID)
	withdrawData, exists, err := p.stateReader.Get(withdrawKey)
	if err != nil || !exists {
		return nil, fmt.Errorf("withdraw not found: %s", firstWithdraw.WithdrawID)
	}

	state := &pb.FrostWithdrawState{}
	if err := proto.Unmarshal(withdrawData, state); err != nil {
		return nil, err
	}

	// 2. 读取 VaultConfig 获取 vault_count
	vaultCfgKey := keys.KeyFrostVaultConfig(chain, 0)
	vaultCfgData, exists, err := p.stateReader.Get(vaultCfgKey)
	if err != nil {
		return nil, err
	}

	var vaultCount uint32 = 1
	if exists && len(vaultCfgData) > 0 {
		var cfg pb.FrostVaultConfig
		if err := proto.Unmarshal(vaultCfgData, &cfg); err == nil {
			vaultCount = cfg.VaultCount
			if vaultCount == 0 {
				vaultCount = 1
			}
		}
	}

	// 3. 按 vault_id 升序累加可用余额直至覆盖总额
	subJobs := make([]*Job, 0)
	var remainingAmount uint64 = totalAmount
	var vaultIDs []uint32

	for id := uint32(0); id < vaultCount && remainingAmount > 0; id++ {
		vaultStateKey := keys.KeyFrostVaultState(chain, id)
		vaultStateData, exists, err := p.stateReader.Get(vaultStateKey)
		if err != nil || !exists || len(vaultStateData) == 0 {
			continue
		}

		var vaultState pb.FrostVaultState
		if err := proto.Unmarshal(vaultStateData, &vaultState); err != nil {
			continue
		}

		// 只考虑 ACTIVE 的 Vault
		if vaultState.Status != "ACTIVE" {
			continue
		}

		// 计算该 Vault 的可用余额
		available, err := p.calculateVaultAvailableBalance(chain, asset, id)
		if err != nil {
			logs.Warn("[JobWindowPlanner] failed to calculate balance for vault %d: %v", id, err)
			continue
		}

		if available == 0 {
			continue
		}

		// 计算该 Vault 需要承担的部分金额
		partialAmount := remainingAmount
		if available < remainingAmount {
			partialAmount = available
		}

		// 创建 SubJob（部分支付）
		subJob, err := p.planSubJob(chain, asset, id, vaultState.KeyEpoch, firstWithdraw, partialAmount)
		if err != nil {
			logs.Warn("[JobWindowPlanner] failed to plan sub job for vault %d: %v", id, err)
			continue
		}

		subJobs = append(subJobs, subJob)
		vaultIDs = append(vaultIDs, id)
		remainingAmount -= partialAmount
	}

	// 检查是否覆盖了全部金额
	if remainingAmount > 0 {
		msg := fmt.Sprintf("insufficient vault balance for composite job: remaining=%d", remainingAmount)
		p.reportTransientLog(firstWithdraw.WithdrawID, "CompositePlan", "WAITING", msg)
		return nil, fmt.Errorf("%s", msg)
	}

	// 4. 生成 CompositeJob ID
	// composite_job_id = H(chain || asset || first_seq || vault_ids[] || "composite")
	compositeJobID := generateCompositeJobID(chain, asset, firstWithdraw.Seq, vaultIDs)

	// 5. 创建 CompositeJob
	compositeJob := &Job{
		JobID:       compositeJobID,
		Chain:       chain,
		Asset:       asset,
		VaultID:     vaultIDs[0], // 使用第一个 Vault ID 作为主 Vault（兼容性）
		KeyEpoch:    subJobs[0].KeyEpoch,
		WithdrawIDs: []string{firstWithdraw.WithdrawID},
		FirstSeq:    firstWithdraw.Seq,
		IsComposite: true,
		SubJobs:     subJobs,
		// CompositeJob 的 TemplateHash 和 TemplateData 为空，由 SubJobs 提供
	}

	logs.Info("[JobWindowPlanner] planned composite job %s with %d sub jobs for withdraw %s",
		compositeJobID, len(subJobs), firstWithdraw.WithdrawID)

	return compositeJob, nil
}

// planSubJob 规划 SubJob（单个 Vault 的部分支付）
func (p *JobWindowPlanner) planSubJob(chain, asset string, vaultID uint32, keyEpoch uint64, withdraw *ScanResult, partialAmount uint64) (*Job, error) {
	// 读取 withdraw 详情
	withdrawKey := keys.KeyFrostWithdraw(withdraw.WithdrawID)
	withdrawData, exists, err := p.stateReader.Get(withdrawKey)
	if err != nil || !exists {
		return nil, fmt.Errorf("withdraw not found: %s", withdraw.WithdrawID)
	}

	state := &pb.FrostWithdrawState{}
	if err := proto.Unmarshal(withdrawData, state); err != nil {
		return nil, err
	}

	// 获取链适配器
	adapter, err := p.adapterFactory.Adapter(chain)
	if err != nil {
		return nil, err
	}

	// 构建模板参数（部分金额）
	params := chainpkg.WithdrawTemplateParams{
		Chain:       chain,
		Asset:       asset,
		VaultID:     vaultID,
		KeyEpoch:    keyEpoch,
		WithdrawIDs: []string{withdraw.WithdrawID},
		Outputs: []chainpkg.WithdrawOutput{
			{
				WithdrawID: withdraw.WithdrawID,
				To:         state.To,
				Amount:     partialAmount, // 使用部分金额
			},
		},
	}

	// 构建模板
	result, err := adapter.BuildWithdrawTemplate(params)
	if err != nil {
		return nil, err
	}

	// 生成 sub_job_id
	subJobID := generateJobID(chain, asset, vaultID, withdraw.Seq, result.TemplateHash, keyEpoch) + "_sub"

	return &Job{
		JobID:        subJobID,
		Chain:        chain,
		Asset:        asset,
		VaultID:      vaultID,
		KeyEpoch:     keyEpoch,
		WithdrawIDs:  []string{withdraw.WithdrawID},
		TemplateHash: result.TemplateHash,
		TemplateData: result.TemplateData,
		FirstSeq:     withdraw.Seq,
		IsComposite:  false,
	}, nil
}

// generateCompositeJobID 生成 CompositeJob ID
// composite_job_id = H(chain || asset || first_seq || vault_ids[] || "composite")
func generateCompositeJobID(chain, asset string, firstSeq uint64, vaultIDs []uint32) string {
	data := chain + "|" + asset + "|" + strconv.FormatUint(firstSeq, 10) + "|"
	for _, id := range vaultIDs {
		data += strconv.FormatUint(uint64(id), 10) + ","
	}
	data += "|composite"
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// reportTransientLog 记录临时日志（针对未生成 Job 的 withdraw）
func (p *JobWindowPlanner) reportTransientLog(withdrawID, step, status, message string) {
	if p.transientLogs == nil {
		p.transientLogs = make(map[string][]*pb.FrostPlanningLog)
	}
	p.transientLogs[withdrawID] = append(p.transientLogs[withdrawID], &pb.FrostPlanningLog{
		Step:      step,
		Status:    status,
		Message:   message,
		Timestamp: uint64(time.Now().UnixMilli()),
	})
}
