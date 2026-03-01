// frost/runtime/planning/job_window_planner.go
// Job 窗口规划器：支持 FIFO 批量规划（最多 maxInFlightPerChainAsset 个 job）

package planning

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	chainpkg "dex/frost/chain"
	"dex/keys"
	"dex/logs"
	"dex/pb"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"google.golang.org/protobuf/proto"
)

// JobWindowPlanner Job 窗口规划器
type JobWindowPlanner struct {
	stateReader    ChainStateReader
	adapterFactory chainpkg.ChainAdapterFactory
	maxInFlight    int // 最多并发 job 数（maxInFlightPerChainAsset）
	transientLogs  map[string][]*pb.FrostPlanningLog
	transientCache map[uint32]uint64 // 生命期仅在一帧内（单次 Tick）的 Vault 可用余额缓存
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
	// 清空旧日志，初始化当前帧(Tick)生命周期的可用余额缓存
	p.transientLogs = make(map[string][]*pb.FrostPlanningLog)
	p.transientCache = make(map[uint32]uint64)

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
		vaultID, keyEpoch, err := p.selectVault(chain, asset, totalAmount)
		if err != nil {
			logs.Verbose("[JobWindowPlanner] failed to select vault for batch amount=%d: %v", totalAmount, err)
			for _, wd := range batchWithdraws {
				p.reportTransientLog(wd.WithdrawID, "SelectVault", "FAILED", fmt.Sprintf("Failed to select vault: %v", err))
			}
			continue
		}

		job, err := p.planBTCJob(chain, asset, vaultID, keyEpoch, batchWithdraws, totalAmount)
		if err != nil {
			logs.Debug("[JobWindowPlanner] failed to plan BTC job: %v", err)
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

					// 扣减本地缓存，防止后续的规划仍然觉得余额充足导致双花逻辑上的超采
					withdrawKey := keys.KeyFrostWithdraw(wd.WithdrawID)
					if withdrawData, exists, e := p.stateReader.Get(withdrawKey); e == nil && exists {
						state := &pb.FrostWithdrawState{}
						if proto.Unmarshal(withdrawData, state) == nil {
							p.deductVaultBalance(singleJob.VaultID, parseAmount(state.Amount))
						}
					}
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
			// 批量规划成功后从缓存中扣去透支余额，防止当次Tick重用
			p.deductVaultBalance(vaultID, totalAmount)
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
				p.deductVaultBalance(vaultID, amount) // 当次扫描成功后直接扣除被占用的余额
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

	var fee uint64
	var changeAmount uint64
	changeAddress := ""
	const btcDustLimit uint64 = 546

	// Close the fee/input loop until totalIn can satisfy totalOut+fee(+change).
	for iter := 0; iter < 6; iter++ {
		totalInput := sumUTXOAmount(utxos)

		feeNoChange := p.estimateBTCFee(len(utxos), len(outputs))
		needNoChange := totalAmount + feeNoChange
		if totalInput < needNoChange {
			utxos, err = p.selectBTCUTXOs(chain, vaultID, needNoChange)
			if err != nil {
				return nil, err
			}
			continue
		}

		fee = feeNoChange
		changeAmount = totalInput - totalAmount - feeNoChange

		if changeAmount >= btcDustLimit {
			feeWithChange := p.estimateBTCFee(len(utxos), len(outputs)+1)
			needWithChange := totalAmount + feeWithChange
			if totalInput < needWithChange {
				utxos, err = p.selectBTCUTXOs(chain, vaultID, needWithChange)
				if err != nil {
					return nil, err
				}
				continue
			}

			fee = feeWithChange
			changeAmount = totalInput - totalAmount - feeWithChange

			if changeAmount >= btcDustLimit {
				changeAddress, err = p.getYoungestVaultTreasuryAddress(chain)
				if err != nil || strings.TrimSpace(changeAddress) == "" {
					logs.Warn("[JobWindowPlanner] fallback to no-change because youngest treasury address unavailable: %v", err)
					fee = totalInput - totalAmount
					changeAmount = 0
					changeAddress = ""
				}
			} else {
				// After accounting for an extra change output, change becomes dust.
				fee = totalInput - totalAmount
				changeAmount = 0
			}
		} else {
			// Dust-level remainder is absorbed into fee.
			fee = totalInput - totalAmount
			changeAmount = 0
		}

		break
	}

	totalInputFinal := sumUTXOAmount(utxos)
	if totalInputFinal < totalAmount+fee {
		return nil, fmt.Errorf("insufficient UTXOs after fee planning: need %d, got %d", totalAmount+fee, totalInputFinal)
	}

	// 4. Get chain adapter and build template.
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
		ChangeAddress: changeAddress,
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

type btcUTXOScanStats struct {
	Scanned      uint64
	IndexMissing uint64
	DecodeFailed uint64
	Locked       uint64
	ZeroAmount   uint64
	Selected     uint64
}

type btcUTXOSelectionError struct {
	Code   string
	Reason string

	Chain    string
	VaultID  uint32
	Need     uint64
	Got      uint64
	Head     uint64
	MaxSeq   uint64
	ScanStat btcUTXOScanStats
}

func (e *btcUTXOSelectionError) Error() string {
	return fmt.Sprintf(
		"UTXO selection failed: need=%d got=%d code=%s chain=%s vault=%d head=%d max_seq=%d scanned=%d selected=%d locked=%d missing=%d decode_failed=%d zero_amount=%d reason=%s",
		e.Need, e.Got, e.Code, e.Chain, e.VaultID, e.Head, e.MaxSeq,
		e.ScanStat.Scanned, e.ScanStat.Selected, e.ScanStat.Locked, e.ScanStat.IndexMissing, e.ScanStat.DecodeFailed, e.ScanStat.ZeroAmount,
		e.Reason,
	)
}

func newBTCUTXOSelectionError(
	code, reason, chain string,
	vaultID uint32,
	need, got, head, maxSeq uint64,
	scanStat btcUTXOScanStats,
) error {
	return &btcUTXOSelectionError{
		Code:     code,
		Reason:   reason,
		Chain:    chain,
		VaultID:  vaultID,
		Need:     need,
		Got:      got,
		Head:     head,
		MaxSeq:   maxSeq,
		ScanStat: scanStat,
	}
}

func classifyBTCUTXOInsufficient(got uint64, stat btcUTXOScanStats) (string, string) {
	if got == 0 {
		switch {
		case stat.Scanned == 0:
			return "fifo_window_empty", "no FIFO window scanned"
		case stat.IndexMissing == stat.Scanned:
			return "fifo_index_missing_all", "all FIFO index entries are missing"
		case stat.Locked == stat.Scanned:
			return "all_locked", "all candidate UTXOs are locked"
		case stat.DecodeFailed == stat.Scanned:
			return "decode_failed_all", "all candidate UTXOs failed to decode"
		case stat.ZeroAmount == stat.Scanned:
			return "zero_amount_all", "all candidate UTXOs have zero amount"
		default:
			return "no_spendable_utxo", "no spendable UTXO found in FIFO window"
		}
	}
	if stat.Selected == 0 {
		return "no_spendable_utxo", "no spendable UTXO found in FIFO window"
	}
	return "selected_but_insufficient_amount", "spendable UTXOs exist but total amount is below required amount"
}

// selectBTCUTXOs 选择 BTC UTXO（按 FIFO 索引顺序，不扫描 UTXO 全前缀）
func (p *JobWindowPlanner) selectBTCUTXOs(chain string, vaultID uint32, needAmount uint64) ([]chainpkg.UTXO, error) {
	// 1. 读取 VaultState，构建 Taproot scriptPubKey
	vaultStateKey := keys.KeyFrostVaultState(chain, vaultID)
	vaultStateData, exists, err := p.stateReader.Get(vaultStateKey)
	if err != nil {
		return nil, newBTCUTXOSelectionError(
			"vault_state_read_failed",
			err.Error(),
			chain,
			vaultID,
			needAmount,
			0,
			0,
			0,
			btcUTXOScanStats{},
		)
	}
	if !exists || len(vaultStateData) == 0 {
		return nil, newBTCUTXOSelectionError(
			"vault_state_missing",
			fmt.Sprintf("vault state not found: key=%s", vaultStateKey),
			chain,
			vaultID,
			needAmount,
			0,
			0,
			0,
			btcUTXOScanStats{},
		)
	}

	var vaultState pb.FrostVaultState
	if err := proto.Unmarshal(vaultStateData, &vaultState); err != nil {
		return nil, newBTCUTXOSelectionError(
			"vault_state_decode_failed",
			err.Error(),
			chain,
			vaultID,
			needAmount,
			0,
			0,
			0,
			btcUTXOScanStats{},
		)
	}

	var scriptPubKey []byte
	if len(vaultState.GroupPubkey) == 32 {
		scriptPubKey = make([]byte, 34)
		scriptPubKey[0] = 0x51
		scriptPubKey[1] = 0x20
		copy(scriptPubKey[2:], vaultState.GroupPubkey)
	}

	// 2. 按 head/seq 从 FIFO 索引顺序读取
	headKey := keys.KeyFrostBtcUtxoFIFOHead(vaultID)
	seqKey := keys.KeyFrostBtcUtxoFIFOSeq(vaultID)

	head := uint64(1)
	if headData, headExists, e := p.stateReader.Get(headKey); e != nil {
		return nil, newBTCUTXOSelectionError(
			"fifo_head_read_failed",
			e.Error(),
			chain,
			vaultID,
			needAmount,
			0,
			0,
			0,
			btcUTXOScanStats{},
		)
	} else if headExists && len(headData) > 0 {
		parsed, pe := strconv.ParseUint(string(headData), 10, 64)
		if pe != nil {
			return nil, newBTCUTXOSelectionError(
				"fifo_head_invalid",
				fmt.Sprintf("invalid head value=%q: %v", string(headData), pe),
				chain,
				vaultID,
				needAmount,
				0,
				0,
				0,
				btcUTXOScanStats{},
			)
		}
		if parsed == 0 {
			return nil, newBTCUTXOSelectionError(
				"fifo_head_invalid",
				fmt.Sprintf("invalid head value=%q: must be >= 1", string(headData)),
				chain,
				vaultID,
				needAmount,
				0,
				0,
				0,
				btcUTXOScanStats{},
			)
		}
		head = parsed
	}

	seqData, exists, err := p.stateReader.Get(seqKey)
	if err != nil {
		return nil, newBTCUTXOSelectionError(
			"fifo_seq_read_failed",
			err.Error(),
			chain,
			vaultID,
			needAmount,
			0,
			head,
			0,
			btcUTXOScanStats{},
		)
	}
	if !exists || len(seqData) == 0 {
		return nil, newBTCUTXOSelectionError(
			"fifo_seq_missing",
			fmt.Sprintf("FIFO seq key missing: key=%s", seqKey),
			chain,
			vaultID,
			needAmount,
			0,
			head,
			0,
			btcUTXOScanStats{},
		)
	}
	maxSeq, err := strconv.ParseUint(string(seqData), 10, 64)
	if err != nil {
		return nil, newBTCUTXOSelectionError(
			"fifo_seq_invalid",
			fmt.Sprintf("invalid seq value=%q: %v", string(seqData), err),
			chain,
			vaultID,
			needAmount,
			0,
			head,
			0,
			btcUTXOScanStats{},
		)
	}
	if maxSeq == 0 {
		return nil, newBTCUTXOSelectionError(
			"fifo_seq_zero",
			fmt.Sprintf("invalid seq value=%q: must be >= 1", string(seqData)),
			chain,
			vaultID,
			needAmount,
			0,
			head,
			maxSeq,
			btcUTXOScanStats{},
		)
	}
	if head > maxSeq {
		return nil, newBTCUTXOSelectionError(
			"fifo_head_ahead",
			"FIFO head is ahead of max seq",
			chain,
			vaultID,
			needAmount,
			0,
			head,
			maxSeq,
			btcUTXOScanStats{},
		)
	}

	selected := make([]chainpkg.UTXO, 0)
	var total uint64
	scanStat := btcUTXOScanStats{}
	for seq := head; seq <= maxSeq && total < needAmount; seq++ {
		scanStat.Scanned++
		indexKey := keys.KeyFrostBtcUtxoFIFOIndex(vaultID, seq)
		utxoData, idxExists, e := p.stateReader.Get(indexKey)
		if e != nil {
			return nil, newBTCUTXOSelectionError(
				"fifo_index_read_failed",
				fmt.Sprintf("failed to read FIFO index key=%s: %v", indexKey, e),
				chain,
				vaultID,
				needAmount,
				total,
				head,
				maxSeq,
				scanStat,
			)
		}
		if !idxExists || len(utxoData) == 0 {
			scanStat.IndexMissing++
			continue
		}

		var protoUtxo pb.FrostUtxo
		if e := proto.Unmarshal(utxoData, &protoUtxo); e != nil {
			scanStat.DecodeFailed++
			continue
		}

		lockKey := keys.KeyFrostBtcLockedUtxo(vaultID, protoUtxo.Txid, protoUtxo.Vout)
		if lockVal, locked, e := p.stateReader.Get(lockKey); e != nil {
			return nil, newBTCUTXOSelectionError(
				"utxo_lock_read_failed",
				fmt.Sprintf("failed to read lock key=%s: %v", lockKey, e),
				chain,
				vaultID,
				needAmount,
				total,
				head,
				maxSeq,
				scanStat,
			)
		} else if locked && len(lockVal) > 0 {
			scanStat.Locked++
			continue
		}

		amount := parseAmount(protoUtxo.Amount)
		if amount == 0 {
			scanStat.ZeroAmount++
			continue
		}

		selected = append(selected, chainpkg.UTXO{
			TxID:          protoUtxo.Txid,
			Vout:          protoUtxo.Vout,
			Amount:        amount,
			ScriptPubKey:  scriptPubKey,
			ConfirmHeight: protoUtxo.FinalizeHeight,
		})
		scanStat.Selected++
		total += amount
	}

	if total < needAmount {
		code, reason := classifyBTCUTXOInsufficient(total, scanStat)
		return nil, newBTCUTXOSelectionError(
			code,
			reason,
			chain,
			vaultID,
			needAmount,
			total,
			head,
			maxSeq,
			scanStat,
		)
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

func sumUTXOAmount(utxos []chainpkg.UTXO) uint64 {
	var total uint64
	for _, utxo := range utxos {
		total += utxo.Amount
	}
	return total
}

func (p *JobWindowPlanner) getYoungestVaultTreasuryAddress(chain string) (string, error) {
	youngestVaultID, err := p.selectYoungestActiveVaultID(chain)
	if err != nil {
		return "", err
	}

	vaultStateKey := keys.KeyFrostVaultState(chain, youngestVaultID)
	vaultStateData, exists, err := p.stateReader.Get(vaultStateKey)
	if err != nil {
		return "", err
	}
	if !exists || len(vaultStateData) == 0 {
		return "", fmt.Errorf("vault state not found for youngest vault: chain=%s vault=%d", chain, youngestVaultID)
	}

	var state pb.FrostVaultState
	if err := proto.Unmarshal(vaultStateData, &state); err != nil {
		return "", err
	}
	xOnly, err := normalizeTaprootXOnlyPubKey(state.GroupPubkey)
	if err != nil {
		return "", fmt.Errorf("invalid group pubkey for youngest vault: chain=%s vault=%d err=%w", chain, youngestVaultID, err)
	}
	addr, err := btcTaprootAddressFromXOnly(xOnly, btcNetParamsForChain(chain))
	if err != nil {
		return "", fmt.Errorf("derive taproot address for youngest vault failed: chain=%s vault=%d err=%w", chain, youngestVaultID, err)
	}
	return addr, nil
}

func normalizeTaprootXOnlyPubKey(groupPubkey []byte) ([]byte, error) {
	switch len(groupPubkey) {
	case 32:
		xOnly := make([]byte, 32)
		copy(xOnly, groupPubkey)
		return xOnly, nil
	case 33:
		if groupPubkey[0] != 0x02 && groupPubkey[0] != 0x03 {
			return nil, fmt.Errorf("invalid compressed pubkey prefix: 0x%x", groupPubkey[0])
		}
		xOnly := make([]byte, 32)
		copy(xOnly, groupPubkey[1:])
		return xOnly, nil
	default:
		return nil, fmt.Errorf("unsupported group pubkey length: %d", len(groupPubkey))
	}
}

func btcTaprootAddressFromXOnly(xOnly []byte, netParams *chaincfg.Params) (string, error) {
	addr, err := btcutil.NewAddressTaproot(xOnly, netParams)
	if err != nil {
		return "", err
	}
	return addr.EncodeAddress(), nil
}

func btcNetParamsForChain(chain string) *chaincfg.Params {
	lower := strings.ToLower(strings.TrimSpace(chain))
	switch lower {
	case "btc_testnet", "btc-testnet", "btctestnet", "testnet":
		return &chaincfg.TestNet3Params
	case "btc_regtest", "btc-regtest", "btcregtest", "regtest":
		return &chaincfg.RegressionNetParams
	case "btc_signet", "btc-signet", "btcsignet", "signet":
		return &chaincfg.SigNetParams
	default:
		return &chaincfg.MainNetParams
	}
}

func (p *JobWindowPlanner) selectYoungestActiveVaultID(chain string) (uint32, error) {
	vaultCfgKey := keys.KeyFrostVaultConfig(chain, 0)
	vaultCfgData, exists, err := p.stateReader.Get(vaultCfgKey)
	if err != nil {
		return 0, err
	}

	vaultCount := uint32(1)
	if exists && len(vaultCfgData) > 0 {
		var cfg pb.FrostVaultConfig
		if err := proto.Unmarshal(vaultCfgData, &cfg); err == nil && cfg.VaultCount > 0 {
			vaultCount = cfg.VaultCount
		}
	}

	found := false
	var youngestID uint32
	var bestEpoch uint64
	var bestActiveSince uint64

	for id := uint32(0); id < vaultCount; id++ {
		vaultStateKey := keys.KeyFrostVaultState(chain, id)
		vaultStateData, stateExists, e := p.stateReader.Get(vaultStateKey)
		if e != nil || !stateExists || len(vaultStateData) == 0 {
			continue
		}

		var state pb.FrostVaultState
		if e := proto.Unmarshal(vaultStateData, &state); e != nil {
			continue
		}
		if state.Status != "ACTIVE" {
			continue
		}

		if !found ||
			state.KeyEpoch > bestEpoch ||
			(state.KeyEpoch == bestEpoch && state.ActiveSinceHeight > bestActiveSince) ||
			(state.KeyEpoch == bestEpoch && state.ActiveSinceHeight == bestActiveSince && id > youngestID) {
			found = true
			youngestID = id
			bestEpoch = state.KeyEpoch
			bestActiveSince = state.ActiveSinceHeight
		}
	}

	if !found {
		return 0, fmt.Errorf("no ACTIVE vault found for chain=%s", chain)
	}
	return youngestID, nil
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
	// BTC single-withdraw fallback must still use BTC-specific planning (inputs/fee/change).
	if scanResult.Chain == "btc" {
		return p.planBTCJob(scanResult.Chain, scanResult.Asset, vaultID, keyEpoch, []*ScanResult{scanResult}, amount)
	}

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
	activeCount := 0
	var maxAvailable uint64
	var maxAvailableVault uint32
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
		activeCount++

		// 检查该 Vault 的可用余额是否足够
		available, err := p.calculateVaultAvailableBalance(chain, asset, id)
		if err != nil {
			logs.Warn("[JobWindowPlanner] failed to calculate balance for vault %d: %v", id, err)
			continue
		}
		if available > maxAvailable || activeCount == 1 {
			maxAvailable = available
			maxAvailableVault = id
		}

		if available >= amount {
			// 余额足够，返回该 Vault
			return id, state.KeyEpoch, nil
		}
		// 余额不足，继续下一个 Vault
	}

	if activeCount == 0 {
		return 0, 1, fmt.Errorf("select vault failed: no ACTIVE vault for chain=%s (vault_count=%d)", chain, vaultCount)
	}
	return 0, 1, fmt.Errorf(
		"select vault failed: insufficient balance across ACTIVE vaults for %s/%s need=%d max_available=%d max_vault=%d active_count=%d",
		chain, asset, amount, maxAvailable, maxAvailableVault, activeCount,
	)
}

// calculateVaultAvailableBalance 计算 Vault 的可用余额
func (p *JobWindowPlanner) calculateVaultAvailableBalance(chain, asset string, vaultID uint32) (uint64, error) {
	// 帧级缓存（单次 Tick 有效）
	if p.transientCache != nil {
		if cachedBal, ok := p.transientCache[vaultID]; ok {
			return cachedBal, nil
		}
	}

	// 读取聚合可用余额，避免运行时扫描 lot/utxo
	balanceKey := keys.KeyFrostVaultAvailableBalance(chain, asset, vaultID)
	balanceData, exists, err := p.stateReader.Get(balanceKey)
	if err != nil {
		return 0, err
	}
	if !exists || len(balanceData) == 0 {
		if p.transientCache != nil {
			p.transientCache[vaultID] = 0
		}
		return 0, nil
	}

	balance := parseAmount(string(balanceData))
	if p.transientCache != nil {
		p.transientCache[vaultID] = balance
	}
	return balance, nil
}

func (p *JobWindowPlanner) deductVaultBalance(vaultID uint32, amount uint64) {
	if p.transientCache != nil {
		if bal, ok := p.transientCache[vaultID]; ok {
			if bal >= amount {
				p.transientCache[vaultID] = bal - amount
			} else {
				p.transientCache[vaultID] = 0 // 防御性归零
			}
		}
	}
}

// calculateBTCBalance 计算 BTC Vault 的可用余额（未锁定的 UTXO）
func (p *JobWindowPlanner) calculateBTCBalance(vaultID uint32) (uint64, error) {
	utxoPrefix := fmt.Sprintf("v1_frost_btc_utxo_%d_", vaultID)
	var total uint64

	type utxoCandidate struct {
		value   []byte
		lockKey string
	}
	candidates := make([]utxoCandidate, 0)
	lockKeys := make([]string, 0)

	err := p.stateReader.Scan(utxoPrefix, func(k string, v []byte) bool {
		parts := strings.Split(k, "_")
		if len(parts) < 6 {
			return true
		}

		txid := parts[4]
		vout, err := strconv.ParseUint(parts[5], 10, 32)
		if err != nil {
			return true
		}

		lk := keys.KeyFrostBtcLockedUtxo(vaultID, txid, uint32(vout))
		lockKeys = append(lockKeys, lk)

		valueCopy := append([]byte(nil), v...)
		candidates = append(candidates, utxoCandidate{value: valueCopy, lockKey: lk})
		return true
	})

	if err != nil {
		return 0, err
	}

	lockedMap, err := getMany(p.stateReader, lockKeys)
	if err != nil {
		return 0, err
	}

	for _, c := range candidates {
		if _, locked := lockedMap[c.lockKey]; locked {
			continue // 已锁定，跳过
		}
		var protoUtxo pb.FrostUtxo
		if err := proto.Unmarshal(c.value, &protoUtxo); err == nil {
			total += parseAmount(protoUtxo.Amount)
		}
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
	rechargeKeys := make([]string, 0)

	err := p.stateReader.Scan(lotPrefix, func(k string, v []byte) bool {
		// v 中存储的是 request_id，需要从 RechargeRequest 获取金额
		requestID := string(v)
		if requestID == "" {
			return true
		}
		rechargeKeys = append(rechargeKeys, keys.KeyRechargeRequest(requestID))
		return true
	})

	if err != nil {
		return 0, err
	}

	rechargeValues, err := getMany(p.stateReader, rechargeKeys)
	if err != nil {
		return 0, err
	}

	amountCache := make(map[string]uint64, len(rechargeValues))
	for _, rechargeKey := range rechargeKeys {
		rechargeData, exists := rechargeValues[rechargeKey]
		if !exists || len(rechargeData) == 0 {
			continue
		}
		amount, cached := amountCache[rechargeKey]
		if !cached {
			var recharge pb.RechargeRequest
			if err := proto.Unmarshal(rechargeData, &recharge); err != nil {
				continue
			}
			parsed, err := strconv.ParseUint(recharge.Amount, 10, 64)
			if err != nil {
				continue
			}
			amount = parsed
			amountCache[rechargeKey] = amount
		}
		total += amount
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

		p.deductVaultBalance(id, partialAmount) // 从本地临时缓存中安全扣除

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
