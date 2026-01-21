// vm/frost_vault_dkg_commit_handler.go
// FrostVaultDkgCommitTx handler: DKG 承诺点上链存储与校验

package vm

import (
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"fmt"
)

// DKG 状态常量
const (
	DKGStatusNotStarted = "NOT_STARTED"
	DKGStatusCommitting = "COMMITTING"
	DKGStatusSharing    = "SHARING"
	DKGStatusResolving  = "RESOLVING"
	DKGStatusKeyReady   = "KEY_READY"
	DKGStatusFailed     = "FAILED"
)

// HandleFrostVaultDkgCommitTx 处理 DKG 承诺点提交
// 校验：
// 1. tx sender 必须属于该 Vault 的 new_committee_members[]
// 2. dkg_status == Committing
// 3. 同一 (chain, vault_id, epoch_id) 每个 sender 只能登记一次
// 4. sign_algo 必须与 VaultConfig 一致
func (e *Executor) HandleFrostVaultDkgCommitTx(sender string, tx *pb.FrostVaultDkgCommitTx, height uint64) error {
	chain := tx.Chain
	vaultID := tx.VaultId
	epochID := tx.EpochId
	signAlgo := tx.SignAlgo

	logs.Debug("[DKGCommit] sender=%s chain=%s vault=%d epoch=%d algo=%v",
		sender, chain, vaultID, epochID, signAlgo)

	// 1. 检查是否已存在承诺（幂等性）
	existingKey := keys.KeyFrostVaultDkgCommit(chain, vaultID, epochID, sender)
	existing, err := e.DB.GetFrostDkgCommitment(existingKey)
	if err == nil && existing != nil {
		logs.Debug("[DKGCommit] already committed, idempotent")
		return nil // 幂等
	}

	// 2. 验证 VaultTransitionState 存在且状态正确
	transitionKey := keys.KeyFrostVaultTransition(chain, vaultID, epochID)
	transition, err := e.DB.GetFrostVaultTransition(transitionKey)
	if err != nil {
		return fmt.Errorf("DKGCommit: transition not found: %w", err)
	}

	// 3. 检查 DKG 状态
	if transition.DkgStatus != DKGStatusCommitting && transition.DkgStatus != DKGStatusNotStarted {
		// 如果 DKG 已经处于后期状态，忽略这笔交易
		logs.Warn("[DKGCommit] skipping late commit tx from %s: dkg_status=%s", sender, transition.DkgStatus)
		return nil
	}

	// 如果当前是 NOT_STARTED，则在接收到第一个 CommitTx 时推进到 COMMITTING 状态
	if transition.DkgStatus == DKGStatusNotStarted {
		transition.DkgStatus = DKGStatusCommitting
		if err := e.DB.SetFrostVaultTransition(transitionKey, transition); err != nil {
			return fmt.Errorf("DKGCommit: failed to update transition status: %w", err)
		}
		logs.Info("[DKGCommit] transition %s status moved to COMMITTING", transitionKey)
	}

	// 4. 检查 sign_algo 一致性
	if transition.SignAlgo != signAlgo {
		return fmt.Errorf("DKGCommit: sign_algo mismatch: tx=%v, expected=%v", signAlgo, transition.SignAlgo)
	}

	// 5. 检查 sender 是否在 new_committee_members 中
	if !isInCommittee(sender, transition.NewCommitteeMembers) {
		return fmt.Errorf("DKGCommit: sender %s not in new_committee_members", sender)
	}

	// 6. 验证承诺点数据
	if len(tx.CommitmentPoints) == 0 {
		return fmt.Errorf("DKGCommit: commitment_points is empty")
	}
	if len(tx.AI0) == 0 {
		return fmt.Errorf("DKGCommit: a_i0 is empty")
	}

	// 7. 存储承诺
	commitment := &pb.FrostVaultDkgCommitment{
		Chain:            chain,
		VaultId:          vaultID,
		EpochId:          epochID,
		MinerAddress:     sender,
		SignAlgo:         signAlgo,
		CommitmentPoints: tx.CommitmentPoints,
		AI0:              tx.AI0,
		CommitHeight:     height,
	}

	if err := e.DB.SetFrostDkgCommitment(existingKey, commitment); err != nil {
		return fmt.Errorf("DKGCommit: failed to store commitment: %w", err)
	}

	logs.Debug("[DKGCommit] stored commitment for sender=%s", sender)
	return nil
}

// isInCommittee 检查地址是否在委员会列表中
func isInCommittee(address string, members []string) bool {
	for _, m := range members {
		if m == address {
			return true
		}
	}
	return false
}
