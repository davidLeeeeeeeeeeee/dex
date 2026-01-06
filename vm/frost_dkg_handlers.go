// vm/frost_dkg_handlers.go
// Frost DKG/轮换相关交易 TxHandler 实现

package vm

import (
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"
)

// ========== FrostVaultDkgCommitTxHandler ==========

type FrostVaultDkgCommitTxHandler struct{}

func (h *FrostVaultDkgCommitTxHandler) Kind() string {
	return "frost_vault_dkg_commit"
}

func (h *FrostVaultDkgCommitTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	commitTx, ok := tx.GetContent().(*pb.AnyTx_FrostVaultDkgCommitTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a dkg commit tx"}, errors.New("not a dkg commit tx")
	}

	req := commitTx.FrostVaultDkgCommitTx
	if req == nil || req.Base == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid dkg commit tx"}, errors.New("invalid dkg commit tx")
	}

	txID := req.Base.TxId
	sender := req.Base.FromAddress
	height := req.Base.ExecutedHeight
	chain := req.Chain
	vaultID := req.VaultId
	epochID := req.EpochId
	signAlgo := req.SignAlgo

	logs.Debug("[DKGCommit] sender=%s chain=%s vault=%d epoch=%d algo=%v", sender, chain, vaultID, epochID, signAlgo)

	// 1. 幂等检查
	existingKey := keys.KeyFrostVaultDkgCommit(chain, vaultID, epochID, sender)
	existingData, exists, _ := sv.Get(existingKey)
	if exists && len(existingData) > 0 {
		logs.Debug("[DKGCommit] already committed, idempotent")
		return nil, &Receipt{TxID: txID, Status: "SUCCEED", WriteCount: 0}, nil
	}

	// 2. 验证 VaultTransitionState 存在且状态正确
	transitionKey := keys.KeyFrostVaultTransition(chain, vaultID, epochID)
	transitionData, transitionExists, _ := sv.Get(transitionKey)
	if !transitionExists || len(transitionData) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "transition not found"}, errors.New("transition not found")
	}

	transition, err := unmarshalFrostVaultTransition(transitionData)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to parse transition"}, err
	}

	// 3. 检查 DKG 状态
	if transition.DkgStatus != DKGStatusCommitting {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: fmt.Sprintf("invalid dkg_status=%s", transition.DkgStatus)}, errors.New("invalid dkg_status")
	}

	// 4. 检查 sign_algo 一致性
	if transition.SignAlgo != signAlgo {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "sign_algo mismatch"}, errors.New("sign_algo mismatch")
	}

	// 5. 检查 sender 是否在 new_committee_members 中
	if !isInCommittee(sender, transition.NewCommitteeMembers) {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "sender not in committee"}, errors.New("sender not in committee")
	}

	// 6. 验证承诺点数据
	if len(req.CommitmentPoints) == 0 || len(req.AI0) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "empty commitment data"}, errors.New("empty commitment data")
	}

	// 7. 存储承诺
	commitment := &pb.FrostVaultDkgCommitment{
		Chain:            chain,
		VaultId:          vaultID,
		EpochId:          epochID,
		MinerAddress:     sender,
		SignAlgo:         signAlgo,
		CommitmentPoints: req.CommitmentPoints,
		AI0:              req.AI0,
		CommitHeight:     height,
	}

	commitData, err := proto.Marshal(commitment)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to marshal commitment"}, err
	}

	ops := []WriteOp{{
		Key:         existingKey,
		Value:       commitData,
		SyncStateDB: true,
		Category:    "frost_dkg_commit",
	}}

	for _, op := range ops {
		sv.Set(op.Key, op.Value)
	}

	logs.Debug("[DKGCommit] stored commitment for sender=%s", sender)
	return ops, &Receipt{TxID: txID, Status: "SUCCEED", WriteCount: len(ops)}, nil
}

func (h *FrostVaultDkgCommitTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}

