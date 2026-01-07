// handlers/frost_query_handlers_test.go
// 测试 Frost 查询 API

package handlers

import (
	"dex/db"
	"dex/keys"
	"dex/pb"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/protobuf/proto"
)

// setupTestHandlerManager 创建测试用的 HandlerManager
func setupTestHandlerManager(t *testing.T) *HandlerManager {
	// 创建临时数据库
	tempDir := t.TempDir()
	dbMgr, err := db.NewManager(tempDir)
	if err != nil {
		t.Fatalf("failed to create db manager: %v", err)
	}

	// 创建 HandlerManager
	hm := NewHandlerManager(dbMgr, nil, "8080", "0x0001", nil, nil)
	return hm
}

// TestHandleGetVaultGroupPubKey 测试 GetVaultGroupPubKey API
func TestHandleGetVaultGroupPubKey(t *testing.T) {
	hm := setupTestHandlerManager(t)

	// 创建测试数据
	vaultState := &pb.FrostVaultState{
		VaultId:    1,
		Chain:      "btc",
		KeyEpoch:   1,
		GroupPubkey: []byte{0x01, 0x02, 0x03, 0x04},
		SignAlgo:   pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
		Status:     "KEY_READY",
	}

	vaultKey := keys.KeyFrostVaultState("btc", 1)
	vaultData, _ := proto.Marshal(vaultState)
	hm.dbManager.EnqueueSet(vaultKey, string(vaultData))
	hm.dbManager.ForceFlush()

	// 创建请求
	req := httptest.NewRequest("GET", "/frost/vault/group_pubkey?chain=btc&vault_id=1", nil)
	w := httptest.NewRecorder()

	// 执行处理
	hm.HandleGetVaultGroupPubKey(w, req)

	// 验证响应
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	// 验证响应内容（protobuf）
	var resp pb.GetVaultGroupPubKeyResponse
	if err := proto.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Chain != "btc" {
		t.Errorf("expected chain btc, got %s", resp.Chain)
	}
	if resp.VaultId != 1 {
		t.Errorf("expected vault_id 1, got %d", resp.VaultId)
	}
	if resp.GroupPubkey != hex.EncodeToString(vaultState.GroupPubkey) {
		t.Errorf("expected group_pubkey %s, got %s", hex.EncodeToString(vaultState.GroupPubkey), resp.GroupPubkey)
	}
}

// TestHandleGetVaultGroupPubKeyNotFound 测试 Vault 不存在的情况
func TestHandleGetVaultGroupPubKeyNotFound(t *testing.T) {
	hm := setupTestHandlerManager(t)

	req := httptest.NewRequest("GET", "/frost/vault/group_pubkey?chain=btc&vault_id=999", nil)
	w := httptest.NewRecorder()

	hm.HandleGetVaultGroupPubKey(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", w.Code)
	}
}

// TestHandleGetVaultTransitionStatus 测试 GetVaultTransitionStatus API
func TestHandleGetVaultTransitionStatus(t *testing.T) {
	hm := setupTestHandlerManager(t)

	// 创建测试数据
	transition := &pb.VaultTransitionState{
		Chain:               "btc",
		VaultId:             1,
		EpochId:             1,
		SignAlgo:            pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
		TriggerHeight:       100,
		OldCommitteeMembers: []string{"0x0001", "0x0002"},
		NewCommitteeMembers: []string{"0x0003", "0x0004"},
		DkgStatus:           "COMMITTING",
		DkgSessionId:        "btc_1_1",
		DkgThresholdT:       2,
		DkgN:                4,
		DkgCommitDeadline:   200,
		DkgDisputeDeadline:  300,
		OldGroupPubkey:      []byte{0x01, 0x02},
		NewGroupPubkey:      []byte{0x03, 0x04},
		ValidationStatus:    "NOT_STARTED",
		Lifecycle:           "ACTIVE",
	}

	transitionKey := keys.KeyFrostVaultTransition("btc", 1, 1)
	transitionData, _ := proto.Marshal(transition)
	hm.dbManager.EnqueueSet(transitionKey, string(transitionData))
	hm.dbManager.ForceFlush()

	// 创建请求
	req := httptest.NewRequest("GET", "/frost/vault/transition_status?chain=btc&vault_id=1&epoch_id=1", nil)
	w := httptest.NewRecorder()

	// 执行处理
	hm.HandleGetVaultTransitionStatus(w, req)

	// 验证响应
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp pb.GetVaultTransitionStatusResponse
	if err := proto.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Chain != "btc" {
		t.Errorf("expected chain btc, got %s", resp.Chain)
	}
	if resp.VaultId != 1 {
		t.Errorf("expected vault_id 1, got %d", resp.VaultId)
	}
	if resp.DkgStatus != "COMMITTING" {
		t.Errorf("expected dkg_status COMMITTING, got %s", resp.DkgStatus)
	}
	if len(resp.NewCommitteeMembers) != 2 {
		t.Errorf("expected 2 new committee members, got %d", len(resp.NewCommitteeMembers))
	}
}

