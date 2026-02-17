package vm

import (
	"dex/db"
	"dex/keys"
	"dex/matching"
	"dex/pb"
	"dex/utils"
	"fmt"
	"math/big"

	"sync"

	"google.golang.org/protobuf/proto"
)

var (
	accountPool = sync.Pool{
		New: func() interface{} {
			return &pb.Account{}
		},
	}
	orderStatePool = sync.Pool{
		New: func() interface{} {
			return &pb.OrderState{}
		},
	}
)

// OrderTxHandler 璁㈠崟浜ゆ槗澶勭悊鍣?
type OrderTxHandler struct {
	// 鍖哄潡绾у埆鐨勮鍗曠翱缂撳瓨锛堢敱 Executor 鍦?PreExecuteBlock 鏃惰缃級
	orderBooks map[string]*matching.OrderBook
}

func (h *OrderTxHandler) Kind() string {
	return "order"
}

// SetOrderBooks 璁剧疆鍖哄潡绾у埆鐨勮鍗曠翱缂撳瓨
func (h *OrderTxHandler) SetOrderBooks(books map[string]*matching.OrderBook) {
	h.orderBooks = books
}

func (h *OrderTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 1. 鎻愬彇OrderTx
	orderTx, ok := tx.GetContent().(*pb.AnyTx_OrderTx)
	if !ok {
		return nil, &Receipt{
			TxID:   tx.GetTxId(),
			Status: "FAILED",
			Error:  "not an order transaction",
		}, fmt.Errorf("not an order transaction")
	}

	ord := orderTx.OrderTx
	if ord == nil || ord.Base == nil {
		return nil, &Receipt{
			TxID:   tx.GetTxId(),
			Status: "FAILED",
			Error:  "invalid order transaction",
		}, fmt.Errorf("invalid order transaction")
	}

	// 2. 鏍规嵁鎿嶄綔绫诲瀷鍒嗗彂澶勭悊
	switch ord.Op {
	case pb.OrderOp_ADD:
		return h.handleAddOrder(ord, sv)
	case pb.OrderOp_REMOVE:
		return h.handleRemoveOrder(ord, sv)
	default:
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "unknown order operation",
		}, fmt.Errorf("unknown order operation: %v", ord.Op)
	}
}

// handleAddOrder 澶勭悊娣诲姞/鏇存柊璁㈠崟锛屽苟鎵ц鎾悎
func (h *OrderTxHandler) handleAddOrder(ord *pb.OrderTx, sv StateView) ([]WriteOp, *Receipt, error) {
	amountBI, err := parsePositiveBalanceStrict("order amount", ord.Amount)
	if err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "invalid order amount",
		}, nil
	}
	priceBI, err := parsePositiveBalanceStrict("order price", ord.Price)
	if err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "invalid order price",
		}, nil
	}

	accountKey := keys.KeyAccount(ord.Base.FromAddress)
	accountData, exists, err := sv.Get(accountKey)
	if err != nil || !exists {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "account not found",
		}, nil
	}

	account := accountPool.Get().(*pb.Account)
	account.Reset()
	defer accountPool.Put(account)

	if err := unmarshalProtoCompat(accountData, account); err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse account",
		}, nil
	}

	if ord.Side == pb.OrderSide_SELL {
		bal := GetBalance(sv, ord.Base.FromAddress, ord.BaseToken)
		current, err := parseBalanceStrict("balance", bal.Balance)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "invalid balance state"}, err
		}
		if current.Cmp(amountBI) < 0 {
			return nil, &Receipt{
				TxID:   ord.Base.TxId,
				Status: "FAILED",
				Error:  fmt.Sprintf("insufficient %s balance: has %s, need %s", ord.BaseToken, current.String(), amountBI.String()),
			}, fmt.Errorf("insufficient balance")
		}
		frozen, err := parseBalanceStrict("order frozen balance", bal.OrderFrozenBalance)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "invalid frozen balance state"}, err
		}
		newBalance, err := SafeSub(current, amountBI)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "balance underflow"}, err
		}
		newFrozen, err := SafeAdd(frozen, amountBI)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "frozen balance overflow"}, err
		}
		bal.Balance = newBalance.String()
		bal.OrderFrozenBalance = newFrozen.String()
		SetBalance(sv, ord.Base.FromAddress, ord.BaseToken, bal)
	} else {
		bal := GetBalance(sv, ord.Base.FromAddress, ord.QuoteToken)
		current, err := parseBalanceStrict("balance", bal.Balance)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "invalid balance state"}, err
		}
		needed, err := SafeMul(amountBI, priceBI)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "amount*price overflow"}, nil
		}
		if current.Cmp(needed) < 0 {
			return nil, &Receipt{
				TxID:   ord.Base.TxId,
				Status: "FAILED",
				Error:  fmt.Sprintf("insufficient %s balance to buy: has %s, need %s", ord.QuoteToken, current.String(), needed.String()),
			}, fmt.Errorf("insufficient balance")
		}
		frozen, err := parseBalanceStrict("order frozen balance", bal.OrderFrozenBalance)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "invalid frozen balance state"}, err
		}
		newBalance, err := SafeSub(current, needed)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "balance underflow"}, err
		}
		newFrozen, err := SafeAdd(frozen, needed)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "frozen balance overflow"}, err
		}
		bal.Balance = newBalance.String()
		bal.OrderFrozenBalance = newFrozen.String()
		SetBalance(sv, ord.Base.FromAddress, ord.QuoteToken, bal)
	}

	pair := utils.GeneratePairKey(ord.BaseToken, ord.QuoteToken)
	orderBook, ok := h.orderBooks[pair]
	if !ok {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  fmt.Sprintf("order book not found for pair: %s", pair),
		}, fmt.Errorf("order book not found for pair: %s", pair)
	}

	var tradeEvents []matching.TradeUpdate
	orderBook.SetTradeSink(func(ev matching.TradeUpdate) {
		tradeEvents = append(tradeEvents, ev)
	})

	newOrder, err := convertToMatchingOrderLegacy(ord)
	if err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  fmt.Sprintf("failed to convert order: %v", err),
		}, err
	}

	if err := orderBook.AddOrder(newOrder); err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  fmt.Sprintf("failed to add order: %v", err),
		}, err
	}

	accountCache := map[string]*pb.Account{
		ord.Base.FromAddress: account,
	}
	ws, err := h.generateWriteOpsFromTrades(ord, tradeEvents, sv, pair, accountCache)
	if err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  fmt.Sprintf("failed to generate write ops: %v", err),
		}, err
	}

	return ws, &Receipt{
		TxID:       ord.Base.TxId,
		Status:     "SUCCEED",
		WriteCount: len(ws),
	}, nil
}

