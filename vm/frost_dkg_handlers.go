// vm/frost_dkg_handlers.go
// Frost DKG/轮换相关交易 TxHandler 实现

package vm

import (
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"errors"
	"fmt"
	"math/big"

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

	// 3. 严格校验 Commit 窗口：只能在 [Trigger, CommitDeadline] 区间
	if height < transition.TriggerHeight || height > transition.DkgCommitDeadline {
		logs.Warn("[DKGCommit] height out of window: height=%d window=[%d, %d]", height, transition.TriggerHeight, transition.DkgCommitDeadline)
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: fmt.Sprintf("commit window violation (height %d, window [%d, %d])", height, transition.TriggerHeight, transition.DkgCommitDeadline)}, nil
	}

	// 4. 检查 DKG 状态
	if transition.DkgStatus != DKGStatusCommitting && transition.DkgStatus != DKGStatusNotStarted {
		// 既然有了严格的高度区间，逻辑状态检查可以更严格。
		// 如果状态已经推进到 SHARING，说明已经过了 Commit 阶段（或者有人违规提前推状态，但高度检测是第一道防线）
		logs.Warn("[DKGCommit] invalid dkg_status for commit: %s", transition.DkgStatus)
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: fmt.Sprintf("invalid dkg_status=%s", transition.DkgStatus)}, nil
	}

	// 准备写操作列表
	ops := []WriteOp{}

	// 如果当前是 NOT_STARTED，则在接收到第一个 CommitTx 时推进到 COMMITTING 状态
	if transition.DkgStatus == DKGStatusNotStarted {
		transition.DkgStatus = DKGStatusCommitting
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

	ops = append(ops, WriteOp{
		Key:         existingKey,
		Value:       commitData,
		SyncStateDB: true,
		Category:    "frost_dkg_commit",
	})

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

	// 3. 严格校验 Sharing 窗口：只能在 (CommitDeadline, SharingDeadline] 区间
	if height <= transition.DkgCommitDeadline {
		logs.Warn("[DKGShare] too early: height=%d <= deadline=%d", height, transition.DkgCommitDeadline)
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: fmt.Sprintf("sharing window violation (too early: height %d <= commit_deadline %d)", height, transition.DkgCommitDeadline)}, nil
	}
	if height > transition.DkgSharingDeadline {
		logs.Warn("[DKGShare] too late: height=%d > deadline=%d", height, transition.DkgSharingDeadline)
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: fmt.Sprintf("sharing window violation (too late: height %d > sharing_deadline %d)", height, transition.DkgSharingDeadline)}, nil
	}

	// 4. 检查 DKG 状态
	if transition.DkgStatus != DKGStatusSharing && transition.DkgStatus != DKGStatusCommitting &&
		transition.DkgStatus != DKGStatusNotStarted {
		logs.Warn("[DKGShare] invalid dkg_status for share: %s", transition.DkgStatus)
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: fmt.Sprintf("invalid dkg_status=%s", transition.DkgStatus)}, nil
	}

	ops := []WriteOp{}

	// 如果当前是 COMMITTING 或 NOT_STARTED，则在接收到第一个 ShareTx 时推进到 SHARING 状态
	if transition.DkgStatus == DKGStatusCommitting || transition.DkgStatus == DKGStatusNotStarted {
		transition.DkgStatus = DKGStatusSharing
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

	ops = append(ops, WriteOp{
		Key:         existingKey,
		Value:       shareData,
		SyncStateDB: true,
		Category:    "frost_dkg_share",
	})

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

	// 3. 严格校验 Dispute 窗口：只能在 (SharingDeadline, DisputeDeadline] 区间
	if height <= transition.DkgSharingDeadline || height > transition.DkgDisputeDeadline {
		logs.Warn("[DKGComplaint] height out of window: height=%d window=(%d, %d]", height, transition.DkgSharingDeadline, transition.DkgDisputeDeadline)
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: fmt.Sprintf("dispute window violation (height %d, window (%d, %d])", height, transition.DkgSharingDeadline, transition.DkgDisputeDeadline)}, nil
	}

	// 4. 检查 DKG 状态
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

	// 2. 检查投诉状态和 Reveal 截止高度
	if complaint.Status != ComplaintStatusPending {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: fmt.Sprintf("complaint status=%s", complaint.Status)}, errors.New("complaint not pending")
	}
	if req.Base.ExecutedHeight > complaint.RevealDeadline {
		logs.Warn("[DKGReveal] reveal deadline exceeded for complaint: height=%d deadline=%d", req.Base.ExecutedHeight, complaint.RevealDeadline)
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "reveal deadline exceeded"}, nil
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
	// 注意：NewCommitteeMembers 就是 qualified_set，剔除时从该集合移除
	// disqualified_set 可以通过投诉记录和 transition 状态推断，暂不单独维护
	if valid {
		// share 有效 - 恶意投诉
		// 处理规则：罚没投诉者的 bond，剔除投诉者，清空 dealer commitment（share 已泄露）
		complaint.Status = ComplaintStatusResolved
		logs.Info("[DKGReveal] share is valid, false complaint by receiver=%s", receiverID)

		// 罚没投诉者的 bond（100% 罚没）
		if complaint.Bond != "" && complaint.Bond != "0" {
			bondAmount, ok := new(big.Int).SetString(complaint.Bond, 10)
			if ok && bondAmount.Sign() > 0 {
				slashOps, err := slashBond(sv, receiverID, bondAmount, "DKG false complaint")
				if err != nil {
					logs.Warn("[DKGReveal] failed to slash bond for receiver %s: %v", receiverID, err)
				} else {
					ops = append(ops, slashOps...)
					logs.Info("[DKGReveal] slashed bond %s from false complainer %s", complaint.Bond, receiverID)
				}
			}
		}

		// 剔除投诉者（恶意投诉）：qualified_set.remove(receiverID)
		transition.NewCommitteeMembers = removeFromCommittee(transition.NewCommitteeMembers, receiverID)
		transition.DkgN = uint32(len(transition.NewCommitteeMembers))

		// 检查门限可行性：current_n < initial_t 时，必须重启 DKG
		if int(transition.DkgN) < int(transition.DkgThresholdT) {
			transition.DkgStatus = DKGStatusFailed
			logs.Warn("[DKGReveal] insufficient qualified participants after removing %s: current_n=%d < initial_t=%d, DKG failed (restart required)",
				receiverID, transition.DkgN, transition.DkgThresholdT)
		}

		// 清空 dealer 的 commitment（share 已泄露，需要重新生成）
		// 注意：只清空该 dealer 的 commitment，其他参与者保持不变
		// dealer 需要重新提交 commitment 和 share
		ops = append(ops, WriteOp{
			Key:         commitKey,
			Value:       nil, // 删除
			Del:         true,
			SyncStateDB: true,
			Category:    "frost_dkg_commit",
		})
	} else {
		// share 无效 - dealer 作恶
		// 处理规则：slash dealer 的质押金（100%），剔除 dealer，其他参与者继续，无需重新生成多项式
		complaint.Status = ComplaintStatusDisqualified
		logs.Info("[DKGReveal] share is invalid, dealer %s disqualified", dealerID)

		// Slash dealer 的质押金（100% 比例）
		slashOps, err := slashMinerStake(sv, dealerID, "DKG dealer fault: invalid share")
		if err != nil {
			logs.Warn("[DKGReveal] failed to slash stake for dealer %s: %v", dealerID, err)
		} else {
			ops = append(ops, slashOps...)
			logs.Info("[DKGReveal] slashed 100%% stake from dealer %s", dealerID)
		}

		// 剔除 dealer（作恶）：qualified_set.remove(dealerID)
		transition.NewCommitteeMembers = removeFromCommittee(transition.NewCommitteeMembers, dealerID)
		transition.DkgN = uint32(len(transition.NewCommitteeMembers))

		// 检查门限可行性：current_n < initial_t 时，必须重启 DKG
		if int(transition.DkgN) < int(transition.DkgThresholdT) {
			transition.DkgStatus = DKGStatusFailed
			logs.Warn("[DKGReveal] insufficient qualified participants after removing %s: current_n=%d < initial_t=%d, DKG failed (restart required)",
				dealerID, transition.DkgN, transition.DkgThresholdT)
		} else {
			// 继续流程：其他参与者计算份额时直接排除该 dealer 的贡献
			// 注意：不需要重新生成多项式，只需在聚合 group_pubkey 时排除该 dealer 的 a_i0
			logs.Info("[DKGReveal] dealer %s disqualified, continuing DKG with remaining participants (n=%d, t=%d)",
				dealerID, transition.DkgN, transition.DkgThresholdT)
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

// checkDkgRestartRule 检查 DKG 重启规则
// 当 current_n < initial_t 时，必须重启 DKG
// 返回是否需要重启
func checkDkgRestartRule(currentN, initialT uint32) bool {
	return int(currentN) < int(initialT)
}

// handleDkgRestart 处理 DKG 重启（当 qualified_set 不足时）
// 根据设计文档，重启时需要：
// - epoch_id += 1（新 epoch）
// - 重新加载完整委员会
// - 清空 disqualified_set
// - 重置 initial_n 和 initial_t
// - dkg_status = COMMITTING
// TODO: 重启时必须更新 DkgCommitDeadline，否则会立即过期。需配合当前区块高度计算。
func handleDkgRestart(transition *pb.VaultTransitionState, newEpochID uint64, fullCommittee []string, thresholdRatio float64) {
	transition.EpochId = newEpochID
	transition.NewCommitteeMembers = fullCommittee // 重新加载完整委员会
	transition.DkgN = uint32(len(fullCommittee))
	// 重新计算门限
	threshold := int(float64(len(fullCommittee)) * thresholdRatio)
	if threshold < 1 {
		threshold = 1
	}
	transition.DkgThresholdT = uint32(threshold)
	transition.DkgStatus = DKGStatusCommitting
	// 清空相关状态
	transition.NewGroupPubkey = nil
	transition.ValidationStatus = "NOT_STARTED"
	transition.ValidationMsgHash = nil
	logs.Info("[DKGRestart] DKG restarted: epoch=%d n=%d t=%d", newEpochID, transition.DkgN, transition.DkgThresholdT)
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

	sender := req.Base.FromAddress
	logs.Debug("[DKGValidationSigned] sender=%s chain=%s vault=%d epoch=%d", sender, chain, vaultID, epochID)

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

	// 2. 检查高度和状态限制
	height := req.Base.ExecutedHeight
	if height <= transition.DkgSharingDeadline {
		logs.Warn("[DKGValidation] sharing window not closed: height=%d deadline=%d", height, transition.DkgSharingDeadline)
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: fmt.Sprintf("sharing window not closed (height %d <= deadline %d)", height, transition.DkgSharingDeadline)}, nil
	}

	if transition.DkgStatus != DKGStatusSharing && transition.DkgStatus != DKGStatusResolving && transition.DkgStatus != DKGStatusCommitting {
		// 如果已经是 KEY_READY，则是延迟到达的重复请求，幂等处理
		if transition.DkgStatus == DKGStatusKeyReady {
			return nil, &Receipt{TxID: txID, Status: "SUCCEED", WriteCount: 0}, nil
		}
		// 其他不合适的状态
		logs.Warn("[DKGValidation] invalid dkg_status: %s", transition.DkgStatus)
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: fmt.Sprintf("invalid dkg_status=%s", transition.DkgStatus)}, nil
	}

	// 3. 验证确认者身份（目前节点由于网络限制暂只提交 Partial Signature，VM 接受委员会成员的确认）
	if !isInCommittee(sender, transition.NewCommitteeMembers) {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "sender not in committee"}, nil
	}

	// 重新计算哈希并验证，确保节点承诺的组公钥与发送的哈希一致
	expectedHash := computeDkgValidationMsgHash(chain, vaultID, epochID, transition.SignAlgo, req.NewGroupPubkey)
	if fmt.Sprintf("0x%x", expectedHash) != fmt.Sprintf("0x%x", req.MsgHash) {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "msg_hash mismatch"}, nil
	}

	logs.Info("[DKGValidation] received validation confirmation from %s for group pubkey %x", sender, req.NewGroupPubkey[:8])
	// TODO: 生产环境应聚合 T 个 Partial Signatures 并验证组签名

	// 4. 更新 transition 状态
	transition.DkgStatus = DKGStatusKeyReady
	transition.ValidationStatus = "PASSED"
	transition.ValidationMsgHash = req.MsgHash
	transition.NewGroupPubkey = req.NewGroupPubkey

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
	// 如果没有旧公钥（第一次 DKG），则直接进入 ACTIVE 状态，否则需要通过迁移交易激活
	if len(transition.OldGroupPubkey) == 0 {
		vault.Status = VaultStatusActive
		logs.Info("[DKGValidation] Vault %d activated automatically (first epoch)", vaultID)
	}

	// 更新委员会成员（从 transition 获取）
	if len(transition.NewCommitteeMembers) > 0 {
		vault.CommitteeMembers = transition.NewCommitteeMembers
	}
	// 设置 lifecycle：DKG 完成后，如果这是首次创建，lifecycle 为 KEY_READY
	// 如果是从旧 Vault 轮换来的，lifecycle 应该已经在 transition 中设置
	if transition.Lifecycle != "" {
		// transition 中可能已经有 lifecycle 信息，但 VaultState 本身不存储 lifecycle
		// lifecycle 主要在 VaultTransitionState 中管理
		// VaultState 的 status 字段用于表示密钥状态：PENDING -> KEY_READY -> ACTIVE
	}

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

