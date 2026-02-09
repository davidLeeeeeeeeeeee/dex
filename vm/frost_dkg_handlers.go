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
	if err := unmarshalProtoCompat(complaintData, complaint); err != nil {
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
	if err := unmarshalProtoCompat(commitData, commitment); err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to parse commitment"}, err
	}

	shareKey := keys.KeyFrostVaultDkgShare(chain, vaultID, epochID, dealerID, receiverID)
	shareData, shareExists, _ := sv.Get(shareKey)
	if !shareExists || len(shareData) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "share not found"}, errors.New("share not found")
	}

	originalShare := &pb.FrostVaultDkgShare{}
	if err := unmarshalProtoCompat(shareData, originalShare); err != nil {
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
			bondAmount, err := parsePositiveBalanceStrict("complaint bond", complaint.Bond)
			if err == nil {
				slashOps, err := slashBond(sv, receiverID, bondAmount, "DKG false complaint")
				if err != nil {
					logs.Warn("[DKGReveal] failed to slash bond for receiver %s: %v", receiverID, err)
				} else {
					ops = append(ops, slashOps...)
					logs.Info("[DKGReveal] slashed bond %s from false complainer %s", complaint.Bond, receiverID)
				}
			} else {
				logs.Warn("[DKGReveal] invalid complaint bond for receiver %s: %v", receiverID, err)
			}
		}

		// 閸撴棃娅庨幎鏇＄様閼板拑绱欓幁鑸靛壈閹舵洝鐦旈敍澶涚窗qualified_set.remove(receiverID)
		transition.NewCommitteeMembers = removeFromCommittee(transition.NewCommitteeMembers, receiverID)
		transition.DkgN = uint32(len(transition.NewCommitteeMembers))

		// 濡偓閺屻儵妫梽鎰讲鐞涘本鈧嶇窗current_n < initial_t 閺冭绱濊箛鍛淬€忛柌宥呮儙 DKG
		if int(transition.DkgN) < int(transition.DkgThresholdT) {
			transition.DkgStatus = DKGStatusFailed
			logs.Warn("[DKGReveal] insufficient qualified participants after removing %s: current_n=%d < initial_t=%d, DKG failed (restart required)",
				receiverID, transition.DkgN, transition.DkgThresholdT)
		}

		// dealer 需要重新提交 commitment 和 share
		// 濞夈劍鍓伴敍姘涧濞撳懐鈹栫拠?dealer 閻?commitment閿涘苯鍙炬禒鏍у棘娑撳氦鈧懍绻氶幐浣风瑝閸?
		// dealer 闂団偓鐟曚線鍣搁弬鐗堝絹娴?commitment 閸?share
		ops = append(ops, WriteOp{
			Key:         commitKey,
			Value:       nil, // 閸掔娀娅?
			Del:         true,
			SyncStateDB: true,
			Category:    "frost_dkg_commit",
		})
	} else {
		// share 閺冪姵鏅?- dealer 娴ｆ粍浼?
		// 婢跺嫮鎮婄憴鍕灟閿涙lash dealer 閻ㄥ嫯宸濋幎濂稿櫨閿?00%閿涘绱濋崜鏃堟珟 dealer閿涘苯鍙炬禒鏍у棘娑撳氦鈧懐鎴风紒顓ㄧ礉閺冪娀娓堕柌宥嗘煀閻㈢喐鍨氭径姘躲€嶅?
		complaint.Status = ComplaintStatusDisqualified
		logs.Info("[DKGReveal] share is invalid, dealer %s disqualified", dealerID)

		// Slash dealer 閻ㄥ嫯宸濋幎濂稿櫨閿?00% 濮ｆ柧绶ラ敍?
		slashOps, err := slashMinerStake(sv, dealerID, "DKG dealer fault: invalid share")
		if err != nil {
			logs.Warn("[DKGReveal] failed to slash stake for dealer %s: %v", dealerID, err)
		} else {
			ops = append(ops, slashOps...)
			logs.Info("[DKGReveal] slashed 100%% stake from dealer %s", dealerID)
		}

		// 閸撴棃娅?dealer閿涘牅缍旈幁璁圭礆閿涙ualified_set.remove(dealerID)
		transition.NewCommitteeMembers = removeFromCommittee(transition.NewCommitteeMembers, dealerID)
		transition.DkgN = uint32(len(transition.NewCommitteeMembers))

		// 濡偓閺屻儵妫梽鎰讲鐞涘本鈧嶇窗current_n < initial_t 閺冭绱濊箛鍛淬€忛柌宥呮儙 DKG
		if int(transition.DkgN) < int(transition.DkgThresholdT) {
			transition.DkgStatus = DKGStatusFailed
			logs.Warn("[DKGReveal] insufficient qualified participants after removing %s: current_n=%d < initial_t=%d, DKG failed (restart required)",
				dealerID, transition.DkgN, transition.DkgThresholdT)
		} else {
			// 缂佈呯敾濞翠胶鈻奸敍姘従娴犳牕寮稉搴も偓鍛邦吀缁犳ぞ鍞ゆ０婵囨閻╁瓨甯撮幒鎺楁珟鐠?dealer 閻ㄥ嫯纭€閻?
			// 濞夈劍鍓伴敍姘瑝闂団偓鐟曚線鍣搁弬鎵晸閹存劕顦挎い鐟扮础閿涘苯褰ч棁鈧崷銊ㄤ粵閸?group_pubkey 閺冭埖甯撻梽銈堫嚉 dealer 閻?a_i0
			logs.Info("[DKGReveal] dealer %s disqualified, continuing DKG with remaining participants (n=%d, t=%d)",
				dealerID, transition.DkgN, transition.DkgThresholdT)
		}
	}

	// 閺囧瓨鏌婇幎鏇＄様閻樿埖鈧?
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

	// 閺囧瓨鏌?transition
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

	// 基本参数校验