// handleRemoveOrder 澶勭悊绉婚櫎璁㈠崟璇锋眰
// 閫昏緫锛氫紭鍏堝皾璇曟煡鎵惧苟绉婚櫎 OrderState锛涘鏋滄湭鎵惧埌锛屽垯灏濊瘯鏌ユ壘鏃х増 OrderTx锛堝吋瀹规€у鐞嗭級銆?
func (h *OrderTxHandler) handleRemoveOrder(ord *pb.OrderTx, sv StateView) ([]WriteOp, *Receipt, error) {
	if ord.OpTargetId == "" {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "op_target_id is required for REMOVE operation",
		}, fmt.Errorf("op_target_id is required")
	}

	targetStateKey := keys.KeyOrderState(ord.OpTargetId)
	targetStateData, exists, err := sv.Get(targetStateKey)
	if err != nil || !exists {
		// 灏濊瘯鍥為€€鏌ユ壘鏃х増 OrderTx
		targetOrderKey := keys.KeyOrder(ord.OpTargetId)
		targetOrderData, oldExists, oldErr := sv.Get(targetOrderKey)
		if oldErr != nil || !oldExists {
			return nil, &Receipt{
				TxID:   ord.Base.TxId,
				Status: "FAILED",
				Error:  "target order not found",
			}, fmt.Errorf("target order not found: %s", ord.OpTargetId)
		}

		var oldOrderTx pb.OrderTx
		if err := unmarshalProtoCompat(targetOrderData, &oldOrderTx); err != nil {
			return nil, &Receipt{
				TxID:   ord.Base.TxId,
				Status: "FAILED",
				Error:  "failed to parse target order",
			}, err
		}

		if oldOrderTx.Base.FromAddress != ord.Base.FromAddress {
			return nil, &Receipt{
				TxID:   ord.Base.TxId,
				Status: "FAILED",
				Error:  "only order creator can remove the order",
			}, fmt.Errorf("permission denied")
		}

		return h.handleRemoveOrderLegacy(ord, &oldOrderTx, sv)
	}

	var targetState pb.OrderState
	if err := unmarshalProtoCompat(targetStateData, &targetState); err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse target order state",
		}, err
	}

	if targetState.Owner != ord.Base.FromAddress {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "only order creator can remove the order",
		}, fmt.Errorf("permission denied")
	}

	accountKey := keys.KeyAccount(ord.Base.FromAddress)
	accountData, exists, err := sv.Get(accountKey)
	if err != nil || !exists {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "account not found",
		}, fmt.Errorf("account not found")
	}

	var account pb.Account
	if err := unmarshalProtoCompat(accountData, &account); err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse account",
		}, err
	}

	totalAmount, err := parseBalanceStrict("order amount", targetState.Amount)
	if err != nil {
		return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "invalid order state amount"}, err
	}
	filledBase, err := parseBalanceStrict("order filled_base", targetState.FilledBase)
	if err != nil {
		return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "invalid order state filled_base"}, err
	}
	toRefundBase, err := SafeSub(totalAmount, filledBase)
	if err != nil {
		return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "invalid order state: filled exceeds amount"}, err
	}

	if targetState.Side == pb.OrderSide_SELL {
		bal := GetBalance(sv, ord.Base.FromAddress, targetState.BaseToken)
		current, err := parseBalanceStrict("balance", bal.Balance)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "invalid balance state"}, err
		}
		frozen, err := parseBalanceStrict("order frozen balance", bal.OrderFrozenBalance)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "invalid frozen balance state"}, err
		}
		newBalance, err := SafeAdd(current, toRefundBase)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "balance overflow"}, err
		}
		newFrozen, err := SafeSub(frozen, toRefundBase)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "frozen balance underflow"}, err
		}
		bal.Balance = newBalance.String()
		bal.OrderFrozenBalance = newFrozen.String()
		SetBalance(sv, ord.Base.FromAddress, targetState.BaseToken, bal)
	} else {
		limitPrice, err := parseBalanceStrict("order price", targetState.Price)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "invalid order state price"}, err
		}
		toRefundQuote, err := SafeMul(toRefundBase, limitPrice)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "refund overflow"}, err
		}

		bal := GetBalance(sv, ord.Base.FromAddress, targetState.QuoteToken)
		current, err := parseBalanceStrict("balance", bal.Balance)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "invalid balance state"}, err
		}
		frozen, err := parseBalanceStrict("order frozen balance", bal.OrderFrozenBalance)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "invalid frozen balance state"}, err
		}
		newBalance, err := SafeAdd(current, toRefundQuote)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "balance overflow"}, err
		}
		newFrozen, err := SafeSub(frozen, toRefundQuote)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "frozen balance underflow"}, err
		}
		bal.Balance = newBalance.String()
		bal.OrderFrozenBalance = newFrozen.String()
		SetBalance(sv, ord.Base.FromAddress, targetState.QuoteToken, bal)
	}

	ws := make([]WriteOp, 0)

	if targetState.Side == pb.OrderSide_SELL {
		balKey := keys.KeyBalance(ord.Base.FromAddress, targetState.BaseToken)
		balData, _, _ := sv.Get(balKey)
		ws = append(ws, WriteOp{Key: balKey, Value: balData, Category: "balance"})
	} else {
		balKey := keys.KeyBalance(ord.Base.FromAddress, targetState.QuoteToken)
		balData, _, _ := sv.Get(balKey)
		ws = append(ws, WriteOp{Key: balKey, Value: balData, Category: "balance"})
	}

	targetState.Status = pb.OrderStateStatus_ORDER_CANCELLED
	updatedStateData, err := proto.Marshal(&targetState)
	if err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to marshal order state",
		}, err
	}

	ws = append(ws, WriteOp{
		Key:      targetStateKey,
		Value:    updatedStateData,
		Del:      false,
		Category: "orderstate",
	})

	accOrderItemKey := keys.KeyAccountOrderItem(ord.Base.FromAddress, ord.OpTargetId)
	ws = append(ws, WriteOp{
		Key:      accOrderItemKey,
		Value:    nil,
		Del:      true,
		Category: "acc_orders_item",
	})

	updatedAccountData, err := proto.Marshal(&account)
	if err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to marshal account",
		}, err
	}

	ws = append(ws, WriteOp{
		Key:      accountKey,
		Value:    updatedAccountData,
		Del:      false,
		Category: "account",
	})

	pair := utils.GeneratePairKey(targetState.BaseToken, targetState.QuoteToken)
	priceKey67, err := db.PriceToKey128(targetState.Price)
	if err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to convert price to key",
		}, err
	}
	priceIndexKey := keys.KeyOrderPriceIndex(pair, targetState.Side, targetState.IsFilled, priceKey67, ord.OpTargetId)

	ws = append(ws, WriteOp{
		Key:      priceIndexKey,
		Value:    nil,
		Del:      true,
		Category: "index",
	})

	return ws, &Receipt{
		TxID:       ord.Base.TxId,
		Status:     "SUCCEED",
		WriteCount: len(ws),
	}, nil
}

