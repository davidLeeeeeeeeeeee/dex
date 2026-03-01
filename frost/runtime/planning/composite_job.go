// frost/runtime/planning/composite_job.go
// CompositeJob（跨 Vault 组合支付）规划

package planning

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	chainpkg "dex/frost/chain"
	"dex/keys"
	"dex/logs"
	"dex/pb"

	"google.golang.org/protobuf/proto"
)

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
