package vm

import (
	"bytes"
	"dex/frost/core/curve"
	"dex/keys"
	"dex/pb"
	"math/big"
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestFrostVaultDkgValidationSignedRejectsMismatchedGroupPubkey(t *testing.T) {
	sv := NewMockStateView()

	chain := "btc"
	vaultID := uint32(7)
	epochID := uint64(3)
	committee := []string{"minerA", "minerB"}
	signAlgo := pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340

	_, expectedPubkey := seedDKGValidationState(t, sv, chain, vaultID, epochID, committee, signAlgo)

	grp := curve.NewSecp256k1Group()
	wrongPubkey := grp.SerializePoint(grp.ScalarBaseMult(big.NewInt(9)))
	if bytes.Equal(wrongPubkey, expectedPubkey) {
		t.Fatalf("wrong pubkey unexpectedly equals expected pubkey")
	}

	msgHash := computeDkgValidationMsgHash(chain, vaultID, epochID, signAlgo, wrongPubkey)
	tx := &pb.AnyTx{
		Content: &pb.AnyTx_FrostVaultDkgValidationSignedTx{
			FrostVaultDkgValidationSignedTx: &pb.FrostVaultDkgValidationSignedTx{
				Base: &pb.BaseMessage{
					TxId:           "tx_dkg_validation_bad_pubkey",
					FromAddress:    committee[0],
					ExecutedHeight: 50,
				},
				Chain:          chain,
				VaultId:        vaultID,
				EpochId:        epochID,
				MsgHash:        msgHash,
				NewGroupPubkey: wrongPubkey,
				Signature:      []byte{0x01},
			},
		},
	}

	h := &FrostVaultDkgValidationSignedTxHandler{}
	_, receipt, err := h.DryRun(tx, sv)
	if err != nil {
		t.Fatalf("DryRun returned error: %v", err)
	}
	if receipt == nil || receipt.Status != "FAILED" {
		t.Fatalf("expected FAILED receipt, got %+v", receipt)
	}
	if receipt.Error != "group_pubkey mismatch with commitments" {
		t.Fatalf("unexpected receipt error: %s", receipt.Error)
	}
}

func TestFrostVaultDkgValidationSignedUsesDerivedGroupPubkey(t *testing.T) {
	sv := NewMockStateView()

	chain := "btc"
	vaultID := uint32(8)
	epochID := uint64(4)
	committee := []string{"minerA", "minerB"}
	signAlgo := pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340

	transitionKey, expectedPubkey := seedDKGValidationState(t, sv, chain, vaultID, epochID, committee, signAlgo)

	msgHash := computeDkgValidationMsgHash(chain, vaultID, epochID, signAlgo, expectedPubkey)
	tx := &pb.AnyTx{
		Content: &pb.AnyTx_FrostVaultDkgValidationSignedTx{
			FrostVaultDkgValidationSignedTx: &pb.FrostVaultDkgValidationSignedTx{
				Base: &pb.BaseMessage{
					TxId:           "tx_dkg_validation_ok",
					FromAddress:    committee[0],
					ExecutedHeight: 50,
				},
				Chain:          chain,
				VaultId:        vaultID,
				EpochId:        epochID,
				MsgHash:        msgHash,
				NewGroupPubkey: expectedPubkey,
				Signature:      []byte{0x02},
			},
		},
	}

	h := &FrostVaultDkgValidationSignedTxHandler{}
	_, receipt, err := h.DryRun(tx, sv)
	if err != nil {
		t.Fatalf("DryRun returned error: %v", err)
	}
	if receipt == nil || receipt.Status != "SUCCEED" {
		t.Fatalf("expected SUCCEED receipt, got %+v", receipt)
	}

	updatedTransitionRaw, exists, _ := sv.Get(transitionKey)
	if !exists || len(updatedTransitionRaw) == 0 {
		t.Fatalf("updated transition missing")
	}
	var updatedTransition pb.VaultTransitionState
	if err := proto.Unmarshal(updatedTransitionRaw, &updatedTransition); err != nil {
		t.Fatalf("unmarshal updated transition: %v", err)
	}
	if !bytes.Equal(updatedTransition.NewGroupPubkey, expectedPubkey) {
		t.Fatalf("transition new_group_pubkey mismatch")
	}

	vaultKey := keys.KeyFrostVaultState(chain, vaultID)
	vaultRaw, exists, _ := sv.Get(vaultKey)
	if !exists || len(vaultRaw) == 0 {
		t.Fatalf("vault state missing")
	}
	var vault pb.FrostVaultState
	if err := proto.Unmarshal(vaultRaw, &vault); err != nil {
		t.Fatalf("unmarshal vault state: %v", err)
	}
	if !bytes.Equal(vault.GroupPubkey, expectedPubkey) {
		t.Fatalf("vault group_pubkey mismatch")
	}
}

func seedDKGValidationState(t *testing.T, sv *MockStateView, chain string, vaultID uint32, epochID uint64, committee []string, signAlgo pb.SignAlgo) (string, []byte) {
	t.Helper()

	grp := curve.NewSecp256k1Group()
	ai0A := grp.SerializePoint(grp.ScalarBaseMult(big.NewInt(2)))
	ai0B := grp.SerializePoint(grp.ScalarBaseMult(big.NewInt(3)))
	sum := grp.Add(grp.DecompressPoint(ai0A), grp.DecompressPoint(ai0B))
	expectedPubkey := grp.SerializePoint(sum)

	transition := &pb.VaultTransitionState{
		Chain:               chain,
		VaultId:             vaultID,
		EpochId:             epochID,
		SignAlgo:            signAlgo,
		DkgStatus:           DKGStatusSharing,
		DkgSharingDeadline:  10,
		NewCommitteeMembers: committee,
		OldGroupPubkey:      []byte{0x01},
	}
	transitionKey := keys.KeyFrostVaultTransition(chain, vaultID, epochID)
	transitionData, err := proto.Marshal(transition)
	if err != nil {
		t.Fatalf("marshal transition: %v", err)
	}
	sv.Set(transitionKey, transitionData)

	commitA := &pb.FrostVaultDkgCommitment{
		Chain:        chain,
		VaultId:      vaultID,
		EpochId:      epochID,
		MinerAddress: committee[0],
		SignAlgo:     signAlgo,
		AI0:          ai0A,
	}
	commitB := &pb.FrostVaultDkgCommitment{
		Chain:        chain,
		VaultId:      vaultID,
		EpochId:      epochID,
		MinerAddress: committee[1],
		SignAlgo:     signAlgo,
		AI0:          ai0B,
	}

	commitAData, err := proto.Marshal(commitA)
	if err != nil {
		t.Fatalf("marshal commitA: %v", err)
	}
	commitBData, err := proto.Marshal(commitB)
	if err != nil {
		t.Fatalf("marshal commitB: %v", err)
	}
	sv.Set(keys.KeyFrostVaultDkgCommit(chain, vaultID, epochID, committee[0]), commitAData)
	sv.Set(keys.KeyFrostVaultDkgCommit(chain, vaultID, epochID, committee[1]), commitBData)

	return transitionKey, expectedPubkey
}
