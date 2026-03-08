// frost/runtime/planning/job_window_planner.go
// Job 窗口规划器：支持 FIFO 批量规划（最多 maxInFlightPerChainAsset 个 job）

package planning

import (
	chainpkg "dex/frost/chain"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"fmt"
	"strconv"
	"time"

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

// planBTCJobWindow BTC 批量规划（每个 job 使用单 UTXO 保守策略）
func (p *JobWindowPlanner) planBTCJobWindow(chain, asset string, withdraws []*ScanResult) ([]*Job, error) {
	jobs := make([]*Job, 0)
	remaining := withdraws

	for len(remaining) > 0 {
		// 选择 vault
		vaultID, keyEpoch, err := p.selectVault(chain, asset, 0)
		if err != nil {
			for _, wd := range remaining {
				p.reportTransientLog(wd.WithdrawID, "SelectVault", "FAILED", fmt.Sprintf("Failed to select vault: %v", err))
			}
			break
		}

		// planBTCJob 内部选单 UTXO + 贪心匹配 withdraw
		job, err := p.planBTCJob(chain, asset, vaultID, keyEpoch, remaining)
		if err != nil {
			for _, wd := range remaining {
				p.reportTransientLog(wd.WithdrawID, "PlanBTCJob", "FAILED", fmt.Sprintf("Failed: %v", err))
			}
			break
		}
		if job == nil || len(job.WithdrawIDs) == 0 {
			break
		}

		jobs = append(jobs, job)

		// 从 remaining 中移除已规划的 withdraw
		consumed := make(map[string]bool, len(job.WithdrawIDs))
		for _, wid := range job.WithdrawIDs {
			consumed[wid] = true
		}
		newRemaining := make([]*ScanResult, 0, len(remaining))
		for _, wd := range remaining {
			if !consumed[wd.WithdrawID] {
				newRemaining = append(newRemaining, wd)
			}
		}
		remaining = newRemaining
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
		return p.planBTCJob(scanResult.Chain, scanResult.Asset, vaultID, keyEpoch, []*ScanResult{scanResult})
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