// handleRemoveOrderLegacy 澶勭悊閬楃暀鐨?OrderTx 鏍煎紡璁㈠崟绉婚櫎
// 鐢ㄤ簬鍏煎鏃ф暟鎹紝褰?OrderState 涓嶅瓨鍦ㄦ椂璋冪敤銆?
func (h *OrderTxHandler) handleRemoveOrderLegacy(ord *pb.OrderTx, targetOrder *pb.OrderTx, sv StateView) ([]WriteOp, *Receipt, error) {
	accountKey := keys.KeyAccount(ord.Base.FromAddress)
	accountData, exists, err := sv.Get(accountKey)
	if err != nil || !exists {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "account not found",
		}, fmt.Errorf("account not found")
	}

	var account pb.Account
	if err := unmarshalProtoCompat(accountData, &account); err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse account",
		}, err
	}

	ws := make([]WriteOp, 0)

	targetOrderKey := keys.KeyOrder(ord.OpTargetId)
	ws = append(ws, WriteOp{
		Key:      targetOrderKey,
		Value:    nil,
		Del:      true,
		Category: "order",
	})

	accOrderItemKey := keys.KeyAccountOrderItem(ord.Base.FromAddress, ord.OpTargetId)
	ws = append(ws, WriteOp{
		Key:      accOrderItemKey,
		Value:    nil,
		Del:      true,
		Category: "acc_orders_item",
	})

	var refundToken string
	var refundAmount *big.Int
	if targetOrder.Side == pb.OrderSide_SELL {
		refundToken = targetOrder.BaseToken
		refundAmount, err = parseBalanceStrict("order amount", targetOrder.Amount)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "invalid order amount"}, err
		}
	} else {
		refundToken = targetOrder.QuoteToken
		amountBI, err := parseBalanceStrict("order amount", targetOrder.Amount)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "invalid order amount"}, err
		}
		priceBI, err := parseBalanceStrict("order price", targetOrder.Price)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "invalid order price"}, err
		}
		refundAmount, err = SafeMul(amountBI, priceBI)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "refund overflow"}, err
		}
	}

	bal := GetBalance(sv, ord.Base.FromAddress, refundToken)
	current, err := parseBalanceStrict("balance", bal.Balance)
	if err != nil {
		return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "invalid balance state"}, err
	}
	frozen, err := parseBalanceStrict("order frozen balance", bal.OrderFrozenBalance)
	if err != nil {
		return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "invalid frozen balance state"}, err
	}

	newBalance, err := SafeAdd(current, refundAmount)
	if err != nil {
		return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "balance overflow"}, err
	}
	bal.Balance = newBalance.String()
	if frozen.Cmp(refundAmount) < 0 {
		bal.OrderFrozenBalance = "0"
	} else {
		newFrozen, err := SafeSub(frozen, refundAmount)
		if err != nil {
			return nil, &Receipt{TxID: ord.Base.TxId, Status: "FAILED", Error: "frozen balance underflow"}, err
		}
		bal.OrderFrozenBalance = newFrozen.String()
	}

	SetBalance(sv, ord.Base.FromAddress, refundToken, bal)

	balKey := keys.KeyBalance(ord.Base.FromAddress, refundToken)
	balData, _, _ := sv.Get(balKey)
	ws = append(ws, WriteOp{
		Key:      balKey,
		Value:    balData,
		Category: "balance",
	})

	updatedAccountData, err := proto.Marshal(&account)
	if err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to marshal account",
		}, err
	}

	ws = append(ws, WriteOp{
		Key:      accountKey,
		Value:    updatedAccountData,
		Del:      false,
		Category: "account",
	})

	pair := utils.GeneratePairKey(targetOrder.BaseToken, targetOrder.QuoteToken)
	priceKey67, err := db.PriceToKey128(targetOrder.Price)
	if err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to convert price to key",
		}, err
	}
	priceIndexKey := keys.KeyOrderPriceIndex(pair, targetOrder.Side, false, priceKey67, ord.OpTargetId)

	ws = append(ws, WriteOp{
		Key:      priceIndexKey,
		Value:    nil,
		Del:      true,
		Category: "index",
	})

	return ws, &Receipt{
		TxID:       ord.Base.TxId,
		Status:     "SUCCEED",
		WriteCount: len(ws),
	}, nil
}