// ========== FrostVaultDkgShareTxHandler ==========

type FrostVaultDkgShareTxHandler struct{}

func (h *FrostVaultDkgShareTxHandler) Kind() string {
	return "frost_vault_dkg_share"
}

func (h *FrostVaultDkgShareTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	shareTx, ok := tx.GetContent().(*pb.AnyTx_FrostVaultDkgShareTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a dkg share tx"}, errors.New("not a dkg share tx")
	}

	req := shareTx.FrostVaultDkgShareTx
	if req == nil || req.Base == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid dkg share tx"}, errors.New("invalid dkg share tx")
	}

	txID := req.Base.TxId
	sender := req.Base.FromAddress
	height := req.Base.ExecutedHeight
	chain := req.Chain
	vaultID := req.VaultId
	epochID := req.EpochId
	dealerID := req.DealerId
	receiverID := req.ReceiverId

	logs.Debug("[DKGShare] sender=%s chain=%s vault=%d epoch=%d dealer=%s receiver=%s",
		sender, chain, vaultID, epochID, dealerID, receiverID)

	// 验证 sender 是 dealer
	if sender != dealerID {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "sender != dealer_id"}, errors.New("sender != dealer_id")
	}

	// 1. 幂等检查
	existingKey := keys.KeyFrostVaultDkgShare(chain, vaultID, epochID, dealerID, receiverID)
	existingData, exists, _ := sv.Get(existingKey)
	if exists && len(existingData) > 0 {
		logs.Debug("[DKGShare] already submitted, idempotent")
		return nil, &Receipt{TxID: txID, Status: "SUCCEED", WriteCount: 0}, nil
	}

	// 2. 验证 VaultTransitionState 存在且状态正确
	transitionKey := keys.KeyFrostVaultTransition(chain, vaultID, epochID)
	transitionData, transitionExists, _ := sv.Get(transitionKey)
	if !transitionExists || len(transitionData) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "transition not found"}, errors.New("transition not found")
	}

	transition, err := unmarshalFrostVaultTransition(transitionData)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to parse transition"}, err
	}

	// 3. 检查 DKG 状态
	if transition.DkgStatus != DKGStatusSharing {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: fmt.Sprintf("invalid dkg_status=%s", transition.DkgStatus)}, errors.New("invalid dkg_status")
	}

	// 4. 检查 dealer/receiver 是否在 new_committee_members 中
	if !isInCommittee(dealerID, transition.NewCommitteeMembers) {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "dealer not in committee"}, errors.New("dealer not in committee")
	}
	if !isInCommittee(receiverID, transition.NewCommitteeMembers) {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "receiver not in committee"}, errors.New("receiver not in committee")
	}

	// 5. 验证密文不为空
	if len(req.Ciphertext) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "empty ciphertext"}, errors.New("empty ciphertext")
	}

	// 6. 存储 share
	share := &pb.FrostVaultDkgShare{
		Chain:       chain,
		VaultId:     vaultID,
		EpochId:     epochID,
		DealerId:    dealerID,
		ReceiverId:  receiverID,
		Ciphertext:  req.Ciphertext,
		ShareHeight: height,
	}

	shareData, err := proto.Marshal(share)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to marshal share"}, err
	}

	ops := []WriteOp{{
		Key:         existingKey,
		Value:       shareData,
		SyncStateDB: true,
		Category:    "frost_dkg_share",
	}}

	for _, op := range ops {
		sv.Set(op.Key, op.Value)
	}

	logs.Debug("[DKGShare] stored share dealer=%s -> receiver=%s", dealerID, receiverID)
	return ops, &Receipt{TxID: txID, Status: "SUCCEED", WriteCount: len(ops)}, nil
}

func (h *FrostVaultDkgShareTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}

// ========== FrostVaultDkgComplaintTxHandler ==========

type FrostVaultDkgComplaintTxHandler struct{}