func verifyRevealInDryRun(share, encRand, ciphertext []byte, commitmentPoints [][]byte, receiverIndex int) bool {
	// 閸╃儤婀伴崣鍌涙殶閺嶏繝鐛?
	if len(share) == 0 || len(encRand) == 0 || len(ciphertext) == 0 || len(commitmentPoints) == 0 {
		return false
	}
	// TODO: 鐎圭偟骞囩€瑰本鏆ｉ惃鍕槕閻礁顒熸宀冪槈閿涘湕CIES 鐎靛棙鏋冩宀冪槈 + Feldman VSS commitment 妤犲矁鐦夐敍?
	// 鏉╂瑩鍣风粻鈧崠鏍ь槱閻炲棴绱濇潻鏂挎礀 true 鐞涖劎銇氭宀冪槈闁俺绻?
	return true
}

// getReceiverIndexInCommittee 娴犲骸顫欓崨妯圭窗娑擃叀骞忛崣鏍ㄥ灇閸涙鍌ㄥ鏇礄1-based閿?
func getReceiverIndexInCommittee(receiverID string, committeeMembers []string) int {
	for i, member := range committeeMembers {
		if member == receiverID {
			return i + 1 // 1-based index
		}
	}
	return 0
}

// removeFromCommittee 娴犲骸顫欓崨妯圭窗娑擃厾些闂勩倖鍨氶崨?
func removeFromCommittee(committee []string, memberID string) []string {
	result := make([]string, 0, len(committee))
	for _, m := range committee {
		if m != memberID {
			result = append(result, m)
		}
	}
	return result
}

// 返回是否需要重启
// 瑜?current_n < initial_t 閺冭绱濊箛鍛淬€忛柌宥呮儙 DKG
// 鏉╂柨娲栭弰顖氭儊闂団偓鐟曚線鍣搁崥?
func checkDkgRestartRule(currentN, initialT uint32) bool {
	return int(currentN) < int(initialT)
}

