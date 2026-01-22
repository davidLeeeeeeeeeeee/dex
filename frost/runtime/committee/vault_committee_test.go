// frost/runtime/vault_committee_test.go
// VaultCommittee 单元测试

package committee

import (
	"testing"
)

func createTestTop10000(n int) []uint64 {
	list := make([]uint64, n)
	for i := 0; i < n; i++ {
		list[i] = uint64(i)
	}
	return list
}

func TestPermute_Deterministic(t *testing.T) {
	list := createTestTop10000(100)
	seed := ComputeSeed(1, "btc")

	// 相同种子应产生相同结果
	result1 := Permute(list, seed)
	result2 := Permute(list, seed)

	if len(result1) != len(result2) {
		t.Error("Results should have same length")
	}

	for i := range result1 {
		if result1[i] != result2[i] {
			t.Errorf("Results differ at index %d: %d vs %d", i, result1[i], result2[i])
		}
	}
}

func TestPermute_DifferentSeed(t *testing.T) {
	list := createTestTop10000(100)
	seed1 := ComputeSeed(1, "btc")
	seed2 := ComputeSeed(2, "btc")

	result1 := Permute(list, seed1)
	result2 := Permute(list, seed2)

	// 不同种子应产生不同结果
	sameCount := 0
	for i := range result1 {
		if result1[i] == result2[i] {
			sameCount++
		}
	}

	// 统计上，相同位置的概率很低
	if sameCount > 10 {
		t.Errorf("Too many same positions: %d/100", sameCount)
	}
}

func TestAssignToVaults(t *testing.T) {
	top10000 := createTestTop10000(100)
	seed := ComputeSeed(1, "btc")
	vaultCount := 5
	committeeSize := 20

	vaults := AssignToVaults(top10000, seed, vaultCount, committeeSize)

	// 验证 Vault 数量
	if len(vaults) != vaultCount {
		t.Errorf("Expected %d vaults, got %d", vaultCount, len(vaults))
	}

	// 验证每个 Vault 的委员会大小
	for i, v := range vaults {
		if len(v) != committeeSize {
			t.Errorf("Vault %d: expected %d members, got %d", i, committeeSize, len(v))
		}
	}

	// 验证没有重复成员
	seen := make(map[uint64]int)
	for vaultIdx, vault := range vaults {
		for _, member := range vault {
			if prevVault, exists := seen[member]; exists {
				t.Errorf("Member %d appears in both vault %d and %d", member, prevVault, vaultIdx)
			}
			seen[member] = vaultIdx
		}
	}
}

func TestAssignToVaults_LargeScale(t *testing.T) {
	// 模拟 Top10000 分配到 50 个 Vault，每个 200 成员
	top10000 := createTestTop10000(10000)
	seed := ComputeSeed(100, "btc")
	vaultCount := 50
	committeeSize := 200

	vaults := AssignToVaults(top10000, seed, vaultCount, committeeSize)

	if len(vaults) != vaultCount {
		t.Errorf("Expected %d vaults, got %d", vaultCount, len(vaults))
	}

	totalMembers := 0
	for _, v := range vaults {
		totalMembers += len(v)
	}

	// 应该正好分配 50 * 200 = 10000 个成员
	if totalMembers != 10000 {
		t.Errorf("Expected 10000 total members, got %d", totalMembers)
	}
}

func TestGetVaultCommittee(t *testing.T) {
	top10000 := createTestTop10000(1000)
	epochID := uint64(42)
	chain := "eth"
	vaultCount := 10
	committeeSize := 100

	// 获取 Vault 0 的委员会
	committee0 := GetVaultCommittee(top10000, epochID, chain, 0, vaultCount, committeeSize)
	if len(committee0) != committeeSize {
		t.Errorf("Expected committee size %d, got %d", committeeSize, len(committee0))
	}

	// 获取 Vault 5 的委员会
	committee5 := GetVaultCommittee(top10000, epochID, chain, 5, vaultCount, committeeSize)
	if len(committee5) != committeeSize {
		t.Errorf("Expected committee size %d, got %d", committeeSize, len(committee5))
	}

	// 两个 Vault 的委员会应该不同
	overlap := 0
	committee5Map := make(map[uint64]bool)
	for _, m := range committee5 {
		committee5Map[m] = true
	}
	for _, m := range committee0 {
		if committee5Map[m] {
			overlap++
		}
	}

	if overlap > 0 {
		t.Errorf("Vaults should not share members, but found %d overlaps", overlap)
	}
}

func TestSortByBitIndex(t *testing.T) {
	unsorted := []uint64{5, 2, 8, 1, 9, 3}
	sorted := SortByBitIndex(unsorted)

	expected := []uint64{1, 2, 3, 5, 8, 9}
	for i, v := range sorted {
		if v != expected[i] {
			t.Errorf("Index %d: expected %d, got %d", i, expected[i], v)
		}
	}
}
