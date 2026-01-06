// vm/frost_vault_transition_signed_handler.go
// FrostVaultTransitionSignedTx handler: 迁移签名产物上链

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

// HandleFrostVaultTransitionSignedTx 处理 Vault 迁移签名
// 当新 Vault 完成 DKG 后，旧 Vault 签名迁移交易，将资产转移到新 Vault
// 校验：
// 1. 验证 old_vault 处于 DRAINING 状态
// 2. 验证签名有效（使用 old_group_pubkey）
// 3. 更新 old_vault 状态到 RETIRED
// 4. 更新 new_vault 状态到 ACTIVE
func (e *Executor) HandleFrostVaultTransitionSignedTx(sender string, tx *pb.FrostVaultTransitionSignedTx, height uint64) error {
	chain := tx.Chain
	oldVaultID := tx.OldVaultId
	newVaultID := tx.NewVaultId
	epochID := tx.EpochId

	logs.Debug("[TransitionSigned] sender=%s chain=%s old_vault=%d new_vault=%d epoch=%d",
		sender, chain, oldVaultID, newVaultID, epochID)

	// 1. 获取旧 Vault 状态
	oldVaultKey := keys.KeyFrostVaultState(chain, oldVaultID)
	oldVault, err := e.DB.GetFrostVaultState(oldVaultKey)
	if err != nil {
		return fmt.Errorf("TransitionSigned: old vault not found: %w", err)
	}

	// 2. 获取 VaultTransitionState
	transitionKey := keys.KeyFrostVaultTransition(chain, newVaultID, epochID)
	transition, err := e.DB.GetFrostVaultTransition(transitionKey)
	if err != nil {
		return fmt.Errorf("TransitionSigned: transition not found: %w", err)
	}

	// 3. 检查 transition 的 lifecycle 是 DRAINING
	if transition.Lifecycle != "DRAINING" {
		// 幂等性检查：如果已经是 RETIRED，直接返回成功
		if transition.Lifecycle == "RETIRED" {
			logs.Debug("[TransitionSigned] already RETIRED, idempotent")
			return nil
		}
		return fmt.Errorf("TransitionSigned: invalid lifecycle=%s, expected DRAINING", transition.Lifecycle)
	}

	// 4. 验证 msg_hash 匹配
	expectedMsgHash := computeTransitionMsgHash(chain, oldVaultID, newVaultID, epochID, transition.OldGroupPubkey, transition.NewGroupPubkey)
	if !bytes.Equal(tx.MsgHash, expectedMsgHash) {
		return fmt.Errorf("TransitionSigned: msg_hash mismatch")
	}

	// 5. 验证签名（使用 old_group_pubkey）
	if len(tx.Signature) == 0 {
		return fmt.Errorf("TransitionSigned: signature is empty")
	}

	valid, err := verifyTransitionSignature(oldVault.SignAlgo, transition.OldGroupPubkey, tx.MsgHash, tx.Signature)
	if err != nil {
		return fmt.Errorf("TransitionSigned: verify failed: %w", err)
	}
	if !valid {
		return fmt.Errorf("TransitionSigned: invalid signature")
	}

	// 6. 更新旧 Vault 状态为 DEPRECATED
	oldVault.Status = "DEPRECATED"
	if err := e.DB.SetFrostVaultState(oldVaultKey, oldVault); err != nil {
		return fmt.Errorf("TransitionSigned: failed to update old vault: %w", err)
	}

	// 7. 获取新 Vault 状态并更新为 ACTIVE
	newVaultKey := keys.KeyFrostVaultState(chain, newVaultID)
	newVault, err := e.DB.GetFrostVaultState(newVaultKey)
	if err != nil {
		return fmt.Errorf("TransitionSigned: new vault not found: %w", err)
	}

	newVault.Status = "ACTIVE"
	newVault.ActiveSinceHeight = height
	if err := e.DB.SetFrostVaultState(newVaultKey, newVault); err != nil {
		return fmt.Errorf("TransitionSigned: failed to update new vault: %w", err)
	}

	// 8. 更新 transition 状态为 RETIRED
	transition.Lifecycle = "RETIRED"
	if err := e.DB.SetFrostVaultTransition(transitionKey, transition); err != nil {
		return fmt.Errorf("TransitionSigned: failed to update transition: %w", err)
	}

	logs.Info("[TransitionSigned] vault transition completed: %d -> %d", oldVaultID, newVaultID)
	return nil
}

// computeTransitionMsgHash 计算迁移消息哈希
func computeTransitionMsgHash(chain string, oldVaultID, newVaultID uint32, epochID uint64, oldPubkey, newPubkey []byte) []byte {
	h := sha256.New()
	h.Write([]byte("frost_vault_transition"))
	h.Write([]byte(chain))
	h.Write([]byte{byte(oldVaultID >> 24), byte(oldVaultID >> 16), byte(oldVaultID >> 8), byte(oldVaultID)})
	h.Write([]byte{byte(newVaultID >> 24), byte(newVaultID >> 16), byte(newVaultID >> 8), byte(newVaultID)})
	h.Write([]byte{byte(epochID >> 56), byte(epochID >> 48), byte(epochID >> 40), byte(epochID >> 32),
		byte(epochID >> 24), byte(epochID >> 16), byte(epochID >> 8), byte(epochID)})
	h.Write(oldPubkey)
	h.Write(newPubkey)
	return h.Sum(nil)
}

// verifyTransitionSignature 验证迁移签名
func verifyTransitionSignature(signAlgo pb.SignAlgo, pubkey, msg, sig []byte) (bool, error) {
	switch signAlgo {
	case pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340:
		if len(pubkey) != 32 {
			return false, fmt.Errorf("invalid pubkey length for BIP340 (expected 32 bytes)")
		}
		return frost.VerifyBIP340(pubkey, msg, sig)
	case pb.SignAlgo_SIGN_ALGO_SCHNORR_ALT_BN128:
		if len(pubkey) != 64 {
			return false, fmt.Errorf("invalid pubkey length for BN128 (expected 64 bytes)")
		}
		return frost.VerifyBN128(pubkey, msg, sig)
	case pb.SignAlgo_SIGN_ALGO_ED25519:
		if len(pubkey) != 32 {
			return false, fmt.Errorf("invalid pubkey length for Ed25519 (expected 32 bytes)")
		}
		return frost.VerifyEd25519(pubkey, msg, sig)
	default:
		return false, fmt.Errorf("unsupported sign_algo: %v", signAlgo)
	}
}