func (h *OrderTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}

// updateAccountBalancesFromStates 鏍规嵁 OrderState 鏇存柊璐︽埛浣欓
func (h *OrderTxHandler) updateAccountBalancesFromStates(
	tradeEvents []matching.TradeUpdate,
	stateUpdates map[string]*pb.OrderState,
	sv StateView,
	accountCache map[string]*pb.Account,
) ([]WriteOp, error) {
	ws := make([]WriteOp, 0)
	if accountCache == nil {
		accountCache = make(map[string]*pb.Account)
	}

	type balanceCacheKey struct {
		addr, token string
	}
	balanceCache := make(map[balanceCacheKey]*pb.TokenBalance)

	getOrLoadBalance := func(addr, token string) *pb.TokenBalance {
		key := balanceCacheKey{addr, token}
		if bal, ok := balanceCache[key]; ok {
			return bal
		}
		bal := GetBalance(sv, addr, token)
		balanceCache[key] = bal
		return bal
	}

	for _, ev := range tradeEvents {
		orderState, ok := stateUpdates[ev.OrderID]
		if !ok {
			continue
		}

		address := orderState.Owner
		baseBal := getOrLoadBalance(address, orderState.BaseToken)
		quoteBal := getOrLoadBalance(address, orderState.QuoteToken)

		baseBalance, err := parseBalanceStrict("balance", baseBal.Balance)
		if err != nil {
			return nil, err
		}
		quoteBalance, err := parseBalanceStrict("balance", quoteBal.Balance)
		if err != nil {
			return nil, err
		}
		tradeAmtBI, err := decimalToBalanceStrict("trade amount", ev.TradeAmt)
		if err != nil {
			return nil, err
		}
		tradePriceBI, err := decimalToBalanceStrict("trade price", ev.TradePrice)
		if err != nil {
			return nil, err
		}
		tradeQuoteAmt, err := SafeMul(tradeAmtBI, tradePriceBI)
		if err != nil {
			return nil, err
		}

		if orderState.Side == pb.OrderSide_SELL {
			frozen, err := parseBalanceStrict("order frozen balance", baseBal.OrderFrozenBalance)
			if err != nil {
				return nil, err
			}
			newFrozen, err := SafeSub(frozen, tradeAmtBI)
			if err != nil {
				return nil, fmt.Errorf(
					"seller frozen balance underflow: order_id=%s owner=%s token=%s frozen=%s deduct=%s trade_amt=%s trade_price=%s: %w",
					ev.OrderID,
					address,
					orderState.BaseToken,
					frozen.String(),
					tradeAmtBI.String(),
					tradeAmtBI.String(),
					tradePriceBI.String(),
					err,
				)
			}
			newQuoteBalance, err := SafeAdd(quoteBalance, tradeQuoteAmt)
			if err != nil {
				return nil, fmt.Errorf("seller quote balance overflow: %w", err)
			}
			baseBal.OrderFrozenBalance = newFrozen.String()
			quoteBal.Balance = newQuoteBalance.String()
		} else {
			limitPrice, err := parseBalanceStrict("order price", orderState.Price)
			if err != nil {
				return nil, err
			}
			frozenToDeduct, err := SafeMul(tradeAmtBI, limitPrice)
			if err != nil {
				return nil, err
			}

			frozen, err := parseBalanceStrict("order frozen balance", quoteBal.OrderFrozenBalance)
			if err != nil {
				return nil, err
			}
			newFrozen, err := SafeSub(frozen, frozenToDeduct)
			if err != nil {
				return nil, fmt.Errorf(
					"buyer frozen balance underflow: order_id=%s owner=%s token=%s frozen=%s deduct=%s trade_amt=%s trade_price=%s limit_price=%s: %w",
					ev.OrderID,
					address,
					orderState.QuoteToken,
					frozen.String(),
					frozenToDeduct.String(),
					tradeAmtBI.String(),
					tradePriceBI.String(),
					limitPrice.String(),
					err,
				)
			}
			quoteBal.OrderFrozenBalance = newFrozen.String()

			newBaseBalance, err := SafeAdd(baseBalance, tradeAmtBI)
			if err != nil {
				return nil, fmt.Errorf("buyer base balance overflow: %w", err)
			}
			baseBal.Balance = newBaseBalance.String()

			if tradePriceBI.Cmp(limitPrice) < 0 {
				priceDiff := new(big.Int).Sub(limitPrice, tradePriceBI)
				refundQuote, err := SafeMul(tradeAmtBI, priceDiff)
				if err != nil {
					return nil, err
				}
				currentQuoteBal, err := parseBalanceStrict("balance", quoteBal.Balance)
				if err != nil {
					return nil, err
				}
				newQuoteBalance, err := SafeAdd(currentQuoteBal, refundQuote)
				if err != nil {
					return nil, fmt.Errorf("buyer quote refund overflow: %w", err)
				}
				quoteBal.Balance = newQuoteBalance.String()
			}
		}
	}

	for key, bal := range balanceCache {
		SetBalance(sv, key.addr, key.token, bal)
		balKey := keys.KeyBalance(key.addr, key.token)
		balData, _, _ := sv.Get(balKey)
		ws = append(ws, WriteOp{
			Key:      balKey,
			Value:    balData,
			Category: "balance",
		})
	}

	return ws, nil
}

