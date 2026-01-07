// vm/frost_vault_init_test.go
// 测试 VaultState 初始化

package vm

import (
	"dex/keys"
	"dex/pb"
	"testing"

	"google.golang.org/protobuf/proto"
)

// TestInitVaultStates 测试 VaultState 初始化
func TestInitVaultStates(t *testing.T) {
	sv := NewMockStateView()
	chain := "btc"
	epochID := uint64(1)

	// 1. 创建 VaultConfig
	vaultCfg := &pb.FrostVaultConfig{
		Chain:        chain,
		VaultCount:   3,
		CommitteeSize: 5,
		ThresholdRatio: 0.67,
		SignAlgo:     pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
	}
	vaultCfgKey := keys.KeyFrostVaultConfig(chain, 0)
	vaultCfgData, _ := proto.Marshal(vaultCfg)
	sv.Set(vaultCfgKey, vaultCfgData)

	// 2. 创建 Top10000（需要至少 15 个成员：3 个 Vault * 5 个成员）
	top10000 := &pb.FrostTop10000{
		Indices: []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
		Addresses: []string{
			"0x0001", "0x0002", "0x0003", "0x0004", "0x0005",
			"0x0006", "0x0007", "0x0008", "0x0009", "0x0010",
			"0x0011", "0x0012", "0x0013", "0x0014", "0x0015",
		},
	}
	top10000Key := keys.KeyFrostTop10000()
	top10000Data, _ := proto.Marshal(top10000)
	sv.Set(top10000Key, top10000Data)

	// 3. 调用 InitVaultStates
	err := InitVaultStates(sv, chain, epochID)
	if err != nil {
		t.Fatalf("InitVaultStates failed: %v", err)
	}

	// 4. 验证每个 Vault 的状态都已创建
	for vaultID := uint32(0); vaultID < 3; vaultID++ {
		vaultStateKey := keys.KeyFrostVaultState(chain, vaultID)
		vaultStateData, exists, err := sv.Get(vaultStateKey)
		if err != nil {
			t.Fatalf("failed to get vault state: %v", err)
		}
		if !exists {
			t.Fatalf("vault state not found for vault %d", vaultID)
		}

		var vaultState pb.FrostVaultState
		if err := proto.Unmarshal(vaultStateData, &vaultState); err != nil {
			t.Fatalf("failed to unmarshal vault state: %v", err)
		}

		// 验证基本字段
		if vaultState.VaultId != vaultID {
			t.Errorf("vault ID mismatch: expected %d, got %d", vaultID, vaultState.VaultId)
		}
		if vaultState.Chain != chain {
			t.Errorf("chain mismatch: expected %s, got %s", chain, vaultState.Chain)
		}
		if vaultState.KeyEpoch != epochID {
			t.Errorf("epoch mismatch: expected %d, got %d", epochID, vaultState.KeyEpoch)
		}
		if vaultState.Status != "PENDING" {
			t.Errorf("status mismatch: expected PENDING, got %s", vaultState.Status)
		}
		if len(vaultState.CommitteeMembers) != 5 {
			t.Errorf("committee size mismatch: expected 5, got %d", len(vaultState.CommitteeMembers))
		}
	}

	// 5. 验证幂等性（再次调用应该不报错）
	err = InitVaultStates(sv, chain, epochID)
	if err != nil {
		t.Fatalf("InitVaultStates (second call) failed: %v", err)
	}
}

// TestInitVaultStatesMissingConfig 测试缺少配置的情况
func TestInitVaultStatesMissingConfig(t *testing.T) {
	sv := NewMockStateView()
	chain := "btc"
	epochID := uint64(1)

	// 不创建 VaultConfig，直接调用 InitVaultStates
	err := InitVaultStates(sv, chain, epochID)
	if err == nil {
		t.Fatal("expected error when VaultConfig is missing")
	}
}

// TestInitVaultStatesMissingTop10000 测试缺少 Top10000 的情况
func TestInitVaultStatesMissingTop10000(t *testing.T) {
	sv := NewMockStateView()
	chain := "btc"
	epochID := uint64(1)

	// 创建 VaultConfig，但不创建 Top10000
	vaultCfg := &pb.FrostVaultConfig{
		Chain:        chain,
		VaultCount:   3,
		CommitteeSize: 5,
		ThresholdRatio: 0.67,
		SignAlgo:     pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
	}
	vaultCfgKey := keys.KeyFrostVaultConfig(chain, 0)
	vaultCfgData, _ := proto.Marshal(vaultCfg)
	sv.Set(vaultCfgKey, vaultCfgData)

	// 调用 InitVaultStates
	err := InitVaultStates(sv, chain, epochID)
	if err == nil {
		t.Fatal("expected error when Top10000 is missing")
	}
}
