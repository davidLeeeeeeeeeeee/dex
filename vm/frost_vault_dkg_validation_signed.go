// vm/frost_vault_dkg_validation_signed.go
// FrostVaultDkgValidationSignedTx 杈呭姪鍑芥暟
// Handler 瀹炵幇鍦?frost_vault_dkg_validation_signed_handler.go 涓?
package vm

import (
	"dex/pb"

	"google.golang.org/protobuf/proto"
)

// ========== 搴忓垪鍖栬緟鍔╁嚱鏁?==========

// marshalFrostVaultTransition 搴忓垪鍖?VaultTransitionState
func marshalFrostVaultTransition(state *pb.VaultTransitionState) ([]byte, error) {
	return proto.Marshal(state)
}

// unmarshalFrostVaultTransition 鍙嶅簭鍒楀寲 VaultTransitionState
func unmarshalFrostVaultTransition(data []byte) (*pb.VaultTransitionState, error) {
	state := &pb.VaultTransitionState{}
	if err := unmarshalProtoCompat(data, state); err != nil {
		return nil, err
	}
	return state, nil
}

// marshalFrostVaultState 搴忓垪鍖?FrostVaultState
func marshalFrostVaultState(state *pb.FrostVaultState) ([]byte, error) {
	return proto.Marshal(state)
}

// unmarshalFrostVaultState 鍙嶅簭鍒楀寲 FrostVaultState
func unmarshalFrostVaultState(data []byte) (*pb.FrostVaultState, error) {
	state := &pb.FrostVaultState{}
	if err := unmarshalProtoCompat(data, state); err != nil {
		return nil, err
	}
	return state, nil
}

// marshalFrostDkgCommitment 搴忓垪鍖?FrostVaultDkgCommitment
func marshalFrostDkgCommitment(commitment *pb.FrostVaultDkgCommitment) ([]byte, error) {
	return proto.Marshal(commitment)
}

// unmarshalFrostDkgCommitment 鍙嶅簭鍒楀寲 FrostVaultDkgCommitment
func unmarshalFrostDkgCommitment(data []byte) (*pb.FrostVaultDkgCommitment, error) {
	commitment := &pb.FrostVaultDkgCommitment{}
	if err := unmarshalProtoCompat(data, commitment); err != nil {
		return nil, err
	}
	return commitment, nil
}

// marshalFrostDkgShare 搴忓垪鍖?FrostVaultDkgShare
func marshalFrostDkgShare(share *pb.FrostVaultDkgShare) ([]byte, error) {
	return proto.Marshal(share)
}

// unmarshalFrostDkgShare 鍙嶅簭鍒楀寲 FrostVaultDkgShare
func unmarshalFrostDkgShare(data []byte) (*pb.FrostVaultDkgShare, error) {
	share := &pb.FrostVaultDkgShare{}
	if err := unmarshalProtoCompat(data, share); err != nil {
		return nil, err
	}
	return share, nil
}