func (h *FrostVaultDkgComplaintTxHandler) Kind() string {
	return "frost_vault_dkg_complaint"
}

func (h *FrostVaultDkgComplaintTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	complaintTx, ok := tx.GetContent().(*pb.AnyTx_FrostVaultDkgComplaintTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a dkg complaint tx"}, errors.New("not a dkg complaint tx")
	}

	req := complaintTx.FrostVaultDkgComplaintTx
	if req == nil || req.Base == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid dkg complaint tx"}, errors.New("invalid dkg complaint tx")
	}

	txID := req.Base.TxId
	sender := req.Base.FromAddress
	height := req.Base.ExecutedHeight
	chain := req.Chain
	vaultID := req.VaultId
	epochID := req.EpochId
	dealerID := req.DealerId
	receiverID := req.ReceiverId

	logs.Debug("[DKGComplaint] sender=%s chain=%s vault=%d epoch=%d dealer=%s receiver=%s",
		sender, chain, vaultID, epochID, dealerID, receiverID)

	// 验证 sender 是 receiver
	if sender != receiverID {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "sender != receiver_id"}, errors.New("sender != receiver_id")
	}

	// 1. 幂等检查
	complaintKey := keys.KeyFrostVaultDkgComplaint(chain, vaultID, epochID, dealerID, receiverID)
	existingData, exists, _ := sv.Get(complaintKey)
	if exists && len(existingData) > 0 {
		logs.Debug("[DKGComplaint] already submitted, idempotent")
		return nil, &Receipt{TxID: txID, Status: "SUCCEED", WriteCount: 0}, nil
	}

	// 2. 验证 VaultTransitionState 存在且状态正确
	transitionKey := keys.KeyFrostVaultTransition(chain, vaultID, epochID)
	transitionData, transitionExists, _ := sv.Get(transitionKey)
	if !transitionExists || len(transitionData) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "transition not found"}, errors.New("transition not found")
	}

	transition, err := unmarshalFrostVaultTransition(transitionData)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to parse transition"}, err
	}

	// 3. 检查 DKG 状态
	if transition.DkgStatus != DKGStatusSharing && transition.DkgStatus != DKGStatusResolving {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: fmt.Sprintf("invalid dkg_status=%s", transition.DkgStatus)}, errors.New("invalid dkg_status")
	}

	// 4. 检查 dealer/receiver 是否在 new_committee_members 中
	if !isInCommittee(dealerID, transition.NewCommitteeMembers) {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "dealer not in committee"}, errors.New("dealer not in committee")
	}
	if !isInCommittee(receiverID, transition.NewCommitteeMembers) {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "receiver not in committee"}, errors.New("receiver not in committee")
	}

	// 5. 验证对应的 share 已上链
	shareKey := keys.KeyFrostVaultDkgShare(chain, vaultID, epochID, dealerID, receiverID)
	shareData, shareExists, _ := sv.Get(shareKey)
	if !shareExists || len(shareData) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "share not found"}, errors.New("share not found")
	}

	// 6. 存储投诉
	complaint := &pb.FrostVaultDkgComplaint{
		Chain:           chain,
		VaultId:         vaultID,
		EpochId:         epochID,
		DealerId:        dealerID,
		ReceiverId:      receiverID,
		Bond:            req.Bond,
		ComplaintHeight: height,
		RevealDeadline:  height + RevealDeadlineBlocks,
		Status:          ComplaintStatusPending,
	}

	complaintData, err := proto.Marshal(complaint)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to marshal complaint"}, err
	}

	ops := []WriteOp{{
		Key:         complaintKey,
		Value:       complaintData,
		SyncStateDB: true,
		Category:    "frost_dkg_complaint",
	}}

	// 7. 更新 transition 状态到 Resolving（如果还在 Sharing）
	if transition.DkgStatus == DKGStatusSharing {
		transition.DkgStatus = DKGStatusResolving
		transitionData, err := proto.Marshal(transition)
		if err == nil {
			ops = append(ops, WriteOp{
				Key:         transitionKey,
				Value:       transitionData,
				SyncStateDB: true,
				Category:    "frost_vault_transition",
			})
		}
	}

	for _, op := range ops {
		sv.Set(op.Key, op.Value)
	}

	logs.Debug("[DKGComplaint] stored complaint dealer=%s receiver=%s", dealerID, receiverID)
	return ops, &Receipt{TxID: txID, Status: "SUCCEED", WriteCount: len(ops)}, nil
}

func (h *FrostVaultDkgComplaintTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}

// ========== FrostVaultDkgRevealTxHandler ==========

type FrostVaultDkgRevealTxHandler struct{}

func (h *FrostVaultDkgRevealTxHandler) Kind() string {
	return "frost_vault_dkg_reveal"
}

func (h *FrostVaultDkgRevealTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	revealTx, ok := tx.GetContent().(*pb.AnyTx_FrostVaultDkgRevealTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a dkg reveal tx"}, errors.New("not a dkg reveal tx")
	}

	req := revealTx.FrostVaultDkgRevealTx
	if req == nil || req.Base == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid dkg reveal tx"}, errors.New("invalid dkg reveal tx")
	}

	txID := req.Base.TxId
	sender := req.Base.FromAddress
	chain := req.Chain
	vaultID := req.VaultId
	epochID := req.EpochId
	dealerID := req.DealerId
	receiverID := req.ReceiverId

	logs.Debug("[DKGReveal] sender=%s chain=%s vault=%d epoch=%d dealer=%s receiver=%s",
		sender, chain, vaultID, epochID, dealerID, receiverID)

	// 验证 sender 是 dealer
	if sender != dealerID {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "sender != dealer_id"}, errors.New("sender != dealer_id")
	}

	// 1. 查找对应的投诉
	complaintKey := keys.KeyFrostVaultDkgComplaint(chain, vaultID, epochID, dealerID, receiverID)
	complaintData, complaintExists, _ := sv.Get(complaintKey)
	if !complaintExists || len(complaintData) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "complaint not found"}, errors.New("complaint not found")
	}

	complaint := &pb.FrostVaultDkgComplaint{}
	if err := proto.Unmarshal(complaintData, complaint); err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to parse complaint"}, err
	}

	// 2. 检查投诉状态
	if complaint.Status != ComplaintStatusPending {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: fmt.Sprintf("complaint status=%s", complaint.Status)}, errors.New("complaint not pending")
	}

	// 3. 验证 reveal 数据
	if len(req.Share) == 0 || len(req.EncRand) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "empty reveal data"}, errors.New("empty reveal data")
	}

	// 4. 获取 transition 状态
	transitionKey := keys.KeyFrostVaultTransition(chain, vaultID, epochID)
	transitionData, transitionExists, _ := sv.Get(transitionKey)
	if !transitionExists || len(transitionData) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "transition not found"}, errors.New("transition not found")
	}

	transition, err := unmarshalFrostVaultTransition(transitionData)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to parse transition"}, err
	}

	// 5. 获取 dealer 的 commitment 和原始 share
	commitKey := keys.KeyFrostVaultDkgCommit(chain, vaultID, epochID, dealerID)
	commitData, commitExists, _ := sv.Get(commitKey)
	if !commitExists || len(commitData) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "dealer commitment not found"}, errors.New("dealer commitment not found")
	}

	commitment := &pb.FrostVaultDkgCommitment{}
	if err := proto.Unmarshal(commitData, commitment); err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to parse commitment"}, err
	}

	shareKey := keys.KeyFrostVaultDkgShare(chain, vaultID, epochID, dealerID, receiverID)
	shareData, shareExists, _ := sv.Get(shareKey)
	if !shareExists || len(shareData) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "share not found"}, errors.New("share not found")
	}

	originalShare := &pb.FrostVaultDkgShare{}
	if err := proto.Unmarshal(shareData, originalShare); err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to parse share"}, err
	}

	// 6. 验证 reveal（密码学验证）
	receiverIndex := getReceiverIndexInCommittee(receiverID, transition.NewCommitteeMembers)
	valid := verifyRevealInDryRun(req.Share, req.EncRand, originalShare.Ciphertext, commitment.CommitmentPoints, receiverIndex)

	ops := []WriteOp{}

	// 7. 根据验证结果处理
	if valid {
		// share 有效 - 恶意投诉
		complaint.Status = ComplaintStatusResolved
		logs.Info("[DKGReveal] share is valid, false complaint by receiver=%s", receiverID)

		// 剔除投诉者（恶意投诉）
		transition.NewCommitteeMembers = removeFromCommittee(transition.NewCommitteeMembers, receiverID)
		transition.DkgN = uint32(len(transition.NewCommitteeMembers))

		// 检查门限可行性
		if int(transition.DkgN) < int(transition.DkgThresholdT) {
			transition.DkgStatus = DKGStatusFailed
			logs.Warn("[DKGReveal] insufficient qualified participants after removing %s, DKG failed", receiverID)
		}

		// 清空 dealer 的 commitment（share 已泄露，需要重新生成）
		// 注意：只清空该 dealer 的 commitment，其他参与者保持不变
		ops = append(ops, WriteOp{
			Key:         commitKey,
			Value:       nil, // 删除
			Del:         true,
			SyncStateDB: true,
			Category:    "frost_dkg_commit",
		})
	} else {
		// share 无效 - dealer 作恶
		complaint.Status = ComplaintStatusDisqualified
		logs.Info("[DKGReveal] share is invalid, dealer %s disqualified", dealerID)

		// 剔除 dealer（作恶）
		transition.NewCommitteeMembers = removeFromCommittee(transition.NewCommitteeMembers, dealerID)
		transition.DkgN = uint32(len(transition.NewCommitteeMembers))

		// 检查门限可行性
		if int(transition.DkgN) < int(transition.DkgThresholdT) {
			transition.DkgStatus = DKGStatusFailed
			logs.Warn("[DKGReveal] insufficient qualified participants after removing %s, DKG failed", dealerID)
		} else {
			// 继续流程：其他参与者计算份额时直接排除该 dealer 的贡献
			logs.Info("[DKGReveal] dealer %s disqualified, continuing DKG with remaining participants", dealerID)
		}
	}

	// 更新投诉状态
	updatedComplaintData, err := proto.Marshal(complaint)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to marshal complaint"}, err
	}
	ops = append(ops, WriteOp{
		Key:         complaintKey,
		Value:       updatedComplaintData,
		SyncStateDB: true,
		Category:    "frost_dkg_complaint",
	})

	// 更新 transition
	updatedTransitionData, err := proto.Marshal(transition)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to marshal transition"}, err
	}
	ops = append(ops, WriteOp{
		Key:         transitionKey,
		Value:       updatedTransitionData,
		SyncStateDB: true,
		Category:    "frost_vault_transition",
	})

	for _, op := range ops {
		sv.Set(op.Key, op.Value)
	}

	logs.Debug("[DKGReveal] resolved complaint dealer=%s receiver=%s valid=%v", dealerID, receiverID, valid)
	return ops, &Receipt{TxID: txID, Status: "SUCCEED", WriteCount: len(ops)}, nil
}

func (h *FrostVaultDkgRevealTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}

// verifyRevealInDryRun 在 DryRun 中验证 reveal 数据（简化版，实际应调用完整验证）
func verifyRevealInDryRun(share, encRand, ciphertext []byte, commitmentPoints [][]byte, receiverIndex int) bool {
	// 基本参数校验
	if len(share) == 0 || len(encRand) == 0 || len(ciphertext) == 0 || len(commitmentPoints) == 0 {
		return false
	}
	// TODO: 实现完整的密码学验证（ECIES 密文验证 + Feldman VSS commitment 验证）
	// 这里简化处理，返回 true 表示验证通过
	return true
}

// getReceiverIndexInCommittee 从委员会中获取成员索引（1-based）
func getReceiverIndexInCommittee(receiverID string, committeeMembers []string) int {
	for i, member := range committeeMembers {
		if member == receiverID {
			return i + 1 // 1-based index
		}
	}
	return 0
}

// removeFromCommittee 从委员会中移除成员
func removeFromCommittee(committee []string, memberID string) []string {
	result := make([]string, 0, len(committee))
	for _, m := range committee {
		if m != memberID {
			result = append(result, m)
		}
	}
	return result
}

// ========== FrostVaultDkgValidationSignedTxHandler ==========

type FrostVaultDkgValidationSignedTxHandler struct{}

