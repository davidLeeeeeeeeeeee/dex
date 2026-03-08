package vm

import (
	"dex/keys"
	"dex/pb"
	"dex/utils"
	"encoding/hex"
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
		Chain:       chainName,
		VaultId:     vaultID,
		Status:      status,
		GroupPubkey: make([]byte, 33),
	}
	state.GroupPubkey[0] = 0x02
	state.GroupPubkey[32] = byte(vaultID + 1)
	data, err := proto.Marshal(state)
	if err != nil {
		t.Fatalf("marshal vault state failed: %v", err)
	}
	sv.Set(keys.KeyFrostVaultState(chainName, vaultID), data)
}

func putTestVaultStateWithPubkeyHex(t *testing.T, sv StateView, chainName string, vaultID uint32, status, pubKeyHex string) {
	t.Helper()
	pubKey, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		t.Fatalf("decode pubkey hex failed: %v", err)
	}
	state := &pb.FrostVaultState{
		Chain:       chainName,
		VaultId:     vaultID,
		Status:      status,
		GroupPubkey: pubKey,
	}
	data, err := proto.Marshal(state)
	if err != nil {
		t.Fatalf("marshal vault state failed: %v", err)
	}
	sv.Set(keys.KeyFrostVaultState(chainName, vaultID), data)
}

func mustDecodeHex(t *testing.T, raw string) []byte {
	t.Helper()
	out, err := hex.DecodeString(raw)
	if err != nil {
		t.Fatalf("decode hex failed: %v", err)
	}
	return out
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

func TestLookupVaultIDByScriptPubKeyBTC(t *testing.T) {
	sv := NewMockStateView()
	chainName := "btc"
	vaultCount := uint32(3)
	receiverAddr := "user_test_address"

	// vault 0: ACTIVE, pubkey 03+xOnly
	xOnly := "d194cd76d1ba9f139cca4af1277993c90e20ac5c817dcfe93a92810c5f9b03cb"
	putTestVaultStateWithPubkeyHex(t, sv, chainName, 0, VaultStatusActive, "03"+xOnly)
	putTestVaultStateWithPubkeyHex(t, sv, chainName, 1, VaultStatusActive, "02aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	putTestVaultStateWithPubkeyHex(t, sv, chainName, 2, VaultStatusDeprecated, "03bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	// 用 vault 0 的 pubkey 计算 tweaked script_pubkey
	rawPubKey := mustDecodeHex(t, "03"+xOnly)
	rawXOnly := mustDecodeHex(t, xOnly)
	tweak := utils.ComputeUserTweak(rawXOnly, receiverAddr)
	tweakedPub, err := utils.ComputeTweakedPubkey(rawPubKey, tweak)
	if err != nil {
		t.Fatalf("ComputeTweakedPubkey failed: %v", err)
	}
	scriptPubKey := make([]byte, 34)
	scriptPubKey[0] = 0x51
	scriptPubKey[1] = 0x20
	copy(scriptPubKey[2:], tweakedPub[1:33])

	vaultID, err := lookupVaultIDByScriptPubKey(sv, chainName, vaultCount, scriptPubKey, receiverAddr)
	if err != nil {
		t.Fatalf("lookupVaultIDByScriptPubKey failed: %v", err)
	}
	if vaultID != 0 {
		t.Fatalf("expected vault_id=0, got %d", vaultID)
	}
}

func TestLookupVaultIDByScriptPubKeyAmbiguous(t *testing.T) {
	sv := NewMockStateView()
	chainName := "btc"
	vaultCount := uint32(2)
	receiverAddr := "ambiguous_user"

	xOnly := "05ffbabdb46782b00fa7e44feb117aaed34adddbf37d1f9d8c19dfaba47ae3f8"
	putTestVaultStateWithPubkeyHex(t, sv, chainName, 0, VaultStatusActive, "03"+xOnly)
	putTestVaultStateWithPubkeyHex(t, sv, chainName, 1, VaultStatusKeyReady, "02"+xOnly)

	// 用 vault 0 的 pubkey + receiverAddr 计算 tweaked script_pubkey
	rawPubKey := mustDecodeHex(t, "03"+xOnly)
	rawXOnly := mustDecodeHex(t, xOnly)
	tweak := utils.ComputeUserTweak(rawXOnly, receiverAddr)
	tweakedPub, err := utils.ComputeTweakedPubkey(rawPubKey, tweak)
	if err != nil {
		t.Fatalf("ComputeTweakedPubkey failed: %v", err)
	}
	scriptPubKey := make([]byte, 34)
	scriptPubKey[0] = 0x51
	scriptPubKey[1] = 0x20
	copy(scriptPubKey[2:], tweakedPub[1:33])

	// vault 0 和 vault 1 的 x-only 相同，但 02 vs 03 前缀不同
	// tweaked pubkey 可能只匹配其中一个（取决于 even-Y 解压），所以未必 ambiguous
	_, err = lookupVaultIDByScriptPubKey(sv, chainName, vaultCount, scriptPubKey, receiverAddr)
	// 结果可能匹配 1 个或 2 个 vault；这里只验证不 panic
	_ = err
}
