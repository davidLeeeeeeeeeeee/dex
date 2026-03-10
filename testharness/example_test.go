package testharness_test

import (
	"context"
	"dex/frost/runtime/adapters"
	"dex/frost/runtime/types"
	"dex/frost/runtime/workers"
	"dex/logs"
	"dex/pb"
	"dex/testharness"
	"testing"
	"time"
)

// TestTransferFlow 基本转账测试：验证框架的 SeedAccount + SubmitTx + AssertBalance
func TestTransferFlow(t *testing.T) {
	h := testharness.New(t, 3)
	from, to := h.Committee[0], h.Committee[1]

	h.SeedAccount(from, "FB", "1000")
	h.SeedAccount(to, "FB", "0")
	h.AdvanceHeight(1)

	h.SubmitTxExpectSuccess(testharness.BuildTransferTx(from, to, "FB", "100"))

	h.AssertBalance(to, "FB", "100")
	h.PrintTxLog()
}

// TestMultiTxSameBlock 同一高度多笔 tx 协作
func TestMultiTxSameBlock(t *testing.T) {
	h := testharness.New(t, 3)
	a, b, c := h.Committee[0], h.Committee[1], h.Committee[2]

	h.SeedAccount(a, "FB", "10000")
	h.SeedAccount(b, "FB", "10000")
	h.SeedAccount(c, "FB", "0")
	h.AdvanceHeight(1)

	h.SubmitBlock(
		testharness.BuildTransferTx(a, c, "FB", "100"),
		testharness.BuildTransferTx(b, c, "FB", "200"),
	)

	h.AssertBalance(c, "FB", "300")
	h.PrintTxLog()
}

// TestDkgCommitViaHarness 验证框架能正确路由 DKG Commit tx 到 VM handler
func TestDkgCommitViaHarness(t *testing.T) {
	h := testharness.New(t, 3)

	committee := h.Committee
	h.AdvanceHeight(5)

	h.SeedVaultTransition("btc", 0, 1, committee,
		1,  // triggerHeight
		20, // commitDeadline
		40, // sharingDeadline
		60, // disputeDeadline
		pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
	)

	// 提交 DKG Commit tx（使用真实公钥作为 commitment / AI0）
	pubKey := h.PublicKeys[committee[0]]
	tx := testharness.BuildDkgCommitTx(
		committee[0], "btc", 0, 1,
		pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
		[][]byte{pubKey}, pubKey,
	)

	err := h.SubmitTx(tx)
	if err != nil {
		t.Fatalf("DKG commit failed: %v", err)
	}

	// 验证 transition 状态已推进到 COMMITTING
	h.AssertDkgStatus("btc", 0, 1, "COMMITTING")
	h.PrintTxLog()
}

// TestHeightAdvance 验证高度控制
func TestHeightAdvance(t *testing.T) {
	h := testharness.New(t, 1)

	if h.Height() != 0 {
		t.Fatalf("initial height should be 0, got %d", h.Height())
	}

	h.AdvanceHeight(10)
	if h.Height() != 10 {
		t.Fatalf("height should be 10, got %d", h.Height())
	}

	h.AdvanceHeight(5)
	if h.Height() != 15 {
		t.Fatalf("height should be 15, got %d", h.Height())
	}
}

// ---------------------------------------------------------------------------
// DKG Runtime + VM 集成测试所需的 mock
// ---------------------------------------------------------------------------

// mockPubKeyProvider 从 harness 的 PublicKeys 查公钥
type mockPubKeyProvider struct{ pubKeys map[string][]byte }

func (m *mockPubKeyProvider) GetMinerSigningPubKey(minerID string, _ pb.SignAlgo) ([]byte, error) {
	if pk, ok := m.pubKeys[minerID]; ok {
		return pk, nil
	}
	return nil, nil
}

// mockVaultCommitteeProvider 固定返回 committee
type mockVaultCommitteeProvider struct {
	committee []string
	pubKeys   map[string][]byte
}

func (m *mockVaultCommitteeProvider) VaultCommittee(_ string, _ uint32, _ uint64) ([]types.SignerInfo, error) {
	result := make([]types.SignerInfo, len(m.committee))
	for i, addr := range m.committee {
		result[i] = types.SignerInfo{ID: types.NodeID(addr), Index: uint32(i + 1), PublicKey: m.pubKeys[addr]}
	}
	return result, nil
}
func (m *mockVaultCommitteeProvider) VaultCurrentEpoch(string, uint32) uint64 { return 1 }
func (m *mockVaultCommitteeProvider) VaultGroupPubkey(string, uint32, uint64) ([]byte, error) {
	return nil, nil
}
func (m *mockVaultCommitteeProvider) CalculateThreshold(string, uint32) (int, error) { return 2, nil }