// - epoch_id += 1（新 epoch）
// - 重新加载完整委员会
// - 清空 disqualified_set
// - 重置 initial_n 和 initial_t
// - dkg_status = COMMITTING
// TODO: 重启时必须更新 DkgCommitDeadline，否则会立即过期。需配合当前区块高度计算。
// - dkg_status = COMMITTING
// TODO: 闁插秴鎯庨弮璺虹箑妞ょ粯娲块弬?DkgCommitDeadline閿涘苯鎯侀崚娆庣窗缁斿宓嗘潻鍥ㄦ埂閵嗗倿娓堕柊宥呮値瑜版挸澧犻崠鍝勬健妤傛ê瀹崇拋锛勭暬閵?
func handleDkgRestart(transition *pb.VaultTransitionState, newEpochID uint64, fullCommittee []string, thresholdRatio float64) {
	transition.EpochId = newEpochID
	transition.NewCommitteeMembers = fullCommittee	// 重新计算门限
	transition.DkgN = uint32(len(fullCommittee))
	// 闁插秵鏌婄拋锛勭暬闂傘劑妾?
	threshold := int(float64(len(fullCommittee)) * thresholdRatio)
	if threshold < 1 {
		threshold = 1
	}
	transition.DkgThresholdT = uint32(threshold)
	transition.DkgStatus = DKGStatusCommitting
	// 濞撳懐鈹栭惄绋垮彠閻樿埖鈧?
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

	// 1. 妤犲矁鐦?VaultTransitionState 鐎涙ê婀?
	transitionKey := keys.KeyFrostVaultTransition(chain, vaultID, epochID)
	transitionData, transitionExists, _ := sv.Get(transitionKey)
	if !transitionExists || len(transitionData) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "transition not found"}, errors.New("transition not found")
	}

	transition, err := unmarshalFrostVaultTransition(transitionData)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to parse transition"}, err
	}

	// 2. 濡偓閺屻儵鐝惔锕€鎷伴悩鑸碘偓渚€妾洪崚?
	height := req.Base.ExecutedHeight
	if height <= transition.DkgSharingDeadline {
		logs.Warn("[DKGValidation] sharing window not closed: height=%d deadline=%d", height, transition.DkgSharingDeadline)
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: fmt.Sprintf("sharing window not closed (height %d <= deadline %d)", height, transition.DkgSharingDeadline)}, nil
	}

	if transition.DkgStatus != DKGStatusSharing && transition.DkgStatus != DKGStatusResolving && transition.DkgStatus != DKGStatusCommitting {
		// 婵″倹鐏夊鑼病閺?KEY_READY閿涘苯鍨弰顖氭鏉╃喎鍩屾潏鍓ф畱闁插秴顦茬拠閿嬬湴閿涘苯绠撶粵澶婎槱閻?
		if transition.DkgStatus == DKGStatusKeyReady {
			return nil, &Receipt{TxID: txID, Status: "SUCCEED", WriteCount: 0}, nil
		}
		// 閸忔湹绮稉宥呮値闁倻娈戦悩鑸碘偓?
		logs.Warn("[DKGValidation] invalid dkg_status: %s", transition.DkgStatus)
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: fmt.Sprintf("invalid dkg_status=%s", transition.DkgStatus)}, nil
	}

	// 3. 妤犲矁鐦夌涵顔款吇閼板懓闊╂禒鏂ょ礄閻╊喖澧犻懞鍌滃仯閻㈠彉绨純鎴犵捕闂勬劕鍩楅弳鍌氬涧閹绘劒姘?Partial Signature閿涘M 閹恒儱褰堟慨鏂挎喅娴兼碍鍨氶崨妯兼畱绾喛顓婚敍?
	if !isInCommittee(sender, transition.NewCommitteeMembers) {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "sender not in committee"}, nil
	}

	// 闁插秵鏌婄拋锛勭暬閸濆牆绗囬獮鍫曠崣鐠囦緤绱濈涵顔荤箽閼哄倻鍋ｉ幍鑳嚡閻ㄥ嫮绮嶉崗顒勬寽娑撳骸褰傞柅浣烘畱閸濆牆绗囨稉鈧懛?
	expectedHash := computeDkgValidationMsgHash(chain, vaultID, epochID, transition.SignAlgo, req.NewGroupPubkey)
	if fmt.Sprintf("0x%x", expectedHash) != fmt.Sprintf("0x%x", req.MsgHash) {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "msg_hash mismatch"}, nil
	}

	logs.Info("[DKGValidation] received validation confirmation from %s for group pubkey %x", sender, req.NewGroupPubkey[:8])
	// 4. 更新 transition 状态

	// 4. 閺囧瓨鏌?transition 閻樿埖鈧?
	transition.DkgStatus = DKGStatusKeyReady
	transition.ValidationStatus = "PASSED"
	transition.ValidationMsgHash = req.MsgHash
	transition.NewGroupPubkey = req.NewGroupPubkey

	updatedTransitionData, err := proto.Marshal(transition)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to marshal transition"}, err
	}

	// 5. 閺囧瓨鏌?VaultState
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
	// 婵″倹鐏夊▽鈩冩箒閺冄冨彆闁姐儻绱欑粭顑跨濞?DKG閿涘绱濋崚娆戞纯閹恒儴绻橀崗?ACTIVE 閻樿埖鈧緤绱濋崥锕€鍨棁鈧憰渚€鈧俺绻冩潻浣盒╂禍銈嗘濠碘偓濞?
	if len(transition.OldGroupPubkey) == 0 {
		vault.Status = VaultStatusActive
		logs.Info("[DKGValidation] Vault %d activated automatically (first epoch)", vaultID)
	}

	// 閺囧瓨鏌婃慨鏂挎喅娴兼碍鍨氶崨姗堢礄娴?transition 閼惧嘲褰囬敍?
	if len(transition.NewCommitteeMembers) > 0 {
		vault.CommitteeMembers = transition.NewCommitteeMembers
	}
	// 鐠佸墽鐤?lifecycle閿涙KG 鐎瑰本鍨氶崥搴礉婵″倹鐏夋潻娆愭Ц妫ｆ牗顐奸崚娑樼紦閿涘ifecycle 娑?KEY_READY
		// transition 中可能已经有 lifecycle 信息，但 VaultState 本身不存储 lifecycle
	if transition.Lifecycle != "" {
		// VaultState 的 status 字段用于表示密钥状态：PENDING -> KEY_READY -> ACTIVE
		// lifecycle 娑撴槒顩﹂崷?VaultTransitionState 娑擃厾顓搁悶?
		// VaultState 閻?status 鐎涙顔岄悽銊ょ艾鐞涖劎銇氱€靛棝鎸滈悩鑸碘偓渚婄窗PENDING -> KEY_READY -> ACTIVE
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

	// 1. 妤犲矁鐦夐弮?Vault 鐎涙ê婀稉鏃傚Ц閹焦顒滅涵?
	oldVaultKey := keys.KeyFrostVaultState(chain, oldVaultID)
	oldVaultData, oldVaultExists, _ := sv.Get(oldVaultKey)
	if !oldVaultExists || len(oldVaultData) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "old vault not found"}, errors.New("old vault not found")
	}

	oldVault, err := unmarshalFrostVaultState(oldVaultData)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to parse old vault"}, err
	}

	// 2. 妤犲矁鐦夌粵鎯ф倳
	if len(req.Signature) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "empty signature"}, errors.New("empty signature")
	}

	// 3. 更新旧 Vault 状态为 DRAINING

	// 3. 閺囧瓨鏌婇弮?Vault 閻樿埖鈧椒璐?DRAINING
	oldVault.Status = VaultLifecycleDraining
	updatedOldVaultData, err := proto.Marshal(oldVault)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to marshal old vault"}, err
	}

	// 4. 妤犲矁鐦夐弬?Vault 鐎涙ê婀稉鏃傚Ц閹椒璐?KEY_READY
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

	// 5. 濠碘偓濞茬粯鏌?Vault
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

