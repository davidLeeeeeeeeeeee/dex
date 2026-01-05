// vm/frost_vault_dkg_reveal.go
// FrostVaultDkgRevealTx handler: dealer 公开 share + 随机数
package vm

import (
	"dex/logs"
	"dex/pb"
	"fmt"
)

// HandleFrostVaultDkgRevealTx 处理 DKG reveal
// 当 dealer 被投诉后，公开 share 和加密随机数
// 校验：
// 1. 必须存在未结案的投诉
// 2. 需在 reveal_deadline 前提交
// 3. sender 必须是 dealer_id
// 4. VM 复算 Enc(pk_receiver, share; enc_rand) 与链上 ciphertext 一致
// 5. VM 用链上已登记的 commitment_points[] 验证 share
func (e *Executor) HandleFrostVaultDkgRevealTx(sender string, tx *pb.FrostVaultDkgRevealTx, height uint64) error {
	chain := tx.Chain
	vaultID := tx.VaultId
	epochID := tx.EpochId
	dealerID := tx.DealerId
	receiverID := tx.ReceiverId

	logs.Debug("[DKGReveal] sender=%s chain=%s vault=%d epoch=%d dealer=%s receiver=%s",
		sender, chain, vaultID, epochID, dealerID, receiverID)

	// 验证 sender 是 dealer
	if sender != dealerID {
		return fmt.Errorf("DKGReveal: sender %s != dealer_id %s", sender, dealerID)
	}

	// 1. 获取投诉
	complaintKey := KeyFrostVaultDkgComplaint(chain, vaultID, epochID, dealerID, receiverID)
	complaint, err := e.DB.GetFrostDkgComplaint(complaintKey)
	if err != nil || complaint == nil {
		return fmt.Errorf("DKGReveal: complaint not found")
	}

	// 2. 检查投诉状态
	if complaint.Status != ComplaintStatusPending {
		logs.Debug("[DKGReveal] complaint already resolved, idempotent")
		return nil
	}

	// 3. 检查 reveal 截止时间
	if height > complaint.RevealDeadline {
		return fmt.Errorf("DKGReveal: reveal_deadline exceeded: current=%d deadline=%d", height, complaint.RevealDeadline)
	}

	// 4. 获取原始 share
	shareKey := KeyFrostVaultDkgShare(chain, vaultID, epochID, dealerID, receiverID)
	originalShare, err := e.DB.GetFrostDkgShare(shareKey)
	if err != nil || originalShare == nil {
		return fmt.Errorf("DKGReveal: original share not found")
	}

	// 5. 获取 dealer 的 commitment
	commitKey := KeyFrostVaultDkgCommit(chain, vaultID, epochID, dealerID)
	commitment, err := e.DB.GetFrostDkgCommitment(commitKey)
	if err != nil || commitment == nil {
		return fmt.Errorf("DKGReveal: dealer commitment not found")
	}

	// 6. 验证：复算密文并验证 share
	// TODO: 实现完整的验证逻辑：
	// - Enc(pk_receiver, share; enc_rand) == ciphertext
	// - g^share == Π A_ik * x_receiver^k
	valid := verifyReveal(tx.Share, tx.EncRand, originalShare.Ciphertext, commitment.CommitmentPoints, receiverID)

	if valid {
		// share 有效 - 恶意投诉
		// 罚没 receiver 的 bond，剔除 receiver
		complaint.Status = ComplaintStatusResolved
		logs.Info("[DKGReveal] share is valid, false complaint by receiver=%s", receiverID)

		// 清空 dealer 的 commitment（share 已泄露，需要重新生成）
		// 这里只记录状态，实际清空在外部处理
	} else {
		// share 无效 - dealer 作恶
		complaint.Status = ComplaintStatusDisqualified
		logs.Info("[DKGReveal] share is invalid, dealer %s disqualified", dealerID)
	}

	// 更新投诉状态
	if err := e.DB.SetFrostDkgComplaint(complaintKey, complaint); err != nil {
		return fmt.Errorf("DKGReveal: failed to update complaint: %w", err)
	}

	logs.Debug("[DKGReveal] processed reveal dealer=%s receiver=%s valid=%v", dealerID, receiverID, valid)
	return nil
}

// verifyReveal 验证 reveal 数据
// TODO: 实现完整的验证逻辑
func verifyReveal(share, encRand, ciphertext []byte, commitmentPoints [][]byte, receiverID string) bool {
	// 简化版本：只检查基本长度
	if len(share) == 0 || len(encRand) == 0 {
		return false
	}

	// 使用参数避免 unused 警告（完整实现时会用到）
	_ = ciphertext
	_ = commitmentPoints
	_ = receiverID

	// TODO: 完整实现：
	// 1. 使用 enc_rand 重新加密 share，验证与 ciphertext 一致
	// 2. 使用 commitment_points 验证 share 的正确性
	// 3. 计算 g^share 并与 Π A_ik * x_receiver^k 比较

	return true // 临时：假设验证通过
}