// generateWriteOpsFromTrades 鏍规嵁鎾悎缁撴灉鐢熸垚 WriteOps
// 鏇存柊 OrderState 骞跺鐞?OrderTx 鐩稿叧鐨勭姸鎬佸彉鏇淬€?
func (h *OrderTxHandler) generateWriteOpsFromTrades(
	newOrd *pb.OrderTx,
	tradeEvents []matching.TradeUpdate,
	sv StateView,
	pair string,
	accountCache map[string]*pb.Account,
) ([]WriteOp, error) {
	ws := make([]WriteOp, 0)

	// 濡傛灉娌℃湁鎴愪氦浜嬩欢锛岀洿鎺ヤ繚瀛樻柊璁㈠崟
	if len(tradeEvents) == 0 {
		// saveNewOrder 浼氬鐞嗕綑棰濆喕缁撳拰 OrderState 淇濆瓨
		return h.saveNewOrder(newOrd, sv, pair, accountCache)
	}

	// 棰勫鐞?OrderState 鏇存柊
	stateUpdates := make(map[string]*pb.OrderState) // orderID -> updated OrderState

	for _, ev := range tradeEvents {
		// 鑾峰彇鎴栧垵濮嬪寲 OrderState
		var orderState *pb.OrderState
		if cached, ok := stateUpdates[ev.OrderID]; ok {
			// 浣跨敤缂撳瓨涓殑鐘舵€?
			orderState = cached
		} else if ev.OrderID == newOrd.Base.TxId {
			// 鏂拌鍗曠殑 OrderState 鍒濆鍖?
			orderState = &pb.OrderState{
				OrderId:          newOrd.Base.TxId,
				FilledBase:       "0",
				FilledQuote:      "0",
				IsFilled:         false,
				Status:           pb.OrderStateStatus_ORDER_OPEN,
				CreateHeight:     newOrd.Base.ExecutedHeight,
				LastUpdateHeight: newOrd.Base.ExecutedHeight,
				BaseToken:        newOrd.BaseToken,
				QuoteToken:       newOrd.QuoteToken,
				Amount:           newOrd.Amount,
				Price:            newOrd.Price,
				Side:             newOrd.Side,
				Owner:            newOrd.Base.FromAddress,
			}
		} else {
			// 灏濊瘯浠?DB 鍔犺浇鐜版湁 OrderState
			orderStateKey := keys.KeyOrderState(ev.OrderID)
			orderStateData, exists, err := sv.Get(orderStateKey)
			if err != nil || !exists {
				// 濡傛灉娌℃湁 OrderState锛屽皾璇曞洖閫€鏌ユ壘 OrderTx锛堝吋瀹规棫鏁版嵁锛?
				orderKey := keys.KeyOrder(ev.OrderID)
				orderData, oldExists, oldErr := sv.Get(orderKey)
				if oldErr != nil || !oldExists {
					continue
				}
				var oldOrderTx pb.OrderTx
				if err := unmarshalProtoCompat(orderData, &oldOrderTx); err != nil {
					continue
				}
				// 浠?OrderTx 閲嶅缓鍒濆 OrderState
				orderState = &pb.OrderState{
					OrderId:     ev.OrderID,
					FilledBase:  "0",
					FilledQuote: "0",
					IsFilled:    false,
					Status:      pb.OrderStateStatus_ORDER_OPEN,
					BaseToken:   oldOrderTx.BaseToken,
					QuoteToken:  oldOrderTx.QuoteToken,
					Amount:      oldOrderTx.Amount,
					Price:       oldOrderTx.Price,
					Side:        oldOrderTx.Side,
					Owner:       oldOrderTx.Base.FromAddress,
				}
			} else {
				orderState = orderStatePool.Get().(*pb.OrderState)
				orderState.Reset()
				if err := unmarshalProtoCompat(orderStateData, orderState); err != nil {
					orderStatePool.Put(orderState)
					continue
				}
			}
		}

		// 鏇存柊鎴愪氦閲?
		filledBase, err := parseBalanceStrict("order filled_base", orderState.FilledBase)
		if err != nil {
			return nil, fmt.Errorf("invalid order state filled_base for %s: %w", ev.OrderID, err)
		}
		filledQuote, err := parseBalanceStrict("order filled_quote", orderState.FilledQuote)
		if err != nil {
			return nil, fmt.Errorf("invalid order state filled_quote for %s: %w", ev.OrderID, err)
		}
		tradeAmtBI, err := decimalToBalanceStrict("trade amount", ev.TradeAmt)
		if err != nil {
			return nil, fmt.Errorf("invalid trade amount for %s: %w", ev.OrderID, err)
		}
		tradePriceBI, err := decimalToBalanceStrict("trade price", ev.TradePrice)
		if err != nil {
			return nil, fmt.Errorf("invalid trade price for %s: %w", ev.OrderID, err)
		}
		tradeQuoteBI, err := SafeMul(tradeAmtBI, tradePriceBI)
		if err != nil {
			return nil, fmt.Errorf("trade quote overflow for %s: %w", ev.OrderID, err)
		}
		newFilledBase, err := SafeAdd(filledBase, tradeAmtBI)
		if err != nil {
			return nil, fmt.Errorf("filled_base overflow for %s: %w", ev.OrderID, err)
		}
		newFilledQuote, err := SafeAdd(filledQuote, tradeQuoteBI)
		if err != nil {
			return nil, fmt.Errorf("filled_quote overflow for %s: %w", ev.OrderID, err)
		}

		orderState.FilledBase = newFilledBase.String()
		orderState.FilledQuote = newFilledQuote.String()
		orderState.IsFilled = ev.IsFilled
		if ev.IsFilled {
			orderState.Status = pb.OrderStateStatus_ORDER_FILLED
		}

		stateUpdates[ev.OrderID] = orderState

		// 濡傛灉鏄柊璁㈠崟锛岃繕闇€瑕佷繚瀛?OrderTx 鍘熸枃
		if ev.OrderID == newOrd.Base.TxId {
			orderKey := keys.KeyOrderTx(newOrd.Base.TxId)
			orderData, _ := proto.Marshal(newOrd)
			ws = append(ws, WriteOp{
				Key:      orderKey,
				Value:    orderData,
				Del:      false,
				Category: "order",
			})
		}
	}

	// 鐢熸垚 OrderState 鐨?WriteOps
	for orderID, orderState := range stateUpdates {
		orderStateKey := keys.KeyOrderState(orderID)
		orderStateData, err := proto.Marshal(orderState)

		if err != nil {
			return nil, fmt.Errorf("failed to marshal order state %s: %w", orderID, err)
		}

		ws = append(ws, WriteOp{
			Key:      orderStateKey,
			Value:    orderStateData,
			Del:      false,
			Category: "orderstate",
		})

		// 澶勭悊绱㈠紩鏇存柊锛氬厛鍒犻櫎鏃х储寮?
		priceKey67, err := db.PriceToKey128(orderState.Price)
		if err != nil {
			return nil, fmt.Errorf("failed to convert price to key: %w", err)
		}

		oldIndexKey := keys.KeyOrderPriceIndex(pair, orderState.Side, false, priceKey67, orderID)
		ws = append(ws, WriteOp{
			Key:      oldIndexKey,
			Value:    nil,
			Del:      true,
			Category: "index",
		})

		// 濡傛灉鏈垚浜ゅ畬锛屾坊鍔犳柊绱㈠紩锛堟垨鑰呭凡鎴愪氦瀹屼篃鍙兘闇€瑕佽褰曪紝瑙嗛€昏緫鑰屽畾锛岃繖閲岄€昏緫鏄?IsFilled 鍒欏彧璁板綍 Ok=true 鐨勭储寮曪紝鍙兘鐢ㄤ簬鍘嗗彶鏌ヨ锛燂級
		newIndexKey := keys.KeyOrderPriceIndex(pair, orderState.Side, orderState.IsFilled, priceKey67, orderID)
		indexData, _ := proto.Marshal(&pb.OrderPriceIndex{Ok: true})
		ws = append(ws, WriteOp{
			Key:      newIndexKey,
			Value:    indexData,
			Del:      false,
			Category: "index",
		})
	}

	// 鐢熸垚鎴愪氦璁板綍鐨?WriteOps
	tradeRecordOps, err := h.generateTradeRecordsFromStates(newOrd, tradeEvents, stateUpdates, pair)
	if err != nil {
		return nil, fmt.Errorf("failed to generate trade records: %w", err)
	}
	ws = append(ws, tradeRecordOps...)

	// 鏇存柊璐︽埛浣欓
	accountBalanceOps, err := h.updateAccountBalancesFromStates(tradeEvents, stateUpdates, sv, accountCache)
	if err != nil {
		return nil, fmt.Errorf("failed to update account balances: %w", err)
	}
	ws = append(ws, accountBalanceOps...)

	// 褰掕繕瀵硅薄姹?
	for _, orderState := range stateUpdates {
		orderStatePool.Put(orderState)
	}

	return ws, nil
}

