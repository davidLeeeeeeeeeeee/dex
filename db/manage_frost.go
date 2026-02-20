// db/manage_frost.go
// Frost 相关数据存储管理

package db

import (
	"dex/keys"
	"dex/pb"

	"google.golang.org/protobuf/proto"
)

// ========== VaultTransitionState ==========

// GetFrostVaultTransition 获取 Vault 轮换状态
func (mgr *Manager) GetFrostVaultTransition(key string) (*pb.VaultTransitionState, error) {
	val, err := mgr.Read(key)
	if err != nil {
		return nil, err
	}
	var state pb.VaultTransitionState
	if err := proto.Unmarshal([]byte(val), &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// SetFrostVaultTransition 设置 Vault 轮换状态
func (mgr *Manager) SetFrostVaultTransition(key string, state *pb.VaultTransitionState) error {
	data, err := proto.Marshal(state)
	if err != nil {
		return err
	}
	mgr.EnqueueSet(key, string(data))
	if state != nil {
		activeKey := keys.KeyFrostVaultTransitionActive(state.Chain, state.VaultId, state.EpochId)
		if isActiveFrostTransition(state) {
			mgr.EnqueueSet(activeKey, key)
		} else {
			mgr.EnqueueDelete(activeKey)
		}
	}
	return nil
}

func isActiveFrostTransition(state *pb.VaultTransitionState) bool {
	if state == nil {
		return false
	}
	switch state.DkgStatus {
	case "KEY_READY", "FAILED":
		return false
	}
	if state.Lifecycle == "RETIRED" {
		return false
	}
	return true
}

// ========== DKG Commitment ==========

// GetFrostDkgCommitment 获取 DKG 承诺
func (mgr *Manager) GetFrostDkgCommitment(key string) (*pb.FrostVaultDkgCommitment, error) {
	val, err := mgr.Read(key)
	if err != nil {
		return nil, err
	}
	var commitment pb.FrostVaultDkgCommitment
	if err := proto.Unmarshal([]byte(val), &commitment); err != nil {
		return nil, err
	}
	return &commitment, nil
}

// SetFrostDkgCommitment 设置 DKG 承诺
func (mgr *Manager) SetFrostDkgCommitment(key string, commitment *pb.FrostVaultDkgCommitment) error {
	data, err := proto.Marshal(commitment)
	if err != nil {
		return err
	}
	mgr.EnqueueSet(key, string(data))
	return nil
}

// ========== DKG Share ==========

// GetFrostDkgShare 获取 DKG share
func (mgr *Manager) GetFrostDkgShare(key string) (*pb.FrostVaultDkgShare, error) {
	val, err := mgr.Read(key)
	if err != nil {
		return nil, err
	}
	var share pb.FrostVaultDkgShare
	if err := proto.Unmarshal([]byte(val), &share); err != nil {
		return nil, err
	}
	return &share, nil
}

// SetFrostDkgShare 设置 DKG share
func (mgr *Manager) SetFrostDkgShare(key string, share *pb.FrostVaultDkgShare) error {
	data, err := proto.Marshal(share)
	if err != nil {
		return err
	}
	mgr.EnqueueSet(key, string(data))
	return nil
}

// ========== Vault State ==========

// GetFrostVaultState 获取 Vault 状态
func (mgr *Manager) GetFrostVaultState(key string) (*pb.FrostVaultState, error) {
	val, err := mgr.Read(key)
	if err != nil {
		return nil, err
	}
	var state pb.FrostVaultState
	if err := proto.Unmarshal([]byte(val), &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// SetFrostVaultState 设置 Vault 状态
func (mgr *Manager) SetFrostVaultState(key string, state *pb.FrostVaultState) error {
	data, err := proto.Marshal(state)
	if err != nil {
		return err
	}
	mgr.EnqueueSet(key, string(data))
	return nil
}

// ========== DKG Complaint ==========

// GetFrostDkgComplaint 获取 DKG 投诉
func (mgr *Manager) GetFrostDkgComplaint(key string) (*pb.FrostVaultDkgComplaint, error) {
	val, err := mgr.Read(key)
	if err != nil {
		return nil, err
	}
	var complaint pb.FrostVaultDkgComplaint
	if err := proto.Unmarshal([]byte(val), &complaint); err != nil {
		return nil, err
	}
	return &complaint, nil
}

// SetFrostDkgComplaint 设置 DKG 投诉
func (mgr *Manager) SetFrostDkgComplaint(key string, complaint *pb.FrostVaultDkgComplaint) error {
	data, err := proto.Marshal(complaint)
	if err != nil {
		return err
	}
	mgr.EnqueueSet(key, string(data))
	return nil
}
