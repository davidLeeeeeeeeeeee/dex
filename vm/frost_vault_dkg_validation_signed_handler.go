// vm/frost_vault_dkg_validation_signed_handler.go
// FrostVaultDkgValidationSignedTx handler: 验证签名推进到 KeyReady

package vm

import (
	"bytes"
	"crypto/sha256"
	"dex/frost/core/frost"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"fmt"
)

// HandleFrostVaultDkgValidationSignedTx 处理 DKG 验证签名
// 校验：
// 1. 验证 msg_hash 匹配
// 2. 验证签名有效（使用 new_group_pubkey）
// 3. 推进状态到 KeyReady
func (e *Executor) HandleFrostVaultDkgValidationSignedTx(sender string, tx *pb.FrostVaultDkgValidationSignedTx, height uint64) error {
	chain := tx.Chain
	vaultID := tx.VaultId
	epochID := tx.EpochId

	logs.Debug("[DKGValidationSigned] sender=%s chain=%s vault=%d epoch=%d",
		sender, chain, vaultID, epochID)

	// 1. 获取 VaultTransitionState
	transitionKey := keys.KeyFrostVaultTransition(chain, vaultID, epochID)
	transition, err := e.DB.GetFrostVaultTransition(transitionKey)
	if err != nil {
		return fmt.Errorf("DKGValidationSigned: transition not found: %w", err)
	}

	// 2. 检查状态：只有在 Resolving 或已完成 DKG 后才能验证
	if transition.DkgStatus != DKGStatusResolving && transition.DkgStatus != DKGStatusKeyReady {
		// 允许幂等：如果已经是 KeyReady，直接返回成功
		if transition.DkgStatus == DKGStatusKeyReady && transition.ValidationStatus == "PASSED" {
			logs.Debug("[DKGValidationSigned] already KeyReady, idempotent")
			return nil
		}
		return fmt.Errorf("DKGValidationSigned: invalid dkg_status=%s", transition.DkgStatus)
	}

	// 3. 验证 msg_hash 匹配
	expectedMsgHash := computeDkgValidationMsgHash(chain, vaultID, epochID, transition.SignAlgo, tx.NewGroupPubkey)
	if !bytes.Equal(tx.MsgHash, expectedMsgHash) {
		return fmt.Errorf("DKGValidationSigned: msg_hash mismatch")
	}

	// 4. 验证签名（使用 new_group_pubkey）
	if len(tx.NewGroupPubkey) == 0 {
		return fmt.Errorf("DKGValidationSigned: new_group_pubkey is empty")
	}
	if len(tx.Signature) == 0 {
		return fmt.Errorf("DKGValidationSigned: signature is empty")
	}

	// 按 sign_algo 验签
	valid, err := verifyDkgValidationSignature(transition.SignAlgo, tx.NewGroupPubkey, tx.MsgHash, tx.Signature)
	if err != nil {
		return fmt.Errorf("DKGValidationSigned: verify failed: %w", err)
	}
	if !valid {
		return fmt.Errorf("DKGValidationSigned: invalid signature")
	}

	// 5. 更新状态到 KeyReady
	transition.DkgStatus = DKGStatusKeyReady
	transition.ValidationStatus = "PASSED"
	transition.NewGroupPubkey = tx.NewGroupPubkey
	transition.ValidationMsgHash = tx.MsgHash

	if err := e.DB.SetFrostVaultTransition(transitionKey, transition); err != nil {
		return fmt.Errorf("DKGValidationSigned: failed to update transition: %w", err)
	}

	// 6. 更新 VaultState（激活新密钥）
	vaultStateKey := keys.KeyFrostVaultState(chain, vaultID)
	vaultState, err := e.DB.GetFrostVaultState(vaultStateKey)
	if err != nil {
		// 首次创建
		vaultState = &pb.FrostVaultState{
			VaultId:  vaultID,
			Chain:    chain,
			SignAlgo: transition.SignAlgo,
		}
	}

	vaultState.KeyEpoch = epochID
	vaultState.GroupPubkey = tx.NewGroupPubkey
	vaultState.Status = "KEY_READY"
	vaultState.ActiveSinceHeight = height
	vaultState.CommitteeMembers = transition.NewCommitteeMembers

	if err := e.DB.SetFrostVaultState(vaultStateKey, vaultState); err != nil {
		return fmt.Errorf("DKGValidationSigned: failed to update vault state: %w", err)
	}

	logs.Debug("[DKGValidationSigned] vault %d epoch %d -> KeyReady", vaultID, epochID)
	return nil
}

// computeDkgValidationMsgHash 计算 DKG 验证消息哈希
func computeDkgValidationMsgHash(chain string, vaultID uint32, epochID uint64, signAlgo pb.SignAlgo, groupPubkey []byte) []byte {
	h := sha256.New()
	h.Write([]byte("frost_vault_dkg_validation"))
	h.Write([]byte(chain))
	h.Write([]byte{byte(vaultID >> 24), byte(vaultID >> 16), byte(vaultID >> 8), byte(vaultID)})
	h.Write([]byte{byte(epochID >> 56), byte(epochID >> 48), byte(epochID >> 40), byte(epochID >> 32),
		byte(epochID >> 24), byte(epochID >> 16), byte(epochID >> 8), byte(epochID)})
	h.Write([]byte{byte(signAlgo)})
	h.Write(groupPubkey)
	return h.Sum(nil)
}

// verifyDkgValidationSignature 验证 DKG 验证签名
func verifyDkgValidationSignature(signAlgo pb.SignAlgo, pubkey, msg, sig []byte) (bool, error) {
	switch signAlgo {
	case pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340:
		return frost.VerifyBIP340(pubkey, msg, sig)
	default:
		// 其他算法暂不支持
		return false, fmt.Errorf("unsupported sign_algo: %v", signAlgo)
	}
}
