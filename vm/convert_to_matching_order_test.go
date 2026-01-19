package vm

import (
	"testing"

	"dex/pb"

	"github.com/shopspring/decimal"
)

func TestConvertToMatchingOrder_UsesFilledBaseWhenBaseTokenIsFirst(t *testing.T) {
	t.Parallel()

	ord := &pb.OrderTx{
		Base: &pb.BaseMessage{
			TxId: "sell_order_1",
		},
		BaseToken:   "FB",
		QuoteToken:  "USDT",
		Side:        pb.OrderSide_SELL,
		Price:       "1.7",
		Amount:      "10",
		FilledBase:  "5",
		FilledQuote: "8.5",
		IsFilled:    false,
	}

	o, err := convertToMatchingOrder(ord)
	if err != nil {
		t.Fatalf("convertToMatchingOrder returned error: %v", err)
	}

	if !o.Amount.Equal(decimal.RequireFromString("5")) {
		t.Fatalf("unexpected remaining amount: got=%s want=%s", o.Amount, "5")
	}
}

func TestConvertToMatchingOrder_UsesFilledBaseForBuyOrder(t *testing.T) {
	t.Parallel()

	// 买单：用户想买 10 FB，已经买到 5 FB，还剩 5 FB 要买
	ord := &pb.OrderTx{
		Base: &pb.BaseMessage{
			TxId: "buy_order_1",
		},
		BaseToken:   "FB",
		QuoteToken:  "USDT",
		Side:        pb.OrderSide_BUY,
		Price:       "1.7",
		Amount:      "10",  // 要买 10 FB
		FilledBase:  "5",   // 已买到 5 FB
		FilledQuote: "8.5", // 已支付 8.5 USDT
		IsFilled:    false,
	}

	o, err := convertToMatchingOrder(ord)
	if err != nil {
		t.Fatalf("convertToMatchingOrder returned error: %v", err)
	}

	// 剩余量 = Amount - FilledBase = 10 - 5 = 5
	if !o.Amount.Equal(decimal.RequireFromString("5")) {
		t.Fatalf("unexpected remaining amount: got=%s want=%s", o.Amount, "5")
	}
}

func TestConvertToMatchingOrder_FullFillReturnsError(t *testing.T) {
	t.Parallel()

	// 订单已完全成交：Amount = FilledBase，剩余量为 0
	ord := &pb.OrderTx{
		Base: &pb.BaseMessage{
			TxId: "sell_order_full",
		},
		BaseToken:   "FB",
		QuoteToken:  "USDT",
		Side:        pb.OrderSide_SELL,
		Price:       "1.7",
		Amount:      "10", // 要卖 10 FB
		FilledBase:  "10", // 已卖出 10 FB
		FilledQuote: "17", // 已收到 17 USDT
		IsFilled:    true,
	}

	_, err := convertToMatchingOrder(ord)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}