// slashBond 罚没投诉者的 bond（从账户余额中扣除）
// 返回 WriteOp 列表用于更新账户余额
func slashBond(sv StateView, address string, bondAmount *big.Int, reason string) ([]WriteOp, error) {
	if bondAmount == nil || bondAmount.Sign() <= 0 {
		return nil, fmt.Errorf("invalid bond amount")
	}

	// 使用 FB 作为原生代币
	tokenAddr := "FB"

	// 从分离存储读取余额
	fbBal := GetBalance(sv, address, tokenAddr)

	// 从可用余额中扣除 bond（如果余额不足，则扣除全部可用余额）
	balance, _ := new(big.Int).SetString(fbBal.Balance, 10)
	if balance == nil {
		balance = big.NewInt(0)
	}

	// 扣除 bond（最多扣除全部可用余额）
	slashAmount := bondAmount
	if balance.Cmp(bondAmount) < 0 {
		slashAmount = balance
		logs.Warn("[slashBond] bond amount %s exceeds balance %s for %s, slashing only available balance", bondAmount.String(), balance.String(), address)
	}

	newBalance, err := SafeSub(balance, slashAmount)
	if err != nil {
		return nil, fmt.Errorf("failed to subtract bond: %w", err)
	}

	fbBal.Balance = newBalance.String()
	SetBalance(sv, address, tokenAddr, fbBal)

	logs.Info("[slashBond] slashed bond %s from %s (reason: %s), remaining balance: %s", slashAmount.String(), address, reason, newBalance.String())

	// 余额已通过 SetBalance 保存，需要返回对应的 WriteOp
	balanceKey := keys.KeyBalance(address, tokenAddr)
	balData, _, _ := sv.Get(balanceKey)

	return []WriteOp{{
		Key:         balanceKey,
		Value:       balData,
		SyncStateDB: true,
		Category:    "balance",
	}}, nil
}

