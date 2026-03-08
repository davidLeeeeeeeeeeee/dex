package testharness_test

import (
	"dex/pb"
	"dex/testharness"
	"testing"
)

// TestTransferFlow 基本转账测试：验证框架的 SeedAccount + SubmitTx + AssertBalance
func TestTransferFlow(t *testing.T) {
	h := testharness.New(t, 3)

	h.SeedAccount("alice", "FB", "1000")
	h.SeedAccount("bob", "FB", "0")
	h.AdvanceHeight(1)

	tx := testharness.BuildTransferTx("alice", "bob", "FB", "100")
	h.SubmitTxExpectSuccess(tx)

	h.AssertBalance("bob", "FB", "100")
	h.PrintTxLog()
}

// TestMultiTxSameBlock 同一高度多笔 tx 协作
func TestMultiTxSameBlock(t *testing.T) {
	h := testharness.New(t, 3)

	h.SeedAccount("alice", "FB", "10000")
	h.SeedAccount("bob", "FB", "10000")
	h.SeedAccount("charlie", "FB", "0")
	h.AdvanceHeight(1)

	h.SubmitBlock(
		testharness.BuildTransferTx("alice", "charlie", "FB", "100"),
		testharness.BuildTransferTx("bob", "charlie", "FB", "200"),
	)

	h.AssertBalance("charlie", "FB", "300")
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

	// 提交一个 DKG Commit tx（使用伪造的 commitment 数据）
	tx := testharness.BuildDkgCommitTx(
		committee[0], "btc", 0, 1,
		pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
		[][]byte{{0x02, 0x01, 0x02, 0x03}}, // fake commitment points
		[]byte{0x02, 0x01, 0x02, 0x03},     // fake AI0
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