// saveNewOrder 淇濆瓨鏂拌鍗曠殑鍒濆鐘舵€?
// 鍖呮嫭锛歄rderTx, OrderState, 璐︽埛淇℃伅鏇存柊, 鍐荤粨浣欓鏇存柊, 璁㈠崟绱㈠紩, 璐︽埛璁㈠崟鍒楄〃銆?
func (h *OrderTxHandler) saveNewOrder(ord *pb.OrderTx, sv StateView, pair string, accountCache map[string]*pb.Account) ([]WriteOp, error) {
	ws := make([]WriteOp, 0)

	// 淇濆瓨鏇存柊鍚庣殑璐︽埛淇℃伅
	if accountCache != nil {
		if acc, ok := accountCache[ord.Base.FromAddress]; ok {
			accountKey := keys.KeyAccount(ord.Base.FromAddress)
			updatedAccountData, _ := proto.Marshal(acc)
			ws = append(ws, WriteOp{
				Key:      accountKey,
				Value:    updatedAccountData,
				Del:      false,
				Category: "account",
			})
		}
	}

	// 淇濆瓨浣欓鏇存柊锛堝喕缁擄級
	if ord.Side == pb.OrderSide_SELL {
		balKey := keys.KeyBalance(ord.Base.FromAddress, ord.BaseToken)
		balData, _, _ := sv.Get(balKey)
		ws = append(ws, WriteOp{Key: balKey, Value: balData, Category: "balance"})
	} else {
		balKey := keys.KeyBalance(ord.Base.FromAddress, ord.QuoteToken)
		balData, _, _ := sv.Get(balKey)
		ws = append(ws, WriteOp{Key: balKey, Value: balData, Category: "balance"})
	}

	// 鍒涘缓鍒濆 OrderState
	orderState := &pb.OrderState{
		OrderId:          ord.Base.TxId,
		FilledBase:       "0",
		FilledQuote:      "0",
		IsFilled:         false,
		Status:           pb.OrderStateStatus_ORDER_OPEN,
		CreateHeight:     ord.Base.ExecutedHeight,
		LastUpdateHeight: ord.Base.ExecutedHeight,
		BaseToken:        ord.BaseToken,
		QuoteToken:       ord.QuoteToken,
		Amount:           ord.Amount,
		Price:            ord.Price,
		Side:             ord.Side,
		Owner:            ord.Base.FromAddress,
	}

	orderStateKey := keys.KeyOrderState(ord.Base.TxId)
	orderStateData, err := proto.Marshal(orderState)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal order state: %w", err)
	}

	ws = append(ws, WriteOp{
		Key:      orderStateKey,
		Value:    orderStateData,
		Del:      false,
		Category: "orderstate",
	})

	// 淇濆瓨 OrderTx 鍘熸枃
	orderKey := keys.KeyOrderTx(ord.Base.TxId)
	orderData, _ := proto.Marshal(ord)
	ws = append(ws, WriteOp{
		Key:      orderKey,
		Value:    orderData,
		Del:      false,
		Category: "order",
	})

	// 娣诲姞鍒拌处鎴风殑璁㈠崟鍒楄〃
	accOrderItemKey := keys.KeyAccountOrderItem(ord.Base.FromAddress, ord.Base.TxId)
	ws = append(ws, WriteOp{
		Key:      accOrderItemKey,
		Value:    []byte{1},
		Del:      false,
		Category: "acc_orders_item",
	})

	// 鍒涘缓浠锋牸绱㈠紩
	priceKey67, err := db.PriceToKey128(ord.Price)
	if err != nil {
		return nil, fmt.Errorf("failed to convert price to key: %w", err)
	}

	priceIndexKey := keys.KeyOrderPriceIndex(pair, ord.Side, orderState.IsFilled, priceKey67, ord.Base.TxId)
	indexData, _ := proto.Marshal(&pb.OrderPriceIndex{Ok: true})
	ws = append(ws, WriteOp{
		Key:      priceIndexKey,
		Value:    indexData,
		Del:      false,
		Category: "index",
	})

	return ws, nil
}

