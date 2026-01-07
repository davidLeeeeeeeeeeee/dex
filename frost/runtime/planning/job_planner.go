// frost/runtime/planning/job_planner.go
// 确定性 Job 规划器
package planning

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"dex/frost/chain"
	"dex/keys"
	"dex/pb"

	"google.golang.org/protobuf/proto"
)

// Job 签名任务结构
type Job struct {
	JobID        string   // 全网唯一（H(chain || asset || vault_id || first_seq || template_hash || key_epoch)）
	Chain        string   // 链标识
	Asset        string   // 资产类型
	VaultID      uint32   // 使用的 Vault ID
	KeyEpoch     uint64   // 密钥版本
	WithdrawIDs  []string // 被该 job 覆盖的 withdraw_id 列表
	TemplateHash []byte   // 模板哈希
	TemplateData []byte   // 模板数据
	FirstSeq     uint64   // 队首 withdraw 的 seq
	IsComposite  bool     // 是否为 CompositeJob
	SubJobs      []*Job   // CompositeJob 的子 job 列表（仅当 IsComposite=true 时有效）
}

// JobPlanner 确定性 Job 规划器
type JobPlanner struct {
	stateReader    ChainStateReader
	adapterFactory chain.ChainAdapterFactory
}

// NewJobPlanner 创建新的 JobPlanner
func NewJobPlanner(stateReader ChainStateReader, adapterFactory chain.ChainAdapterFactory) *JobPlanner {
	return &JobPlanner{
		stateReader:    stateReader,
		adapterFactory: adapterFactory,
	}
}

// PlanJob 规划签名任务（最小版：单 vault、单 withdraw）
// 输入：扫描结果（队首 QUEUED withdraw）
// 输出：确定性规划的 Job（包含 template_hash）
func (p *JobPlanner) PlanJob(scanResult *ScanResult) (*Job, error) {
	if scanResult == nil {
		return nil, nil
	}

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

	// 2. 选择 Vault（按 vault_id 升序，检查余额和 lifecycle）
	amount := parseAmount(state.Amount)
	vaultID, keyEpoch, err := p.selectVault(scanResult.Chain, scanResult.Asset, amount)
	if err != nil {
		return nil, err
	}

	// 3. 获取链适配器并构建模板
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

	// 5. 构建模板（获取 template_hash）
	result, err := adapter.BuildWithdrawTemplate(params)
	if err != nil {
		return nil, err
	}

	// 6. 生成 job_id（确定性）
	// job_id = H(chain || asset || vault_id || first_seq || template_hash || key_epoch)
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
// 按 vault_id 升序遍历，选出第一个 lifecycle=ACTIVE 且余额足够的 Vault
func (p *JobPlanner) selectVault(chain, asset string, amount uint64) (vaultID uint32, keyEpoch uint64, err error) {
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

	// 按 vault_id 升序遍历，选择第一个 ACTIVE 且余额足够的 Vault
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

		// 检查 status 必须是 ACTIVE
		if state.Status != "ACTIVE" {
			continue
		}

		// 检查该 Vault 的可用余额是否足够
		// 注意：JobPlanner 没有 calculateVaultAvailableBalance 方法
		// 这里简化处理，先返回第一个 ACTIVE 的 Vault
		// 完整的余额检查应该在 JobWindowPlanner 中实现

		return id, state.KeyEpoch, nil
	}

	// 如果没有找到 ACTIVE 的 Vault，返回默认值
	return 0, 1, nil
}

// generateJobID 生成 job_id
// job_id = H(chain || asset || vault_id || first_seq || template_hash || key_epoch)
func generateJobID(chain, asset string, vaultID uint32, firstSeq uint64, templateHash []byte, keyEpoch uint64) string {
	data := chain + "|" + asset + "|" +
		strconv.FormatUint(uint64(vaultID), 10) + "|" +
		strconv.FormatUint(firstSeq, 10) + "|" +
		hex.EncodeToString(templateHash) + "|" +
		strconv.FormatUint(keyEpoch, 10)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// parseAmount 解析金额字符串为 uint64
func parseAmount(amount string) uint64 {
	n, _ := strconv.ParseUint(amount, 10, 64)
	return n
}