// TestHandleGetVaultDkgCommitments 测试 GetVaultDkgCommitments API
func TestHandleGetVaultDkgCommitments(t *testing.T) {
	hm := setupTestHandlerManager(t)

	// 创建测试数据
	commit1 := &pb.FrostVaultDkgCommitment{
		Chain:            "btc",
		VaultId:          1,
		EpochId:          1,
		MinerAddress:     "0x0001",
		SignAlgo:         pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
		CommitmentPoints: [][]byte{{0x01, 0x02}, {0x03, 0x04}},
		CommitHeight:     100,
	}

	commit2 := &pb.FrostVaultDkgCommitment{
		Chain:            "btc",
		VaultId:          1,
		EpochId:          1,
		MinerAddress:     "0x0002",
		SignAlgo:         pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
		CommitmentPoints: [][]byte{{0x05, 0x06}},
		CommitHeight:     101,
	}

	commitKey1 := keys.KeyFrostVaultDkgCommit("btc", 1, 1, "0x0001")
	commitKey2 := keys.KeyFrostVaultDkgCommit("btc", 1, 1, "0x0002")
	commitData1, _ := proto.Marshal(commit1)
	commitData2, _ := proto.Marshal(commit2)
	hm.dbManager.EnqueueSet(commitKey1, string(commitData1))
	hm.dbManager.EnqueueSet(commitKey2, string(commitData2))
	hm.dbManager.ForceFlush()

	// 创建请求
	req := httptest.NewRequest("GET", "/frost/vault/dkg_commitments?chain=btc&vault_id=1&epoch_id=1", nil)
	w := httptest.NewRecorder()

	// 执行处理
	hm.HandleGetVaultDkgCommitments(w, req)

	// 验证响应
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp pb.GetVaultDkgCommitmentsResponse
	if err := proto.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Total != 2 {
		t.Errorf("expected 2 commitments, got %d", resp.Total)
	}
	if len(resp.Commitments) != 2 {
		t.Errorf("expected 2 commitments in array, got %d", len(resp.Commitments))
	}

	// 验证第一个 commitment
	if resp.Commitments[0].Participant != "0x0001" && resp.Commitments[0].Participant != "0x0002" {
		t.Errorf("unexpected participant: %s", resp.Commitments[0].Participant)
	}
}

// TestHandleDownloadSignedPackage 测试 DownloadSignedPackage API
func TestHandleDownloadSignedPackage(t *testing.T) {
	hm := setupTestHandlerManager(t)

	// 创建测试数据
	signedPkg := &pb.FrostSignedPackage{
		JobId:              "job_123",
		Idx:                0,
		SignedPackageBytes: []byte{0x01, 0x02, 0x03, 0x04},
		SubmitHeight:       100,
		Submitter:          "0x0001",
	}

	pkgKey := keys.KeyFrostSignedPackage("job_123", 0)
	pkgData, _ := proto.Marshal(signedPkg)
	hm.dbManager.EnqueueSet(pkgKey, string(pkgData))
	hm.dbManager.ForceFlush()

	// 创建请求
	req := httptest.NewRequest("GET", "/frost/signed_package?job_id=job_123&idx=0", nil)
	w := httptest.NewRecorder()

	// 执行处理
	hm.HandleDownloadSignedPackage(w, req)

	// 验证响应
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp pb.DownloadSignedPackageResponse
	if err := proto.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.JobId != "job_123" {
		t.Errorf("expected job_id job_123, got %s", resp.JobId)
	}
	if resp.Idx != 0 {
		t.Errorf("expected idx 0, got %d", resp.Idx)
	}
	if resp.SignedPackageBytes != hex.EncodeToString(signedPkg.SignedPackageBytes) {
		t.Errorf("expected signed_package_bytes %s, got %s", hex.EncodeToString(signedPkg.SignedPackageBytes), resp.SignedPackageBytes)
	}
	if resp.Submitter != "0x0001" {
		t.Errorf("expected submitter 0x0001, got %s", resp.Submitter)
	}
}

// TestHandleDownloadSignedPackageNotFound 测试签名包不存在的情况
func TestHandleDownloadSignedPackageNotFound(t *testing.T) {
	hm := setupTestHandlerManager(t)

	req := httptest.NewRequest("GET", "/frost/signed_package?job_id=nonexistent", nil)
	w := httptest.NewRecorder()

	hm.HandleDownloadSignedPackage(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", w.Code)
	}
}