// slashBond 缂冩碍鐥呴幎鏇＄様閼板懐娈?bond閿涘牅绮犵拹锔藉煕娴ｆ瑩顤傛稉顓熷⒏闂勩倧绱?
// 鏉╂柨娲?WriteOp 閸掓銆冮悽銊ょ艾閺囧瓨鏌婄拹锔藉煕娴ｆ瑩顤?
func slashBond(sv StateView, address string, bondAmount *big.Int, reason string) ([]WriteOp, error) {
	if bondAmount == nil || bondAmount.Sign() <= 0 {
		return nil, fmt.Errorf("invalid bond amount")
	}

	// 娴ｈ法鏁?FB 娴ｆ粈璐熼崢鐔烘晸娴狅絽绔?
	tokenAddr := "FB"

	// 娴犲骸鍨庣粋璇茬摠閸屻劏顕伴崣鏍︾稇妫?
	fbBal := GetBalance(sv, address, tokenAddr)

	// 娴犲骸褰查悽銊ょ稇妫版繀鑵戦幍锝夋珟 bond閿涘牆顩ч弸婊€缍戞０婵呯瑝鐡掔绱濋崚娆愬⒏闂勩倕鍙忛柈銊ュ讲閻劋缍戞０婵撶礆
	balance, err := parseBalanceStrict("balance", fbBal.Balance)
	if err != nil {
		return nil, fmt.Errorf("invalid balance state: %w", err)
	}

	// 閹碉綁娅?bond閿涘牊娓舵径姘⒏闂勩倕鍙忛柈銊ュ讲閻劋缍戞０婵撶礆
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

	// 娴ｆ瑩顤傚鏌モ偓姘崇箖 SetBalance 娣囨繂鐡ㄩ敍宀勬付鐟曚浇绻戦崶鐐差嚠鎼存梻娈?WriteOp
	balanceKey := keys.KeyBalance(address, tokenAddr)
	balData, _, _ := sv.Get(balanceKey)

	return []WriteOp{{
		Key:         balanceKey,
		Value:       balData,
		SyncStateDB: true,
		Category:    "balance",
	}}, nil
}