// generateTradeRecordsFromStates 浠?OrderState 鐢熸垚鎴愪氦璁板綍
func (h *OrderTxHandler) generateTradeRecordsFromStates(
	newOrd *pb.OrderTx,
	tradeEvents []matching.TradeUpdate,
	stateUpdates map[string]*pb.OrderState,
	pair string,
) ([]WriteOp, error) {
	ws := make([]WriteOp, 0)

	for i := 0; i < len(tradeEvents)-1; i += 2 {
		takerEv := tradeEvents[i]
		makerEv := tradeEvents[i+1]

		var takerOrderID, makerOrderID string
		var tradePrice, tradeAmount string
		var takerSide pb.OrderSide

		if takerEv.OrderID == newOrd.Base.TxId {
			takerOrderID = takerEv.OrderID
			makerOrderID = makerEv.OrderID
			tradePrice = takerEv.TradePrice.String()
			tradeAmount = takerEv.TradeAmt.String()
			takerSide = newOrd.Side
		} else {
			takerOrderID = makerEv.OrderID
			makerOrderID = takerEv.OrderID
			tradePrice = makerEv.TradePrice.String()
			tradeAmount = makerEv.TradeAmt.String()
			if makerState, ok := stateUpdates[makerOrderID]; ok {
				takerSide = makerState.Side
			}
		}

		// key 鏍煎紡: "address:token"
		takerIDShort := takerOrderID
		if len(takerIDShort) > 8 {
			takerIDShort = takerIDShort[:8]
		}
		makerIDShort := makerOrderID
		if len(makerIDShort) > 8 {
			makerIDShort = makerIDShort[:8]
		}
		tradeID := fmt.Sprintf("%s_%s_%d", takerIDShort, makerIDShort, i/2)
		timestamp := utils.NowRFC3339()

		tradeRecord := &pb.TradeRecord{
			TradeId:      tradeID,
			Pair:         pair,
			Price:        tradePrice,
			Amount:       tradeAmount,
			MakerOrderId: makerOrderID,
			TakerOrderId: takerOrderID,
			TakerSide:    takerSide,
			Timestamp:    timestamp,
			Height:       newOrd.Base.ExecutedHeight,
		}

		tradeData, err := proto.Marshal(tradeRecord)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal trade record: %w", err)
		}

		tradeKey := keys.KeyTradeRecord(pair, utils.NowUnixNano(), tradeID)
		ws = append(ws, WriteOp{
			Key:      tradeKey,
			Value:    tradeData,
			Del:      false,
			Category: "trade",
		})
	}

	return ws, nil
}
