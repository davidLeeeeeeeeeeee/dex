package testharness_test

import (
	"dex/pb"
	"dex/testharness"
	"testing"
)

// TestWitnessMinerAssignment 验证 WitnessRequestTx 提交后，
// 前端接口（vm.WitnessRequestTxHandler）能将任务正确分配给活跃 witness 地址。
func TestWitnessMinerAssignment(t *testing.T) {
	// 至少 5 个节点，确保候选池大于 InitialWitnessCount(5)
	h := testharness.New(t, 6)

	const (
		chain    = "ETH"
		token    = "0xUSDT"
		stakeAmt = "2000000" // 超过 MinStakeAmount(1000000)
	)

	// 1. 为所有委员会成员创建账户 + 质押，使其成为活跃 witness
	for _, addr := range h.Committee {
		h.SeedAccount(addr, "FB", stakeAmt)
		h.SubmitTxExpectSuccess(testharness.BuildWitnessStakeTx(addr, pb.OrderOp_ADD, stakeAmt))
	}
	h.AdvanceHeight(1)

	// 2. 种 VaultConfig（resolveVaultChainAndCount 需要）和 VaultState（ACTIVE）
	h.SeedVaultConfig(chain, 0, 10)
	h.SeedVaultState(chain, 0, "ACTIVE", 1)

	// 3. 种 Token（WitnessRequestTxHandler 校验 token 是否存在）
	h.SeedToken(token)

	// 4. 提交入账见证请求
	requester := h.Committee[0]
	tx := testharness.BuildWitnessRequestTx(
		requester, chain, "0xNativeTxHashABC", 0, nil,
		token, "1000", h.Committee[1], "10",
	)
	requestID := tx.GetTxId()
	h.SubmitTxExpectSuccess(tx)

	// 5. 读回 RechargeRequest，断言已分配 witness
	req := h.GetRechargeRequest(requestID)
	if req == nil {
		t.Fatal("RechargeRequest not found after submission")
	}
	if len(req.SelectedWitnesses) == 0 {
		t.Fatal("no witnesses assigned to request")
	}

	// 所有分配地址必须在 committee 内（即活跃 witness 集合）
	addrSet := make(map[string]bool, len(h.Committee))
	for _, a := range h.Committee {
		addrSet[a] = true
	}
	for _, w := range req.SelectedWitnesses {
		if !addrSet[w] {
			t.Errorf("assigned witness %s not in committee", w)
		}
	}

	t.Logf("request_id=%s assigned_witnesses=%v", requestID, req.SelectedWitnesses)
	h.PrintTxLog()
}
