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

// OrderTxHandler 订单交易处理器
type OrderTxHandler struct {
	// 区块级别的订单簿缓存（由 Executor 在 PreExecuteBlock 时设置）
	orderBooks map[string]*matching.OrderBook
}

func (h *OrderTxHandler) Kind() string {
	return "order"
}

// SetOrderBooks 设置区块级别的订单簿缓存
func (h *OrderTxHandler) SetOrderBooks(books map[string]*matching.OrderBook) {
	h.orderBooks = books
}

func (h *OrderTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 1. 提取OrderTx
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

	// 2. 根据操作类型分发处理
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

// handleAddOrder 处理添加/更新订单，并执行撮合
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

// handleRemoveOrder 婢跺嫮鎮婇幘銈呭礋
// 閻滄澘婀担璺ㄦ暏 OrderState 閼板奔绗夐敓?OrderTx
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
		ws = append(ws, WriteOp{Key: balKey, Value: balData, SyncStateDB: true, Category: "balance"})
	} else {
		balKey := keys.KeyBalance(ord.Base.FromAddress, targetState.QuoteToken)
		balData, _, _ := sv.Get(balKey)
		ws = append(ws, WriteOp{Key: balKey, Value: balData, SyncStateDB: true, Category: "balance"})
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
		Key:         targetStateKey,
		Value:       updatedStateData,
		Del:         false,
		SyncStateDB: true,
		Category:    "orderstate",
	})

	accOrderItemKey := keys.KeyAccountOrderItem(ord.Base.FromAddress, ord.OpTargetId)
	ws = append(ws, WriteOp{
		Key:         accOrderItemKey,
		Value:       nil,
		Del:         true,
		SyncStateDB: true,
		Category:    "acc_orders_item",
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
		Key:         accountKey,
		Value:       updatedAccountData,
		Del:         false,
		SyncStateDB: true,
		Category:    "account",
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
		Key:         priceIndexKey,
		Value:       nil,
		Del:         true,
		SyncStateDB: false,
		Category:    "index",
	})

	return ws, &Receipt{
		TxID:       ord.Base.TxId,
		Status:     "SUCCEED",
		WriteCount: len(ws),
	}, nil
}