// slashMinerStake 罚没矿工的质押金（100% 比例，从 MinerLockedBalance 中扣除）
// 返回 WriteOp 列表用于更新账户余额
func slashMinerStake(sv StateView, address string, reason string) ([]WriteOp, error) {
	// 使用 FB 作为原生代币
	tokenAddr := "FB"

	// 从分离存储读取余额
	fbBal := GetBalance(sv, address, tokenAddr)

	// 获取当前质押金（100% 罚没）
	lockedBalance, _ := new(big.Int).SetString(fbBal.MinerLockedBalance, 10)
	if lockedBalance == nil || lockedBalance.Sign() <= 0 {
		logs.Warn("[slashMinerStake] no locked balance to slash for %s", address)
		return nil, nil // 没有质押金可罚没，返回空操作
	}

	// 100% 罚没：将 MinerLockedBalance 清零
	fbBal.MinerLockedBalance = "0"
	SetBalance(sv, address, tokenAddr, fbBal)

	logs.Info("[slashMinerStake] slashed 100%% stake %s from %s (reason: %s)", lockedBalance.String(), address, reason)

	// 余额已通过 SetBalance 保存，需要返回对应的 WriteOp
	balanceKey := keys.KeyBalance(address, tokenAddr)
	balData, _, _ := sv.Get(balanceKey)

	return []WriteOp{{
		Key:         balanceKey,
		Value:       balData,
		SyncStateDB: true,
		Category:    "balance",
	}}, nil
}
