package vm

import (
	"testing"

	"dex/pb"

	"github.com/shopspring/decimal"
)

// 娴嬭瘯浣跨敤 OrderState 鐨勮浆鎹㈠嚱鏁?
func TestConvertOrderStateToMatchingOrder_UsesFilledBaseWhenBaseTokenIsFirst(t *testing.T) {
	t.Parallel()

	state := &pb.OrderState{
		OrderId:     "sell_order_1",
		BaseToken:   "FB",
		QuoteToken:  "USDT",
		Side:        pb.OrderSide_SELL,
		Price:       "17",
		Amount:      "10",
		FilledBase:  "5",
		FilledQuote: "85",
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

	// 涔板崟锛氱敤鎴锋兂涔?10 FB锛屽凡缁忎拱鍒?5 FB锛岃繕鍓?5 FB 瑕佷拱
	state := &pb.OrderState{
		OrderId:     "buy_order_1",
		BaseToken:   "FB",
		QuoteToken:  "USDT",
		Side:        pb.OrderSide_BUY,
		Price:       "17",
		Amount:      "10", // 瑕佷拱 10 FB
		FilledBase:  "5",  // 宸蹭拱鍒?5 FB
		FilledQuote: "85", // 宸叉敮浠?8.5 USDT
		IsFilled:    false,
	}

	o, err := convertOrderStateToMatchingOrder(state)
	if err != nil {
		t.Fatalf("convertOrderStateToMatchingOrder returned error: %v", err)
	}

	// 鍓╀綑閲?= Amount - FilledBase = 10 - 5 = 5
	if !o.Amount.Equal(decimal.RequireFromString("5")) {
		t.Fatalf("unexpected remaining amount: got=%s want=%s", o.Amount, "5")
	}
}

func TestConvertOrderStateToMatchingOrder_FullFillReturnsError(t *testing.T) {
	t.Parallel()

	// 璁㈠崟宸插畬鍏ㄦ垚浜わ細Amount = FilledBase锛屽墿浣欓噺涓?0
	state := &pb.OrderState{
		OrderId:     "sell_order_full",
		BaseToken:   "FB",
		QuoteToken:  "USDT",
		Side:        pb.OrderSide_SELL,
		Price:       "17",
		Amount:      "10",  // 瑕佸崠 10 FB
		FilledBase:  "10",  // 宸插崠鍑?10 FB
		FilledQuote: "170", // 宸叉敹鍒?170 USDT
		IsFilled:    true,
	}

	_, err := convertOrderStateToMatchingOrder(state)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

// 娴嬭瘯鏃х増 OrderTx 鐨勫吋瀹规€ц浆鎹㈠嚱鏁?
func TestConvertToMatchingOrderLegacy_NewOrder(t *testing.T) {
	t.Parallel()

	ord := &pb.OrderTx{
		Base: &pb.BaseMessage{
			TxId: "new_order_1",
		},
		BaseToken:  "FB",
		QuoteToken: "USDT",
		Side:       pb.OrderSide_SELL,
		Price:      "17",
		Amount:     "10",
	}

	o, err := convertToMatchingOrderLegacy(ord)
	if err != nil {
		t.Fatalf("convertToMatchingOrderLegacy returned error: %v", err)
	}

	// 鏂拌鍗曟病鏈夋垚浜わ紝鍓╀綑閲?= Amount = 10
	if !o.Amount.Equal(decimal.RequireFromString("10")) {
		t.Fatalf("unexpected remaining amount: got=%s want=%s", o.Amount, "10")
	}
}