// slashMinerStake 缂冩碍鐥呴惌鍨紣閻ㄥ嫯宸濋幎濂稿櫨閿?00% 濮ｆ柧绶ラ敍灞肩矤 MinerLockedBalance 娑擃厽澧搁梽銈忕礆
	// 使用 FB 作为原生代币
func slashMinerStake(sv StateView, address string, reason string) ([]WriteOp, error) {
	// 娴ｈ法鏁?FB 娴ｆ粈璐熼崢鐔烘晸娴狅絽绔?
	tokenAddr := "FB"

	// 娴犲骸鍨庣粋璇茬摠閸屻劏顕伴崣鏍︾稇妫?
	fbBal := GetBalance(sv, address, tokenAddr)

	// 閼惧嘲褰囪ぐ鎾冲鐠愩劍濞傞柌鎴礄100% 缂冩碍鐥呴敍?
	lockedBalance, err := parseBalanceStrict("miner locked balance", fbBal.MinerLockedBalance)
	if err != nil {
		return nil, fmt.Errorf("invalid miner locked balance state: %w", err)
	}
	if lockedBalance.Sign() <= 0 {
		logs.Warn("[slashMinerStake] no locked balance to slash for %s", address)
		return nil, nil // 濞屸剝婀佺拹銊﹀▊闁叉垵褰茬純姘梾閿涘矁绻戦崶鐐碘敄閹垮秳缍?
	}

	// 100% 缂冩碍鐥呴敍姘殺 MinerLockedBalance 濞撳懘娴?
	fbBal.MinerLockedBalance = "0"
	SetBalance(sv, address, tokenAddr, fbBal)

	logs.Info("[slashMinerStake] slashed 100%% stake %s from %s (reason: %s)", lockedBalance.String(), address, reason)

	// 娴ｆ瑩顤傚鏌モ偓姘崇箖 SetBalance 娣囨繂鐡ㄩ敍宀勬付鐟曚浇绻戦崶鐐差嚠鎼存梻娈?WriteOp
	balanceKey := keys.KeyBalance(address, tokenAddr)
	balData, _, _ := sv.Get(balanceKey)

	return []WriteOp{{
		Key:         balanceKey,
		Value:       balData,
		SyncStateDB: true,
		Category:    "balance",
	}}, nil
}
