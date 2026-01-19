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

func TestConvertToMatchingOrder_UsesFilledQuoteWhenBaseTokenIsSecond(t *testing.T) {
	t.Parallel()

	ord := &pb.OrderTx{
		Base: &pb.BaseMessage{
			TxId: "buy_order_1",
		},
		BaseToken:   "USDT",
		QuoteToken:  "FB",
		Side:        pb.OrderSide_BUY,
		Price:       "1.7",
		Amount:      "10",
		FilledBase:  "8.5",
		FilledQuote: "5",
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

func TestConvertToMatchingOrder_FullFillReturnsError(t *testing.T) {
	t.Parallel()

	ord := &pb.OrderTx{
		Base: &pb.BaseMessage{
			TxId: "buy_order_full",
		},
		BaseToken:   "USDT",
		QuoteToken:  "FB",
		Side:        pb.OrderSide_BUY,
		Price:       "1.7",
		Amount:      "10",
		FilledBase:  "17",
		FilledQuote: "10",
		IsFilled:    true,
	}

	_, err := convertToMatchingOrder(ord)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

