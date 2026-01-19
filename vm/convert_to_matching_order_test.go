package vm

import (
	"testing"

	"dex/pb"

	"github.com/shopspring/decimal"
)

// 测试使用 OrderState 的转换函数
func TestConvertOrderStateToMatchingOrder_UsesFilledBaseWhenBaseTokenIsFirst(t *testing.T) {
	t.Parallel()

	state := &pb.OrderState{
		OrderId:     "sell_order_1",
		BaseToken:   "FB",
		QuoteToken:  "USDT",
		Side:        pb.OrderSide_SELL,
		Price:       "1.7",
		Amount:      "10",
		FilledBase:  "5",
		FilledQuote: "8.5",
		IsFilled:    false,
	}

	o, err := convertOrderStateToMatchingOrder(state)
	if err != nil {
		t.Fatalf("convertOrderStateToMatchingOrder returned error: %v", err)
	}

	if !o.Amount.Equal(decimal.RequireFromString("5")) {
		t.Fatalf("unexpected remaining amount: got=%s want=%s", o.Amount, "5")
	}
}

func TestConvertOrderStateToMatchingOrder_UsesFilledBaseForBuyOrder(t *testing.T) {
	t.Parallel()

	// 买单：用户想买 10 FB，已经买到 5 FB，还剩 5 FB 要买
	state := &pb.OrderState{
		OrderId:     "buy_order_1",
		BaseToken:   "FB",
		QuoteToken:  "USDT",
		Side:        pb.OrderSide_BUY,
		Price:       "1.7",
		Amount:      "10",  // 要买 10 FB
		FilledBase:  "5",   // 已买到 5 FB
		FilledQuote: "8.5", // 已支付 8.5 USDT
		IsFilled:    false,
	}

	o, err := convertOrderStateToMatchingOrder(state)
	if err != nil {
		t.Fatalf("convertOrderStateToMatchingOrder returned error: %v", err)
	}

	// 剩余量 = Amount - FilledBase = 10 - 5 = 5
	if !o.Amount.Equal(decimal.RequireFromString("5")) {
		t.Fatalf("unexpected remaining amount: got=%s want=%s", o.Amount, "5")
	}
}

func TestConvertOrderStateToMatchingOrder_FullFillReturnsError(t *testing.T) {
	t.Parallel()

	// 订单已完全成交：Amount = FilledBase，剩余量为 0
	state := &pb.OrderState{
		OrderId:     "sell_order_full",
		BaseToken:   "FB",
		QuoteToken:  "USDT",
		Side:        pb.OrderSide_SELL,
		Price:       "1.7",
		Amount:      "10", // 要卖 10 FB
		FilledBase:  "10", // 已卖出 10 FB
		FilledQuote: "17", // 已收到 17 USDT
		IsFilled:    true,
	}

	_, err := convertOrderStateToMatchingOrder(state)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

// 测试旧版 OrderTx 的兼容性转换函数
func TestConvertToMatchingOrderLegacy_NewOrder(t *testing.T) {
	t.Parallel()

	ord := &pb.OrderTx{
		Base: &pb.BaseMessage{
			TxId: "new_order_1",
		},
		BaseToken:  "FB",
		QuoteToken: "USDT",
		Side:       pb.OrderSide_SELL,
		Price:      "1.7",
		Amount:     "10",
	}

	o, err := convertToMatchingOrderLegacy(ord)
	if err != nil {
		t.Fatalf("convertToMatchingOrderLegacy returned error: %v", err)
	}

	// 新订单没有成交，剩余量 = Amount = 10
	if !o.Amount.Equal(decimal.RequireFromString("10")) {
		t.Fatalf("unexpected remaining amount: got=%s want=%s", o.Amount, "10")
	}
}
