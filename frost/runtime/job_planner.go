// frost/runtime/job_planner.go
// 确定性 Job 规划器
package runtime

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

	// 2. 选择 Vault（最小版：按 vault_id 升序选择第一个 ACTIVE 的 Vault）
	vaultID, keyEpoch, err := p.selectVault(scanResult.Chain)
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

// selectVault 选择 Vault（最小版：返回第一个 ACTIVE 的 Vault）
// 按 vault_id 升序遍历，选出第一个 lifecycle=ACTIVE 的 Vault
func (p *JobPlanner) selectVault(chain string) (vaultID uint32, keyEpoch uint64, err error) {
	// 简化版：直接返回 vault_id=0，key_epoch=1
	// 实际实现需要遍历所有 Vault 并检查 lifecycle 和 balance
	// TODO: 完善 Vault 选择逻辑
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
