// frost/runtime/planning/job_window_planner.go
// Job 窗口规划器：支持 FIFO 批量规划（最多 maxInFlightPerChainAsset 个 job）

package planning

import (
	"dex/frost/chain"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"strconv"

	"google.golang.org/protobuf/proto"
)

// JobWindowPlanner Job 窗口规划器
type JobWindowPlanner struct {
	stateReader    ChainStateReader
	adapterFactory chain.ChainAdapterFactory
	maxInFlight    int // 最多并发 job 数（maxInFlightPerChainAsset）
}

// NewJobWindowPlanner 创建新的 Job 窗口规划器
func NewJobWindowPlanner(stateReader ChainStateReader, adapterFactory chain.ChainAdapterFactory, maxInFlight int) *JobWindowPlanner {
	if maxInFlight <= 0 {
		maxInFlight = 1 // 默认值
	}
	return &JobWindowPlanner{
		stateReader:    stateReader,
		adapterFactory: adapterFactory,
		maxInFlight:    maxInFlight,
	}
}

// PlanJobWindow 规划 Job 窗口（最多 maxInFlight 个 job）
// 返回：Job 列表（按 FIFO 顺序）
func (p *JobWindowPlanner) PlanJobWindow(chain, asset string) ([]*Job, error) {
	// 1. 扫描连续的 QUEUED withdraw（最多 maxInFlight 个）
	withdraws, err := p.scanQueuedWithdraws(chain, asset, p.maxInFlight)
	if err != nil {
		return nil, err
	}
	if len(withdraws) == 0 {
		return nil, nil
	}

	// 2. 为每个 withdraw 规划 job（确定性）
	jobs := make([]*Job, 0, len(withdraws))
	for _, wd := range withdraws {
		job, err := p.planJobForWithdraw(wd)
		if err != nil {
			logs.Warn("[JobWindowPlanner] failed to plan job for withdraw %s: %v", wd.WithdrawID, err)
			continue
		}
		if job != nil {
			jobs = append(jobs, job)
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

	// 2. 选择 Vault
	vaultID, keyEpoch, err := p.selectVault(scanResult.Chain)
	if err != nil {
		return nil, err
	}

	// 3. 获取链适配器
	adapter, err := p.adapterFactory.Adapter(scanResult.Chain)
	if err != nil {
		return nil, err
	}

	// 4. 构建模板参数
	params := chain.WithdrawTemplateParams{
		Chain:       scanResult.Chain,
		Asset:       scanResult.Asset,
		VaultID:     vaultID,
		KeyEpoch:    keyEpoch,
		WithdrawIDs: []string{scanResult.WithdrawID},
		Outputs: []chain.WithdrawOutput{
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
func (p *JobWindowPlanner) selectVault(chain string) (vaultID uint32, keyEpoch uint64, err error) {
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

		// 找到第一个 ACTIVE 的 Vault
		return id, state.KeyEpoch, nil
	}

	// 如果没有找到 ACTIVE 的 Vault，返回默认值
	return 0, 1, nil
}