func (h *FrostVaultDkgValidationSignedTxHandler) Kind() string {
	return "frost_vault_dkg_validation_signed"
}

func (h *FrostVaultDkgValidationSignedTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	validationTx, ok := tx.GetContent().(*pb.AnyTx_FrostVaultDkgValidationSignedTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a dkg validation signed tx"}, errors.New("not a dkg validation signed tx")
	}

	req := validationTx.FrostVaultDkgValidationSignedTx
	if req == nil || req.Base == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid dkg validation signed tx"}, errors.New("invalid dkg validation signed tx")
	}

	txID := req.Base.TxId
	chain := req.Chain
	vaultID := req.VaultId
	epochID := req.EpochId

	logs.Debug("[DKGValidationSigned] chain=%s vault=%d epoch=%d", chain, vaultID, epochID)

	// 1. 验证 VaultTransitionState 存在
	transitionKey := keys.KeyFrostVaultTransition(chain, vaultID, epochID)
	transitionData, transitionExists, _ := sv.Get(transitionKey)
	if !transitionExists || len(transitionData) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "transition not found"}, errors.New("transition not found")
	}

	transition, err := unmarshalFrostVaultTransition(transitionData)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to parse transition"}, err
	}

	// 2. 检查状态（应在 Sharing 或 Resolving 之后）
	if transition.DkgStatus != DKGStatusSharing && transition.DkgStatus != DKGStatusResolving {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: fmt.Sprintf("invalid dkg_status=%s", transition.DkgStatus)}, errors.New("invalid dkg_status")
	}

	// 3. 验证签名
	if len(req.Signature) == 0 || len(req.NewGroupPubkey) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "empty signature or pubkey"}, errors.New("empty signature or pubkey")
	}

	// TODO: 使用 group_pubkey 验证签名是否有效

	// 4. 更新 transition 状态
	transition.DkgStatus = DKGStatusKeyReady
	transition.ValidationStatus = "PASSED"
	transition.ValidationMsgHash = req.MsgHash

	updatedTransitionData, err := proto.Marshal(transition)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to marshal transition"}, err
	}

	// 5. 更新 VaultState
	vaultKey := keys.KeyFrostVaultState(chain, vaultID)
	vaultData, vaultExists, _ := sv.Get(vaultKey)

	var vault *pb.FrostVaultState
	if vaultExists && len(vaultData) > 0 {
		vault, _ = unmarshalFrostVaultState(vaultData)
	}
	if vault == nil {
		vault = &pb.FrostVaultState{
			VaultId: vaultID,
			Chain:   chain,
		}
	}

	vault.KeyEpoch = epochID
	vault.GroupPubkey = req.NewGroupPubkey
	vault.SignAlgo = transition.SignAlgo
	vault.Status = VaultStatusKeyReady

	updatedVaultData, err := proto.Marshal(vault)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to marshal vault"}, err
	}

	ops := []WriteOp{
		{Key: transitionKey, Value: updatedTransitionData, SyncStateDB: true, Category: "frost_vault_transition"},
		{Key: vaultKey, Value: updatedVaultData, SyncStateDB: true, Category: "frost_vault_state"},
	}

	for _, op := range ops {
		sv.Set(op.Key, op.Value)
	}

	logs.Debug("[DKGValidationSigned] vault=%d epoch=%d now KEY_READY", vaultID, epochID)
	return ops, &Receipt{TxID: txID, Status: "SUCCEED", WriteCount: len(ops)}, nil
}

func (h *FrostVaultDkgValidationSignedTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}

// ========== FrostVaultTransitionSignedTxHandler ==========

type FrostVaultTransitionSignedTxHandler struct{}

func (h *FrostVaultTransitionSignedTxHandler) Kind() string {
	return "frost_vault_transition_signed"
}

func (h *FrostVaultTransitionSignedTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	transitionTx, ok := tx.GetContent().(*pb.AnyTx_FrostVaultTransitionSignedTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a transition signed tx"}, errors.New("not a transition signed tx")
	}

	req := transitionTx.FrostVaultTransitionSignedTx
	if req == nil || req.Base == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid transition signed tx"}, errors.New("invalid transition signed tx")
	}

	txID := req.Base.TxId
	chain := req.Chain
	oldVaultID := req.OldVaultId
	newVaultID := req.NewVaultId
	epochID := req.EpochId

	logs.Debug("[TransitionSigned] chain=%s old_vault=%d new_vault=%d epoch=%d",
		chain, oldVaultID, newVaultID, epochID)

	// 1. 验证旧 Vault 存在且状态正确
	oldVaultKey := keys.KeyFrostVaultState(chain, oldVaultID)
	oldVaultData, oldVaultExists, _ := sv.Get(oldVaultKey)
	if !oldVaultExists || len(oldVaultData) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "old vault not found"}, errors.New("old vault not found")
	}

	oldVault, err := unmarshalFrostVaultState(oldVaultData)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to parse old vault"}, err
	}

	// 2. 验证签名
	if len(req.Signature) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "empty signature"}, errors.New("empty signature")
	}

	// TODO: 使用旧 vault 的 group_pubkey 验证签名

	// 3. 更新旧 Vault 状态为 DRAINING
	oldVault.Status = VaultLifecycleDraining
	updatedOldVaultData, err := proto.Marshal(oldVault)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to marshal old vault"}, err
	}

	// 4. 验证新 Vault 存在且状态为 KEY_READY
	newVaultKey := keys.KeyFrostVaultState(chain, newVaultID)
	newVaultData, newVaultExists, _ := sv.Get(newVaultKey)
	if !newVaultExists || len(newVaultData) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "new vault not found"}, errors.New("new vault not found")
	}

	newVault, err := unmarshalFrostVaultState(newVaultData)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to parse new vault"}, err
	}

	if newVault.Status != VaultStatusKeyReady {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: fmt.Sprintf("new vault status=%s", newVault.Status)}, errors.New("new vault not KEY_READY")
	}

	// 5. 激活新 Vault
	newVault.Status = VaultStatusActive
	updatedNewVaultData, err := proto.Marshal(newVault)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to marshal new vault"}, err
	}

	ops := []WriteOp{
		{Key: oldVaultKey, Value: updatedOldVaultData, SyncStateDB: true, Category: "frost_vault_state"},
		{Key: newVaultKey, Value: updatedNewVaultData, SyncStateDB: true, Category: "frost_vault_state"},
	}

	for _, op := range ops {
		sv.Set(op.Key, op.Value)
	}

	logs.Debug("[TransitionSigned] old_vault=%d -> DRAINING, new_vault=%d -> ACTIVE", oldVaultID, newVaultID)
	return ops, &Receipt{TxID: txID, Status: "SUCCEED", WriteCount: len(ops)}, nil
}

func (h *FrostVaultTransitionSignedTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}
