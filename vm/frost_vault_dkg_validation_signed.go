// vm/frost_vault_dkg_validation_signed.go
// FrostVaultDkgValidationSignedTx 辅助函数
// Handler 实现在 frost_vault_dkg_validation_signed_handler.go 中
package vm

import (
	"dex/pb"

	"google.golang.org/protobuf/proto"
)

// ========== 序列化辅助函数 ==========

// marshalFrostVaultTransition 序列化 VaultTransitionState
func marshalFrostVaultTransition(state *pb.VaultTransitionState) ([]byte, error) {
	return proto.Marshal(state)
}

// unmarshalFrostVaultTransition 反序列化 VaultTransitionState
func unmarshalFrostVaultTransition(data []byte) (*pb.VaultTransitionState, error) {
	state := &pb.VaultTransitionState{}
	if err := unmarshalProtoCompat(data, state); err != nil {
		return nil, err
	}
	return state, nil
}

// marshalFrostVaultState 序列化 FrostVaultState
func marshalFrostVaultState(state *pb.FrostVaultState) ([]byte, error) {
	return proto.Marshal(state)
}

// unmarshalFrostVaultState 反序列化 FrostVaultState
func unmarshalFrostVaultState(data []byte) (*pb.FrostVaultState, error) {
	state := &pb.FrostVaultState{}
	if err := unmarshalProtoCompat(data, state); err != nil {
		return nil, err
	}
	return state, nil
}

// marshalFrostDkgCommitment 序列化 FrostVaultDkgCommitment
func marshalFrostDkgCommitment(commitment *pb.FrostVaultDkgCommitment) ([]byte, error) {
	return proto.Marshal(commitment)
}

// unmarshalFrostDkgCommitment 反序列化 FrostVaultDkgCommitment
func unmarshalFrostDkgCommitment(data []byte) (*pb.FrostVaultDkgCommitment, error) {
	commitment := &pb.FrostVaultDkgCommitment{}
	if err := unmarshalProtoCompat(data, commitment); err != nil {
		return nil, err
	}
	return commitment, nil
}

// marshalFrostDkgShare 序列化 FrostVaultDkgShare
func marshalFrostDkgShare(share *pb.FrostVaultDkgShare) ([]byte, error) {
	return proto.Marshal(share)
}

// unmarshalFrostDkgShare 反序列化 FrostVaultDkgShare
func unmarshalFrostDkgShare(data []byte) (*pb.FrostVaultDkgShare, error) {
	share := &pb.FrostVaultDkgShare{}
	if err := unmarshalProtoCompat(data, share); err != nil {
		return nil, err
	}
	return share, nil
}
