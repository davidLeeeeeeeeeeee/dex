package vm

import (
	"dex/keys"
	"dex/pb"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
)

func putTestVaultConfig(t *testing.T, sv StateView, chainName string, vaultCount uint32) {
	t.Helper()
	cfg := &pb.FrostVaultConfig{
		Chain:          chainName,
		VaultCount:     vaultCount,
		CommitteeSize:  4,
		ThresholdRatio: 0.67,
	}
	data, err := proto.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal vault config failed: %v", err)
	}
	sv.Set(keys.KeyFrostVaultConfig(chainName, 0), data)
}

func putTestVaultState(t *testing.T, sv StateView, chainName string, vaultID uint32, status string) {
	t.Helper()
	state := &pb.FrostVaultState{
		Chain:   chainName,
		VaultId: vaultID,
		Status:  status,
	}
	data, err := proto.Marshal(state)
	if err != nil {
		t.Fatalf("marshal vault state failed: %v", err)
	}
	sv.Set(keys.KeyFrostVaultState(chainName, vaultID), data)
}

func TestResolveVaultChainAndCountCaseInsensitive(t *testing.T) {
	sv := NewMockStateView()
	putTestVaultConfig(t, sv, "eth", 3)

	chainName, vaultCount, err := resolveVaultChainAndCount(sv, "ETH")
	if err != nil {
		t.Fatalf("resolveVaultChainAndCount failed: %v", err)
	}
	if chainName != "eth" {
		t.Fatalf("expected resolved chain eth, got %s", chainName)
	}
	if vaultCount != 3 {
		t.Fatalf("expected vault count 3, got %d", vaultCount)
	}
}

func TestResolveVaultChainAndCountRequiresOnChainConfig(t *testing.T) {
	sv := NewMockStateView()

	_, _, err := resolveVaultChainAndCount(sv, "eth")
	if err == nil {
		t.Fatal("expected missing vault config error")
	}
	if !strings.Contains(err.Error(), "vault config not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAllocateVaultIDWithStateCheckOnlyReadyOrActive(t *testing.T) {
	sv := NewMockStateView()
	chainName := "eth"
	vaultCount := uint32(5)
	requestID := "req-ready-only"

	initial := allocateVaultID(requestID, vaultCount)
	putTestVaultState(t, sv, chainName, initial, "PENDING")
	putTestVaultState(t, sv, chainName, (initial+1)%vaultCount, VaultLifecycleDraining)
	expected := (initial + 2) % vaultCount
	putTestVaultState(t, sv, chainName, expected, VaultStatusKeyReady)
	putTestVaultState(t, sv, chainName, (initial+3)%vaultCount, VaultStatusActive)

	actual, err := allocateVaultIDWithStateCheck(sv, chainName, requestID, vaultCount)
	if err != nil {
		t.Fatalf("allocateVaultIDWithStateCheck failed: %v", err)
	}
	if actual != expected {
		t.Fatalf("expected vault %d, got %d", expected, actual)
	}
}

func TestAllocateVaultIDWithStateCheckNoEligibleVault(t *testing.T) {
	sv := NewMockStateView()
	chainName := "eth"
	vaultCount := uint32(3)
	requestID := "req-no-eligible"

	putTestVaultState(t, sv, chainName, 0, "PENDING")
	putTestVaultState(t, sv, chainName, 1, VaultLifecycleDraining)
	putTestVaultState(t, sv, chainName, 2, VaultStatusDeprecated)

	_, err := allocateVaultIDWithStateCheck(sv, chainName, requestID, vaultCount)
	if err == nil {
		t.Fatal("expected no allocatable vault error")
	}
	if !strings.Contains(err.Error(), "no allocatable vault") {
		t.Fatalf("unexpected error: %v", err)
	}
}