// handleRemoveOrderLegacy 婢跺嫮鎮婇弮褎鏆熼幑顔界壐瀵繒娈戦幘銈呭礋閿涘牆鍚嬬€硅鈧嶇礆
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
		Key:         targetOrderKey,
		Value:       nil,
		Del:         true,
		SyncStateDB: true,
		Category:    "order",
	})

	accOrderItemKey := keys.KeyAccountOrderItem(ord.Base.FromAddress, ord.OpTargetId)
	ws = append(ws, WriteOp{
		Key:         accOrderItemKey,
		Value:       nil,
		Del:         true,
		SyncStateDB: true,
		Category:    "acc_orders_item",
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
		Key:         balKey,
		Value:       balData,
		SyncStateDB: true,
		Category:    "balance",
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
		Key:         accountKey,
		Value:       updatedAccountData,
		Del:         false,
		SyncStateDB: true,
		Category:    "account",
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
		Key:         priceIndexKey,
		Value:       nil,
		Del:         true,
		SyncStateDB: false,
		Category:    "index",
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

// generateWriteOpsFromTrades 閺嶈宓侀幘顔兼値娴滃娆㈤悽鐔稿灇WriteOps
// 閻滄澘婀担璺ㄦ暏 OrderState 鐎涙ê鍋嶉崣顖氬綁閻樿埖鈧緤绱漁rderTx 閸欘亝妲告稉宥呭讲閸欐娈戞禍銈嗘閸樼喐鏋?
func (h *OrderTxHandler) generateWriteOpsFromTrades(
	newOrd *pb.OrderTx,
	tradeEvents []matching.TradeUpdate,
	sv StateView,
	pair string,
	accountCache map[string]*pb.Account,
) ([]WriteOp, error) {
	ws := make([]WriteOp, 0)

	// 婵″倹鐏夊▽鈩冩箒閹绢喖鎮庢禍瀣╂閿涘矁顕╅弰搴ゎ吂閸楁洘婀幋鎰唉閿涘瞼娲块幒銉ょ箽鐎涙顓归崡鏇犲Ц閿?
	if len(tradeEvents) == 0 {
		// 濞夈劍鍓伴敍姘崇箹闁插瞼娈?saveNewOrder 閸愬懘鍎存导姘辨晸閹存劘澶勯幋閿嬫纯閿?WriteOp
		// 娴ｅ棛鏁遍敓?handleAddOrder 瀹歌尙绮￠崘鑽ょ波娴滃棔缍戞０婵撶礉閹存垳婊戞潻娆撳櫡閿?WriteOp 娑撳骸鍨垫慨瀣畱娑撯偓閿?
		return h.saveNewOrder(newOrd, sv, pair, accountCache)
	}

	// 婢跺嫮鎮婂В蹇庨嚋閹绢喖鎮庢禍瀣╂ - 娴ｈ法鏁?OrderState 鐎涙ê鍋嶉崣顖氬綁閻樿鎷?
	stateUpdates := make(map[string]*pb.OrderState) // orderID -> updated OrderState

	for _, ev := range tradeEvents {
		// 閸旂姾娴囩拋銏犲礋閻樿鎷?- 娴兼ê鍘涢敓?stateUpdates 娑擃叀骞忛崣鏍у嚒閺囧瓨鏌婇惃鍕閿?
		var orderState *pb.OrderState
		if cached, ok := stateUpdates[ev.OrderID]; ok {
			// 瀹稿弶婀佺拠銉吂閸楁洜濮搁幀浣烘畱閺囧瓨鏌婇悧鍫熸拱閿涘苯婀崗璺虹唨绾偓娑撳﹦鎴风紒顓熸纯閿?
			orderState = cached
		} else if ev.OrderID == newOrd.Base.TxId {
			// 鏉╂瑦妲搁弬鎷岊吂閸楁洩绱濋崚娑樼紦閸掓繂顫?OrderState
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
 // 失败也要归还
			orderStateKey := keys.KeyOrderState(ev.OrderID)
			orderStateData, exists, err := sv.Get(orderStateKey)
			if err != nil || !exists {
				// 閸忕厧顔愰弮褎鏆熼幑顕嗙窗鐏忔繆鐦禒搴㈡＋閿?OrderTx 閸旂姾娴?
				orderKey := keys.KeyOrder(ev.OrderID)
				orderData, oldExists, oldErr := sv.Get(orderKey)
				if oldErr != nil || !oldExists {
					continue
				}
				var oldOrderTx pb.OrderTx
				if err := unmarshalProtoCompat(orderData, &oldOrderTx); err != nil {
					continue
				}
				// 鏉烆剚宕查敓?OrderState
				orderState = &pb.OrderState{
					OrderId:     ev.OrderID,
					FilledBase:  "0", // 閺冄勬殶閹诡喖褰查懗鑺ョ梾閺堝绻栨禍娑樼摟濞堢绱濋柌宥嗘煀鐠侊紕鐣?
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
				// 鏉╂瑩鍣锋稉宥堝厴閿?defer Put閿涘苯娲滄稉鍝勬倵闂堛垼绻曠憰浣哥摠閿?map 楠炴儼顫﹀顏嗗箚婢舵牜娈戦柅鏄忕帆娴ｈ法鏁?
				if err := unmarshalProtoCompat(orderStateData, orderState); err != nil {
					orderStatePool.Put(orderState) // 婢惰精瑙︽稊鐔活洣瑜版帟绻?
					continue
				}
			}
		}

		// 閺囧瓨鏌婄拋銏犲礋閻樿埖鈧胶娈戦幋鎰唉娣団剝浼?
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

		// 閸氬本妞傜涵顔荤箽鐠併垹宕熺€圭偘缍嬬悮顐＄箽鐎涙﹫绱欓崗鐓庮啇缁便垹绱╅柌宥呯紦閿?
		if ev.OrderID == newOrd.Base.TxId {
			orderKey := keys.KeyOrderTx(newOrd.Base.TxId)
			orderData, _ := proto.Marshal(newOrd)
			ws = append(ws, WriteOp{
				Key:         orderKey,
				Value:       orderData,
				Del:         false,
				SyncStateDB: true,
				Category:    "order",
			})
		}
	}

	// 閻㈢喐鍨歐riteOps娣囨繂鐡ㄩ幍鈧張澶嬫纯閺傛壆娈戠拋銏犲礋閻樿鎷?
	for orderID, orderState := range stateUpdates {
		orderStateKey := keys.KeyOrderState(orderID)
		orderStateData, err := proto.Marshal(orderState)

		if err != nil {
			return nil, fmt.Errorf("failed to marshal order state %s: %w", orderID, err)
		}

		ws = append(ws, WriteOp{
			Key:         orderStateKey,
			Value:       orderStateData,
			Del:         false,
			SyncStateDB: true,
			Category:    "orderstate",
		})

	// 所有使用 stateUpdates 的函数已执行完毕，现在安全地归还 Pool 对象
		priceKey67, err := db.PriceToKey128(orderState.Price)
		if err != nil {
			return nil, fmt.Errorf("failed to convert price to key: %w", err)
		}

		// 閸掔娀娅庨弮褏娈戦張顏呭灇娴溿倗鍌ㄩ敓?
		oldIndexKey := keys.KeyOrderPriceIndex(pair, orderState.Side, false, priceKey67, orderID)
		ws = append(ws, WriteOp{
			Key:         oldIndexKey,
			Value:       nil,
			Del:         true,
			SyncStateDB: false,
			Category:    "index",
		})

	// 如果 accountCache 中有更新后的账户，生成对应的 WriteOp
		if orderState.IsFilled {
			newIndexKey := keys.KeyOrderPriceIndex(pair, orderState.Side, true, priceKey67, orderID)
			indexData, _ := proto.Marshal(&pb.OrderPriceIndex{Ok: true})
			ws = append(ws, WriteOp{
				Key:         newIndexKey,
				Value:       indexData,
				Del:         false,
				SyncStateDB: false,
				Category:    "index",
			})
		} else {
			// 婵″倹鐏夐張顏勭暚閸忋劍鍨氭禍銈忕礉娣囨繄鏆€閺堫亝鍨氭禍銈囧偍閿?(娴ｈ法鏁ら弬鎵畱 Key 閺嶇厧绱＄悰銉ュ弿 Side)
			indexData, _ := proto.Marshal(&pb.OrderPriceIndex{Ok: true})
			ws = append(ws, WriteOp{
				Key:         oldIndexKey,
				Value:       indexData,
				Del:         false,
				SyncStateDB: false,
				Category:    "index",
			})
		}
	}

	// 閻㈢喐鍨氶幋鎰唉鐠佹澘缍?
	tradeRecordOps, err := h.generateTradeRecordsFromStates(newOrd, tradeEvents, stateUpdates, pair)
	if err != nil {
		return nil, fmt.Errorf("failed to generate trade records: %w", err)
	}
	ws = append(ws, tradeRecordOps...)

	// 閺囧瓨鏌婄拹锔藉煕娴ｆ瑩顤?
	// 閿?accountCache 娴肩姴鍙嗛敍宀€鈥樻穱婵嗘倱娑撯偓缁楁柧姘﹂弰鎾冲敶閻ㄥ嫪缍戞０婵呮叏閺€瑙勬Ц缁鳖垳袧閿?
	accountBalanceOps, err := h.updateAccountBalancesFromStates(tradeEvents, stateUpdates, sv, accountCache)
	if err != nil {
		return nil, fmt.Errorf("failed to update account balances: %w", err)
	}
	ws = append(ws, accountBalanceOps...)

	// 閹碘偓閺堝濞囬敓?stateUpdates 閻ㄥ嫬鍤遍弫鏉垮嚒閹笛嗩攽鐎瑰本鐦敍宀€骞囬崷銊ョ暔閸忋劌婀磋ぐ鎺曠箷 Pool 鐎电钖?
	for _, orderState := range stateUpdates {
		orderStatePool.Put(orderState)
	}

	return ws, nil
}

// generateWriteOpsFromTrades ... (濮濄倕顦╅惇浣烘殣閿涘奔绻氶幐浣稿闂堛垻娈戦弴瀛樻煀)
// 濞夈劍鍓伴敍姝秔dateAccountBalances 閸戣姤鏆熷鑼额潶瀵啰鏁ら敍灞绢劃婢跺嫮娲块幒銉ョ殺閸忚泛鍞寸€硅绔荤粚鐑樺灗閸掔娀娅?

// saveNewOrder 娣囨繂鐡ㄩ弬鎷岊吂閸楁洩绱欓張顏呭灇娴溿倗娈戦幆鍛枌閿?
// 閻滄澘婀穱婵嗙摠 OrderState 閼板奔绗夐敓?OrderTx閿涘湦rderTx 娴ｆ粈璐熸禍銈嗘閸樼喐鏋冮敓?applyResult 缂佺喍绔存穱婵嗙摠閿?v1_txraw_<txid>閿?
func (h *OrderTxHandler) saveNewOrder(ord *pb.OrderTx, sv StateView, pair string, accountCache map[string]*pb.Account) ([]WriteOp, error) {
	ws := make([]WriteOp, 0)

	// 婵″倹鐏?accountCache 娑擃厽婀侀弴瀛樻煀閸氬海娈戠拹锔藉煕閿涘瞼鏁撻幋鎰嚠鎼存梻娈?WriteOp
	if accountCache != nil {
		if acc, ok := accountCache[ord.Base.FromAddress]; ok {
			accountKey := keys.KeyAccount(ord.Base.FromAddress)
			updatedAccountData, _ := proto.Marshal(acc)
			ws = append(ws, WriteOp{
				Key:         accountKey,
				Value:       updatedAccountData,
				Del:         false,
				SyncStateDB: true,
				Category:    "account",
			})
		}
	}

	// 閻㈢喐鍨氶崘鑽ょ波娴ｆ瑩顤傞敓?WriteOp閿涘潝andleAddOrder 瀹告煡鈧俺绻?SetBalance 閸愭瑥鍙?sv閿?
	if ord.Side == pb.OrderSide_SELL {
		balKey := keys.KeyBalance(ord.Base.FromAddress, ord.BaseToken)
		balData, _, _ := sv.Get(balKey)
		ws = append(ws, WriteOp{Key: balKey, Value: balData, SyncStateDB: true, Category: "balance"})
	} else {
		balKey := keys.KeyBalance(ord.Base.FromAddress, ord.QuoteToken)
		balData, _, _ := sv.Get(balKey)
		ws = append(ws, WriteOp{Key: balKey, Value: balData, SyncStateDB: true, Category: "balance"})
	}

	// 閸掓稑缂?OrderState閿涘牆褰查崣妯煎Ц閹緤绱?
	orderState := &pb.OrderState{
		OrderId:          ord.Base.TxId,
		FilledBase:       "0",
		FilledQuote:      "0",
		IsFilled:         false,
		Status:           pb.OrderStateStatus_ORDER_OPEN,
		CreateHeight:     ord.Base.ExecutedHeight,
		LastUpdateHeight: ord.Base.ExecutedHeight,
		// 閸愭ぞ缍戠€涙顔岄敍灞炬煙娓氭寧鐓￠敓?
		BaseToken:  ord.BaseToken,
		QuoteToken: ord.QuoteToken,
		Amount:     ord.Amount,
		Price:      ord.Price,
		Side:       ord.Side,
		Owner:      ord.Base.FromAddress,
	}

	orderStateKey := keys.KeyOrderState(ord.Base.TxId)
	orderStateData, err := proto.Marshal(orderState)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal order state: %w", err)
	}

	ws = append(ws, WriteOp{
		Key:         orderStateKey,
		Value:       orderStateData,
		Del:         false,
		SyncStateDB: true,
		Category:    "orderstate",
	})

	// 閸氬本妞傛穱婵嗙摠鐠併垹宕熺€圭偘缍嬮敍灞间簰娓氳法鍌ㄥ鏇㈠櫢瀵ゆ椽鈧槒绶敍鍦buildOrderPriceIndexes閿涘鍏樺锝呯埗瀹搞儰缍?
	orderKey := keys.KeyOrderTx(ord.Base.TxId)
	orderData, _ := proto.Marshal(ord)
	ws = append(ws, WriteOp{
		Key:         orderKey,
		Value:       orderData,
		Del:         false,
		SyncStateDB: true,
		Category:    "order",
	})

	// 4. 鐠囪褰囬悪顒傜彌鐎涙ê鍋嶉惃鍕吂閸楁洖鍨悰銊ヨ嫙濞ｈ濮為弬鎷岊吂閿?(Phase 2 闁插秵鐎敍姘暭娑撹櫣顬囬弫锝呯摠閿?
	accOrderItemKey := keys.KeyAccountOrderItem(ord.Base.FromAddress, ord.Base.TxId)
	ws = append(ws, WriteOp{
		Key:         accOrderItemKey,
		Value:       []byte{1}, // 娴犲懍缍旀稉鐑樼垼鐠佹澘鐡ㄩ敓?
		Del:         false,
		SyncStateDB: true,
		Category:    "acc_orders_item",
	})

	// 閸掓稑缂撴禒閿嬬壐缁便垹绱╅敍鍫濈唨閿?OrderState.IsFilled閿?
	priceKey67, err := db.PriceToKey128(ord.Price)
	if err != nil {
		return nil, fmt.Errorf("failed to convert price to key: %w", err)
	}

		// 创建成交记录
	priceIndexKey := keys.KeyOrderPriceIndex(pair, ord.Side, orderState.IsFilled, priceKey67, ord.Base.TxId)
	indexData, _ := proto.Marshal(&pb.OrderPriceIndex{Ok: true})
	ws = append(ws, WriteOp{
		Key:         priceIndexKey,
		Value:       indexData,
		Del:         false,
		SyncStateDB: false,
		Category:    "index",
	})

	return ws, nil
}

// generateTradeRecords 閺嶈宓侀幘顔兼値娴滃娆㈤悽鐔稿灇閹存劒姘︾拋鏉跨秿
// 閹绢喖鎮庢禍瀣╂閹存劕顕崙铏瑰箛閿涘澅aker 閿?maker 閸氬嫪绔存稉顏庣礆閿涘矂娓剁憰浣告値楠炶埖鍨氭稉鈧弶鈩冨灇娴溿倛顔囬敓?
func (h *OrderTxHandler) generateTradeRecords(
	newOrd *pb.OrderTx,
	tradeEvents []matching.TradeUpdate,
	orderUpdates map[string]*pb.OrderTx,
	pair string,
) ([]WriteOp, error) {
	ws := make([]WriteOp, 0)

	// 閺傛媽顓归崡鏇熸Ц taker閿涘本鎸抽崥鍫滅皑娴犺埖鍨氱€电懓鍤敓?
	// 闁秴宸绘禍瀣╂閿涘本鐦℃稉銈勯嚋娴滃娆㈤悽鐔稿灇娑撯偓閺夆剝鍨氭禍銈堫唶閿?
	for i := 0; i < len(tradeEvents)-1; i += 2 {
		takerEv := tradeEvents[i]
		makerEv := tradeEvents[i+1]

		// 绾喖鐣?taker 閿?maker
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
			if makerOrd, ok := orderUpdates[makerOrderID]; ok {
				takerSide = makerOrd.Side
			}
		}

		// 閻㈢喐鍨氶幋鎰唉 ID閿涘牅濞囬敓?taker 鐠併垹宕?ID + maker 鐠併垹宕?ID + 缁便垹绱╅敓?
		tradeID := fmt.Sprintf("%s_%s_%d", takerOrderID[:8], makerOrderID[:8], i/2)
		timestamp := utils.NowRFC3339()

		// 閸掓稑缂撻幋鎰唉鐠佹澘缍?
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

		// 娴ｈ法鏁ら弮鍫曟？閹村厖缍旈敓?key 閸撳秶绱戦敍宀冾唨閺堚偓閺傛壆娈戦幋鎰唉鐠佹澘缍嶉幒鎺戞躬閸撳秹娼?
		tradeKey := keys.KeyTradeRecord(pair, utils.NowUnixNano(), tradeID)
		ws = append(ws, WriteOp{
			Key:         tradeKey,
			Value:       tradeData,
			Del:         false,
			SyncStateDB: false,
			Category:    "trade",
		})
	}

	return ws, nil
}

// generateTradeRecordsFromStates 娴ｈ法鏁?OrderState 閻㈢喐鍨氶幋鎰唉鐠佹澘缍?
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

	// key 格式: "address:token"
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
			Key:         tradeKey,
			Value:       tradeData,
			Del:         false,
			SyncStateDB: false,
			Category:    "trade",
		})
	}

	return ws, nil
}

// updateAccountBalancesFromStates 娴ｈ法鏁?OrderState 閺囧瓨鏌婄拹锔藉煕娴ｆ瑩顤傞敍鍫滃▏閻劌鍨庣粋璇茬摠閸岊煉绱?
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
				return nil, fmt.Errorf("seller frozen balance underflow: %w", err)
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
				return nil, fmt.Errorf("buyer frozen balance underflow: %w", err)
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
			Key:         balKey,
			Value:       balData,
			SyncStateDB: true,
			Category:    "balance",
		})
	}

	return ws, nil
}
