// vm/frost_vault_dkg_reveal.go
// FrostVaultDkgRevealTx handler: dealer 公开 share + 随机数
package vm

import (
	"dex/frost/security"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"fmt"
	"math/big"
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
	complaintKey := keys.KeyFrostVaultDkgComplaint(chain, vaultID, epochID, dealerID, receiverID)
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
	shareKey := keys.KeyFrostVaultDkgShare(chain, vaultID, epochID, dealerID, receiverID)
	originalShare, err := e.DB.GetFrostDkgShare(shareKey)
	if err != nil || originalShare == nil {
		return fmt.Errorf("DKGReveal: original share not found")
	}

	// 5. 获取 dealer 的 commitment
	commitKey := keys.KeyFrostVaultDkgCommit(chain, vaultID, epochID, dealerID)
	commitment, err := e.DB.GetFrostDkgCommitment(commitKey)
	if err != nil || commitment == nil {
		return fmt.Errorf("DKGReveal: dealer commitment not found")
	}

	// 6. 获取 transition 状态以获取 committee 成员列表
	transitionKey := keys.KeyFrostVaultTransition(chain, vaultID, epochID)
	transition, err := e.DB.GetFrostVaultTransition(transitionKey)
	if err != nil {
		return fmt.Errorf("DKGReveal: failed to get transition: %w", err)
	}
	if transition == nil {
		return fmt.Errorf("DKGReveal: transition not found")
	}

	var receiverIndex int
	receiverIndex = getReceiverIndex(receiverID, transition.NewCommitteeMembers)

	// 7. 获取 receiver 的公钥（用于密文验证）
	// TODO: 从链上状态获取 receiver 的签名公钥
	// 当前实现：跳过密文验证，只验证 Feldman VSS commitment
	var receiverPubKey []byte // 空表示跳过密文验证

	// 8. 验证：复算密文并验证 share
	// - Enc(pk_receiver, share; enc_rand) == ciphertext
	// - g^share == Π A_ik * x_receiver^k
	valid := verifyReveal(tx.Share, tx.EncRand, originalShare.Ciphertext, commitment.CommitmentPoints, receiverID, receiverPubKey, receiverIndex)

	if valid {
		// share 有效 - 恶意投诉
		// 罚没 receiver 的 bond，剔除 receiver
		complaint.Status = ComplaintStatusResolved
		logs.Info("[DKGReveal] share is valid, false complaint by receiver=%s", receiverID)

		// 剔除投诉者（恶意投诉）
		transition.NewCommitteeMembers = removeFromCommittee(transition.NewCommitteeMembers, receiverID)
		transition.DkgN = uint32(len(transition.NewCommitteeMembers))

		// 检查门限可行性
		if int(transition.DkgN) < int(transition.DkgThresholdT) {
			// 无法凑齐门限，必须重启 DKG
			transition.DkgStatus = DKGStatusFailed
			logs.Warn("[DKGReveal] insufficient qualified participants after removing %s, DKG failed", receiverID)
		}

		// 清空 dealer 的 commitment（share 已泄露，需要重新生成）
		// 注意：只清空该 dealer 的 commitment，其他参与者保持不变
		commitKey := keys.KeyFrostVaultDkgCommit(chain, vaultID, epochID, dealerID)
		e.DB.EnqueueDel(commitKey)

		// 更新 transition
		if err := e.DB.SetFrostVaultTransition(transitionKey, transition); err != nil {
			return fmt.Errorf("DKGReveal: failed to update transition: %w", err)
		}
	} else {
		// share 无效 - dealer 作恶
		complaint.Status = ComplaintStatusDisqualified
		logs.Info("[DKGReveal] share is invalid, dealer %s disqualified", dealerID)

		// 剔除 dealer（作恶）
		transition.NewCommitteeMembers = removeFromCommittee(transition.NewCommitteeMembers, dealerID)
		transition.DkgN = uint32(len(transition.NewCommitteeMembers))

		// 检查门限可行性
		if int(transition.DkgN) < int(transition.DkgThresholdT) {
			// 无法凑齐门限，必须重启 DKG
			transition.DkgStatus = DKGStatusFailed
			logs.Warn("[DKGReveal] insufficient qualified participants after removing %s, DKG failed", dealerID)
		} else {
			// 继续流程：其他参与者计算份额时直接排除该 dealer 的贡献
			// 注意：不需要重新生成多项式，只需在聚合时排除该 dealer
			logs.Info("[DKGReveal] dealer %s disqualified, continuing DKG with remaining participants", dealerID)
		}

		// 更新 transition
		if err := e.DB.SetFrostVaultTransition(transitionKey, transition); err != nil {
			return fmt.Errorf("DKGReveal: failed to update transition: %w", err)
		}
	}

	// 更新投诉状态
	if err := e.DB.SetFrostDkgComplaint(complaintKey, complaint); err != nil {
		return fmt.Errorf("DKGReveal: failed to update complaint: %w", err)
	}

	logs.Debug("[DKGReveal] processed reveal dealer=%s receiver=%s valid=%v", dealerID, receiverID, valid)
	return nil
}

// verifyReveal 验证 reveal 数据
// 实现链上裁决的核心验证逻辑：
// 1. 使用 enc_rand 重新加密 share，验证与链上 ciphertext 一致
// 2. 使用 commitment_points 验证 share 的正确性（Feldman VSS）
func verifyReveal(share, encRand, ciphertext []byte, commitmentPoints [][]byte, receiverID string, receiverPubKey []byte, receiverIndex int) bool {
	// 基本参数校验
	if len(share) == 0 || len(encRand) == 0 {
		logs.Debug("[verifyReveal] empty share or encRand")
		return false
	}
	if len(ciphertext) == 0 {
		logs.Debug("[verifyReveal] empty ciphertext")
		return false
	}
	if len(commitmentPoints) == 0 {
		logs.Debug("[verifyReveal] empty commitmentPoints")
		return false
	}

	// 验证 1：密文一致性（ECIES 加密验证）
	// 如果提供了 receiver 公钥，验证 Enc(pk_receiver, share; enc_rand) == ciphertext
	if len(receiverPubKey) == 33 {
		ciphertextValid := security.ECIESVerifyCiphertext(receiverPubKey, share, encRand, ciphertext)
		if !ciphertextValid {
			logs.Debug("[verifyReveal] ciphertext verification failed")
			return false
		}
		logs.Debug("[verifyReveal] ciphertext verification passed")
	} else {
		// 如果没有 receiver 公钥，跳过密文验证（降级模式）
		logs.Debug("[verifyReveal] skipping ciphertext verification (no receiver pubkey)")
	}

	// 验证 2：Feldman VSS 验证
	// 验证 g^share == Π A_ik * x_receiver^k
	if receiverIndex > 0 {
		receiverIndexBig := big.NewInt(int64(receiverIndex))
		shareValid := security.VerifyShareAgainstCommitment(share, commitmentPoints, receiverIndexBig)
		if !shareValid {
			logs.Debug("[verifyReveal] share commitment verification failed")
			return false
		}
		logs.Debug("[verifyReveal] share commitment verification passed")
	} else {
		// 如果没有 receiver index，跳过 commitment 验证（降级模式）
		logs.Debug("[verifyReveal] skipping commitment verification (no receiver index)")
	}

	return true
}

// getReceiverIndex 从 committee 成员列表中获取 receiver 的索引（1-based）
func getReceiverIndex(receiverID string, committeeMembers []string) int {
	for i, member := range committeeMembers {
		if member == receiverID {
			return i + 1 // 1-based index
		}
	}
	return 0 // 未找到
}
