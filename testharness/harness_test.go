package testharness_test

import (
	"dex/pb"
	"dex/testharness"
	"testing"
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
