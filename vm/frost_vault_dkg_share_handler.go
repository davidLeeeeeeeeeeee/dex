// vm/frost_vault_dkg_share_handler.go
// FrostVaultDkgShareTx handler: 加密 share 上链存储

package vm

import (
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"fmt"
)

// HandleFrostVaultDkgShareTx 处理加密 share 提交
// 校验：
// 1. dkg_status == Sharing
// 2. dealer_id / receiver_id 必须属于该 Vault 的 new_committee_members[]
// 3. 同一 (chain, vault_id, epoch_id, dealer_id, receiver_id) 只能登记一次
func (e *Executor) HandleFrostVaultDkgShareTx(sender string, tx *pb.FrostVaultDkgShareTx, height uint64) error {
	chain := tx.Chain
	vaultID := tx.VaultId
	epochID := tx.EpochId
	dealerID := tx.DealerId
	receiverID := tx.ReceiverId

	logs.Debug("[DKGShare] sender=%s chain=%s vault=%d epoch=%d dealer=%s receiver=%s",
		sender, chain, vaultID, epochID, dealerID, receiverID)

	// 验证 sender 是 dealer
	if sender != dealerID {
		return fmt.Errorf("DKGShare: sender %s != dealer_id %s", sender, dealerID)
	}

	// 1. 检查是否已存在（幂等性）
	existingKey := keys.KeyFrostVaultDkgShare(chain, vaultID, epochID, dealerID, receiverID)
	existing, err := e.DB.GetFrostDkgShare(existingKey)
	if err == nil && existing != nil {
		logs.Debug("[DKGShare] already submitted, idempotent")
		return nil // 幂等
	}

	// 2. 验证 VaultTransitionState 存在且状态正确
	transitionKey := keys.KeyFrostVaultTransition(chain, vaultID, epochID)
	transition, err := e.DB.GetFrostVaultTransition(transitionKey)
	if err != nil {
		return fmt.Errorf("DKGShare: transition not found: %w", err)
	}

	// 3. 检查 DKG 状态
	if transition.DkgStatus != DKGStatusSharing && transition.DkgStatus != DKGStatusCommitting &&
		transition.DkgStatus != DKGStatusResolving && transition.DkgStatus != DKGStatusNotStarted {
		// 如果 DKG 已经完成，忽略这笔交易
		logs.Warn("[DKGShare] skipping late share tx from %s: dkg_status=%s", sender, transition.DkgStatus)
		return nil
	}

	// 如果当前是 COMMITTING 或 NOT_STARTED，则在接收到第一个 ShareTx 时推进到 SHARING 状态
	if transition.DkgStatus == DKGStatusCommitting || transition.DkgStatus == DKGStatusNotStarted {
		transition.DkgStatus = DKGStatusSharing
		if err := e.DB.SetFrostVaultTransition(transitionKey, transition); err != nil {
			return fmt.Errorf("DKGShare: failed to update transition status: %w", err)
		}
		logs.Info("[DKGShare] transition %s status moved to SHARING", transitionKey)
	}

	// 4. 检查 dealer_id 是否在 new_committee_members 中
	if !isInCommittee(dealerID, transition.NewCommitteeMembers) {
		return fmt.Errorf("DKGShare: dealer %s not in new_committee_members", dealerID)
	}

	// 5. 检查 receiver_id 是否在 new_committee_members 中
	if !isInCommittee(receiverID, transition.NewCommitteeMembers) {
		return fmt.Errorf("DKGShare: receiver %s not in new_committee_members", receiverID)
	}

	// 6. 验证密文不为空
	if len(tx.Ciphertext) == 0 {
		return fmt.Errorf("DKGShare: ciphertext is empty")
	}

	// 7. 存储 share
	share := &pb.FrostVaultDkgShare{
		Chain:       chain,
		VaultId:     vaultID,
		EpochId:     epochID,
		DealerId:    dealerID,
		ReceiverId:  receiverID,
		Ciphertext:  tx.Ciphertext,
		ShareHeight: height,
	}

	if err := e.DB.SetFrostDkgShare(existingKey, share); err != nil {
		return fmt.Errorf("DKGShare: failed to store share: %w", err)
	}

	logs.Debug("[DKGShare] stored share dealer=%s -> receiver=%s", dealerID, receiverID)
	return nil
}