// mockSignerSetProvider
type mockSignerSetProvider struct{}

func (m *mockSignerSetProvider) Top10000(_ uint64) ([]types.SignerInfo, error) { return nil, nil }
func (m *mockSignerSetProvider) CurrentEpoch(_ uint64) uint64                  { return 1 }

// mockChainAdapterFactory
type mockChainAdapterFactory struct{}

func (m *mockChainAdapterFactory) Adapter(string) (workers.ChainAdapter, error) { return nil, nil }

// mockLocalShareStore 内存存储 local share
type mockLocalShareStore struct {
	shares map[string][]byte
}

func newMockLocalShareStore() *mockLocalShareStore {
	return &mockLocalShareStore{shares: make(map[string][]byte)}
}
func (m *mockLocalShareStore) SaveLocalShare(chain string, vaultID uint32, epoch uint64, share []byte) error {
	key := chain + "_" + string(rune(vaultID)) + "_" + string(rune(epoch))
	m.shares[key] = share
	return nil
}
func (m *mockLocalShareStore) LoadLocalShare(chain string, vaultID uint32, epoch uint64) ([]byte, error) {
	key := chain + "_" + string(rune(vaultID)) + "_" + string(rune(epoch))
	return m.shares[key], nil
}

// ---------------------------------------------------------------------------
// TestDKGWithRuntime — Runtime + VM 集成：3 个 Worker 跑完整 DKG 流程
// ---------------------------------------------------------------------------

func TestDKGWithRuntime(t *testing.T) {
	h := testharness.New(t, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	committee := h.Committee

	// 1. 种子数据
	h.SeedVaultTransition("btc", 0, 1, committee,
		1,  // triggerHeight
		20, // commitDeadline
		40, // sharingDeadline
		60, // disputeDeadline
		pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
	)
	h.SeedVaultState("btc", 0, "PENDING_DKG", 1)
	h.AdvanceHeight(5) // 当前高度 5，在 commit 窗口内 [1,20]

	// 2. 构造共享依赖
	pubKeyProvider := &mockPubKeyProvider{pubKeys: h.PublicKeys}
	vaultProvider := &mockVaultCommitteeProvider{committee: committee, pubKeys: h.PublicKeys}
	cryptoFactory := adapters.NewDefaultCryptoExecutorFactory()
	logger := logs.NewNodeLogger("test", 100)

	// 3. 为每个 committee 成员创建一个 TransitionWorker
	for _, member := range committee {
		w := workers.NewTransitionWorker(
			h.StateReader(),
			h.TxSubmitter(),
			pubKeyProvider,
			cryptoFactory,
			vaultProvider,
			&mockSignerSetProvider{},
			&mockChainAdapterFactory{},
			newMockLocalShareStore(),
			h.PrivateKeys[member],
			member,
			logger,
		)
		go w.StartSession(ctx, "btc", 0, 1, pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340)
	}

	// 4. 后台自动推高度（驱动 waitForHeight 循环）
	stopHeight := h.AutoAdvanceHeight(100 * time.Millisecond)
	defer stopHeight()

	// 5. 等最终状态：Vault 的 GroupPubkey 被写入
	h.RunUntil(func() bool {
		ts := h.GetTransitionState("btc", 0, 1)
		return ts != nil && ts.DkgStatus == "KEY_READY"
	}, 30*time.Second)

	t.Logf("DKG completed! final height=%d", h.Height())
	h.PrintTxLog()

	// 验证
	ts := h.GetTransitionState("btc", 0, 1)
	if ts == nil {
		t.Fatal("transition state is nil")
	}
	t.Logf("DKG status=%s, validation_status=%s", ts.DkgStatus, ts.ValidationStatus)

	// 验证 tx 种类和数量
	commitCount := h.TxLogCountByKind("frost_vault_dkg_commit")
	shareCount := h.TxLogCountByKind("frost_vault_dkg_share")
	validationCount := h.TxLogCountByKind("frost_vault_dkg_validation_signed")
	t.Logf("TX stats: commit=%d, share=%d, validation=%d", commitCount, shareCount, validationCount)

	if commitCount != 3 {
		t.Fatalf("expected 3 commit txs, got %d", commitCount)
	}
	if shareCount != 9 { // 3 个 dealer × 3 个 receiver
		t.Fatalf("expected 9 share txs, got %d", shareCount)
	}
	if validationCount != 3 {
		t.Fatalf("expected 3 validation txs, got %d", validationCount)
	}

	// 确认 DKG 流程没有产生 withdraw 类 tx
	if h.TxLogHasKind("frost_withdraw_signed") {
		t.Fatal("unexpected frost_withdraw_signed tx during DKG flow")
	}
}
