// vm/frost_vault_dkg_complaint.go
// FrostVaultDkgComplaintTx handler: 链上裁决/举证
package vm

import (
	"dex/logs"
	"dex/pb"
	"fmt"
)

// 投诉状态常量
const (
	ComplaintStatusPending      = "PENDING"      // 待处理
	ComplaintStatusResolved     = "RESOLVED"     // 已解决（share 有效）
	ComplaintStatusDisqualified = "DISQUALIFIED" // 已失格（dealer 作恶）
)

// RevealDeadlineBlocks reveal 截止区块数
const RevealDeadlineBlocks = 100

// HandleFrostVaultDkgComplaintTx 处理 DKG 投诉
// 当 receiver 认为收到的 share 无效时，发起投诉
// 校验：
// 1. dkg_status ∈ {Sharing, Resolving}
// 2. 必须存在对应的 DkgShareTx（密文已上链）
// 3. sender 必须是 receiver_id
// 4. 同一 (chain, vault_id, epoch_id, dealer_id, receiver_id) 只能投诉一次
func (e *Executor) HandleFrostVaultDkgComplaintTx(sender string, tx *pb.FrostVaultDkgComplaintTx, height uint64) error {
	chain := tx.Chain
	vaultID := tx.VaultId
	epochID := tx.EpochId
	dealerID := tx.DealerId
	receiverID := tx.ReceiverId

	logs.Debug("[DKGComplaint] sender=%s chain=%s vault=%d epoch=%d dealer=%s receiver=%s",
		sender, chain, vaultID, epochID, dealerID, receiverID)

	// 验证 sender 是 receiver
	if sender != receiverID {
		return fmt.Errorf("DKGComplaint: sender %s != receiver_id %s", sender, receiverID)
	}

	// 1. 检查是否已存在投诉（幂等性）
	complaintKey := KeyFrostVaultDkgComplaint(chain, vaultID, epochID, dealerID, receiverID)
	existing, err := e.DB.GetFrostDkgComplaint(complaintKey)
	if err == nil && existing != nil {
		logs.Debug("[DKGComplaint] already submitted, idempotent")
		return nil
	}

	// 2. 验证 VaultTransitionState 存在且状态正确
	transitionKey := KeyFrostVaultTransition(chain, vaultID, epochID)
	transition, err := e.DB.GetFrostVaultTransition(transitionKey)
	if err != nil {
		return fmt.Errorf("DKGComplaint: transition not found: %w", err)
	}

	// 3. 检查 DKG 状态
	if transition.DkgStatus != DKGStatusSharing && transition.DkgStatus != DKGStatusResolving {
		return fmt.Errorf("DKGComplaint: invalid dkg_status=%s", transition.DkgStatus)
	}

	// 4. 检查 dealer_id 是否在 new_committee_members 中
	if !isInCommittee(dealerID, transition.NewCommitteeMembers) {
		return fmt.Errorf("DKGComplaint: dealer %s not in new_committee_members", dealerID)
	}

	// 5. 检查 receiver_id 是否在 new_committee_members 中
	if !isInCommittee(receiverID, transition.NewCommitteeMembers) {
		return fmt.Errorf("DKGComplaint: receiver %s not in new_committee_members", receiverID)
	}

	// 6. 验证对应的 share 已上链
	shareKey := KeyFrostVaultDkgShare(chain, vaultID, epochID, dealerID, receiverID)
	share, err := e.DB.GetFrostDkgShare(shareKey)
	if err != nil || share == nil {
		return fmt.Errorf("DKGComplaint: share not found for dealer=%s receiver=%s", dealerID, receiverID)
	}

	// 7. 存储投诉
	complaint := &pb.FrostVaultDkgComplaint{
		Chain:           chain,
		VaultId:         vaultID,
		EpochId:         epochID,
		DealerId:        dealerID,
		ReceiverId:      receiverID,
		Bond:            tx.Bond,
		ComplaintHeight: height,
		RevealDeadline:  height + RevealDeadlineBlocks,
		Status:          ComplaintStatusPending,
	}

	if err := e.DB.SetFrostDkgComplaint(complaintKey, complaint); err != nil {
		return fmt.Errorf("DKGComplaint: failed to store complaint: %w", err)
	}

	// 8. 更新 transition 状态到 Resolving（如果还在 Sharing）
	if transition.DkgStatus == DKGStatusSharing {
		transition.DkgStatus = DKGStatusResolving
		if err := e.DB.SetFrostVaultTransition(transitionKey, transition); err != nil {
			return fmt.Errorf("DKGComplaint: failed to update transition: %w", err)
		}
	}

	logs.Debug("[DKGComplaint] stored complaint dealer=%s receiver=%s", dealerID, receiverID)
	return nil
}

// KeyFrostVaultDkgComplaint 生成 DKG 投诉 key
func KeyFrostVaultDkgComplaint(chain string, vaultID uint32, epochID uint64, dealer, receiver string) string {
	return fmt.Sprintf("v1_frost_vault_dkg_complaint_%s_%d_%d_%s_%s", chain, vaultID, epochID, dealer, receiver)
}
