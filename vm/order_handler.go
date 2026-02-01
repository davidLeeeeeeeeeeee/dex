package vm

import (
	"dex/db"
	"dex/keys"
	"dex/matching"
	"dex/pb"
	"dex/utils"
	"fmt"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
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
	// 1. 验证订单参数
	amountDec, err := decimal.NewFromString(ord.Amount)
	if err != nil || amountDec.LessThanOrEqual(decimal.Zero) {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "invalid order amount",
		}, nil // 业务逻辑失败不应挂掉区块
	}

	priceDec, err := decimal.NewFromString(ord.Price)
	if err != nil || priceDec.LessThanOrEqual(decimal.Zero) {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "invalid order price",
		}, nil
	}

	// 检查是否超过 MaxUint256
	maxUint256Dec := decimal.NewFromBigInt(MaxUint256, 0)
	if amountDec.GreaterThan(maxUint256Dec) {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "amount overflow",
		}, nil
	}
	if priceDec.GreaterThan(maxUint256Dec) {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "price overflow",
		}, nil
	}

	// 2. 读取账户
	accountKey := keys.KeyAccount(ord.Base.FromAddress)
	accountData, exists, err := sv.Get(accountKey)
	if err != nil || !exists {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "account not found",
		}, nil
	}

	var account pb.Account
	if err := proto.Unmarshal(accountData, &account); err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse account",
		}, nil
	}

	// 3. 根据订单方向检查余额
	// - 卖单：需要有 BaseToken 余额（要卖出的币）
	// - 买单：需要有 QuoteToken 余额（要支付的币）
	if account.Balances == nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "account has no balances",
		}, nil
	}

	if ord.Side == pb.OrderSide_SELL {
		// 卖单：检查并冻结 BaseToken 余额
		bal := account.Balances[ord.BaseToken]
		if bal == nil {
			return nil, &Receipt{
				TxID:   ord.Base.TxId,
				Status: "FAILED",
				Error:  "insufficient base token balance",
			}, nil
		}
		current, _ := decimal.NewFromString(bal.Balance)
		if current.LessThan(amountDec) {
			return nil, &Receipt{
				TxID:   ord.Base.TxId,
				Status: "FAILED",
				Error:  fmt.Sprintf("insufficient %s balance: has %s, need %s", ord.BaseToken, current, amountDec),
			}, fmt.Errorf("insufficient balance")
		}
		// 1) 扣除余额，增加订单锁定余额
		frozen, _ := decimal.NewFromString(bal.OrderFrozenBalance)
		bal.Balance = current.Sub(amountDec).String()
		bal.OrderFrozenBalance = frozen.Add(amountDec).String()
	} else {
		// 买单：检查并冻结 QuoteToken 余额
		bal := account.Balances[ord.QuoteToken]
		if bal == nil {
			return nil, &Receipt{
				TxID:   ord.Base.TxId,
				Status: "FAILED",
				Error:  "insufficient quote token balance",
			}, nil
		}
		current, _ := decimal.NewFromString(bal.Balance)
		needed := amountDec.Mul(priceDec)
		if current.LessThan(needed) {
			return nil, &Receipt{
				TxID:   ord.Base.TxId,
				Status: "FAILED",
				Error:  fmt.Sprintf("insufficient %s balance to buy: has %s, need %s", ord.QuoteToken, current, needed),
			}, fmt.Errorf("insufficient balance")
		}
		// 1) 扣除余额，增加订单锁定余额
		frozen, _ := decimal.NewFromString(bal.OrderFrozenBalance)
		bal.Balance = current.Sub(needed).String()
		bal.OrderFrozenBalance = frozen.Add(needed).String()
	}

	// 4. 生成交易对key（使用utils.GeneratePairKey确保一致性）
	pair := utils.GeneratePairKey(ord.BaseToken, ord.QuoteToken)

	// 5. 从缓存中获取订单簿（已在 PreExecuteBlock 中重建）
	orderBook, ok := h.orderBooks[pair]
	if !ok {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  fmt.Sprintf("order book not found for pair: %s", pair),
		}, fmt.Errorf("order book not found for pair: %s", pair)
	}

	// 6. 收集撮合事件
	var tradeEvents []matching.TradeUpdate
	orderBook.SetTradeSink(func(ev matching.TradeUpdate) {
		tradeEvents = append(tradeEvents, ev)
	})

	// 7. 将新订单转换为matching.Order并添加到订单簿
	// 新订单还没有 OrderState，使用 Legacy 函数（假设未成交）
	newOrder, err := convertToMatchingOrderLegacy(ord)
	if err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  fmt.Sprintf("failed to convert order: %v", err),
		}, err
	}

	// 8. 执行撮合
	if err := orderBook.AddOrder(newOrder); err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  fmt.Sprintf("failed to add order: %v", err),
		}, err
	}

	// 9. 根据撮合事件生成WriteOps
	// 传入已修改的账户 cache，确保 Taker 的余额冻结不会被后续成交逻辑覆盖
	accountCache := map[string]*pb.Account{
		ord.Base.FromAddress: &account,
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

// handleRemoveOrder 处理撤单
// 现在使用 OrderState 而不是 OrderTx
func (h *OrderTxHandler) handleRemoveOrder(ord *pb.OrderTx, sv StateView) ([]WriteOp, *Receipt, error) {
	if ord.OpTargetId == "" {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "op_target_id is required for REMOVE operation",
		}, fmt.Errorf("op_target_id is required")
	}

	// 读取要撤销的订单状态
	targetStateKey := keys.KeyOrderState(ord.OpTargetId)
	targetStateData, exists, err := sv.Get(targetStateKey)
	if err != nil || !exists {
		// 兼容旧数据：尝试从旧的 OrderTx 加载
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
		if err := proto.Unmarshal(targetOrderData, &oldOrderTx); err != nil {
			return nil, &Receipt{
				TxID:   ord.Base.TxId,
				Status: "FAILED",
				Error:  "failed to parse target order",
			}, err
		}

		// 验证权限
		if oldOrderTx.Base.FromAddress != ord.Base.FromAddress {
			return nil, &Receipt{
				TxID:   ord.Base.TxId,
				Status: "FAILED",
				Error:  "only order creator can remove the order",
			}, fmt.Errorf("permission denied")
		}

		// 使用旧数据处理撤单
		return h.handleRemoveOrderLegacy(ord, &oldOrderTx, sv)
	}

	var targetState pb.OrderState
	if err := proto.Unmarshal(targetStateData, &targetState); err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse target order state",
		}, err
	}

	// 验证权限：只有订单创建者可以撤单
	if targetState.Owner != ord.Base.FromAddress {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "only order creator can remove the order",
		}, fmt.Errorf("permission denied")
	}

	// 读取账户
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
	if err := proto.Unmarshal(accountData, &account); err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse account",
		}, err
	}

	// 1) 返还剩余锁定余额
	remainingBase, _ := decimal.NewFromString(targetState.Amount)
	filledBase, _ := decimal.NewFromString(targetState.FilledBase)
	toRefundBase := remainingBase.Sub(filledBase)

	if targetState.Side == pb.OrderSide_SELL {
		// 卖单退回 BaseToken
		bal := account.Balances[targetState.BaseToken]
		if bal != nil {
			current, _ := decimal.NewFromString(bal.Balance)
			frozen, _ := decimal.NewFromString(bal.OrderFrozenBalance)
			// 原子更新：可用 += 待退回，冻结 -= 待退回
			bal.Balance = current.Add(toRefundBase).String()
			bal.OrderFrozenBalance = frozen.Sub(toRefundBase).String()
		}
	} else {
		// 买单退回 QuoteToken
		// 注意：下单时冻结的是 Amount * LimitPrice
		// 已经成交的部分，冻结额度已在 updateAccountBalancesFromStates 中扣除
		// 剩余未成交部分，冻结额度应为 (Amount - FilledBase) * LimitPrice
		limitPrice, _ := decimal.NewFromString(targetState.Price)
		toRefundQuote := toRefundBase.Mul(limitPrice)

		bal := account.Balances[targetState.QuoteToken]
		if bal != nil {
			current, _ := decimal.NewFromString(bal.Balance)
			frozen, _ := decimal.NewFromString(bal.OrderFrozenBalance)
			bal.Balance = current.Add(toRefundQuote).String()
			bal.OrderFrozenBalance = frozen.Sub(toRefundQuote).String()
		}
	}

	ws := make([]WriteOp, 0)

	// 更新订单状态为已撤单（而不是删除）
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

	// 4. 从离散订单列表中移除 (Phase 2 重构)
	accOrderItemKey := keys.KeyAccountOrderItem(ord.Base.FromAddress, ord.OpTargetId)
	ws = append(ws, WriteOp{
		Key:         accOrderItemKey,
		Value:       nil,
		Del:         true,
		SyncStateDB: true,
		Category:    "acc_orders_item",
	})

	// 保存更新后的账户（仅更新 Nonce 等，不再包含订单列表）
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

	// 删除价格索引
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

// handleRemoveOrderLegacy 处理旧数据格式的撤单（兼容性）
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
	if err := proto.Unmarshal(accountData, &account); err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse account",
		}, err
	}

	ws := make([]WriteOp, 0)

	// 删除旧的订单数据
	targetOrderKey := keys.KeyOrder(ord.OpTargetId)
	ws = append(ws, WriteOp{
		Key:         targetOrderKey,
		Value:       nil,
		Del:         true,
		SyncStateDB: true,
		Category:    "order",
	})

	// 4. 从离散订单列表中移除 (Phase 2 重构)
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

	// 删除价格索引
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

// generateWriteOpsFromTrades 根据撮合事件生成WriteOps
// 现在使用 OrderState 存储可变状态，OrderTx 只是不可变的交易原文
func (h *OrderTxHandler) generateWriteOpsFromTrades(
	newOrd *pb.OrderTx,
	tradeEvents []matching.TradeUpdate,
	sv StateView,
	pair string,
	accountCache map[string]*pb.Account,
) ([]WriteOp, error) {
	ws := make([]WriteOp, 0)

	// 如果没有撮合事件，说明订单未成交，直接保存订单状态
	if len(tradeEvents) == 0 {
		// 注意：这里的 saveNewOrder 内部会生成账户更新 WriteOp
		// 但由于 handleAddOrder 已经冻结了余额，我们这里的 WriteOp 与初始的一致
		return h.saveNewOrder(newOrd, sv, pair, accountCache)
	}

	// 处理每个撮合事件 - 使用 OrderState 存储可变状态
	stateUpdates := make(map[string]*pb.OrderState) // orderID -> updated OrderState

	for _, ev := range tradeEvents {
		// 加载订单状态 - 优先从 stateUpdates 中获取已更新的版本
		var orderState *pb.OrderState
		if cached, ok := stateUpdates[ev.OrderID]; ok {
			// 已有该订单状态的更新版本，在其基础上继续更新
			orderState = cached
		} else if ev.OrderID == newOrd.Base.TxId {
			// 这是新订单，创建初始 OrderState
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
			// 这是已存在的订单，从StateView加载 OrderState
			orderStateKey := keys.KeyOrderState(ev.OrderID)
			orderStateData, exists, err := sv.Get(orderStateKey)
			if err != nil || !exists {
				// 兼容旧数据：尝试从旧的 OrderTx 加载
				orderKey := keys.KeyOrder(ev.OrderID)
				orderData, oldExists, oldErr := sv.Get(orderKey)
				if oldErr != nil || !oldExists {
					continue
				}
				var oldOrderTx pb.OrderTx
				if err := proto.Unmarshal(orderData, &oldOrderTx); err != nil {
					continue
				}
				// 转换为 OrderState
				orderState = &pb.OrderState{
					OrderId:     ev.OrderID,
					FilledBase:  "0", // 旧数据可能没有这些字段，重新计算
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
				orderState = &pb.OrderState{}
				if err := proto.Unmarshal(orderStateData, orderState); err != nil {
					continue
				}
			}
		}

		// 更新订单状态的成交信息
		filledBase, _ := decimal.NewFromString(orderState.FilledBase)
		filledQuote, _ := decimal.NewFromString(orderState.FilledQuote)

		// 更新成交量
		filledBase = filledBase.Add(ev.TradeAmt)
		filledQuote = filledQuote.Add(ev.TradeAmt.Mul(ev.TradePrice))

		orderState.FilledBase = filledBase.String()
		orderState.FilledQuote = filledQuote.String()
		orderState.IsFilled = ev.IsFilled
		if ev.IsFilled {
			orderState.Status = pb.OrderStateStatus_ORDER_FILLED
		}

		stateUpdates[ev.OrderID] = orderState

		// 同时确保订单实体被保存（兼容索引重建）
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

	// 生成WriteOps保存所有更新的订单状态
	for orderID, orderState := range stateUpdates {
		// 保存订单状态
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

		// 更新价格索引
		priceKey67, err := db.PriceToKey128(orderState.Price)
		if err != nil {
			return nil, fmt.Errorf("failed to convert price to key: %w", err)
		}

		// 删除旧的未成交索引
		oldIndexKey := keys.KeyOrderPriceIndex(pair, orderState.Side, false, priceKey67, orderID)
		ws = append(ws, WriteOp{
			Key:         oldIndexKey,
			Value:       nil,
			Del:         true,
			SyncStateDB: false,
			Category:    "index",
		})

		// 如果订单已完全成交，创建新的已成交索引
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
			// 如果未完全成交，保留未成交索引 (使用新的 Key 格式补全 Side)
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

	// 生成成交记录
	tradeRecordOps, err := h.generateTradeRecordsFromStates(newOrd, tradeEvents, stateUpdates, pair)
	if err != nil {
		return nil, fmt.Errorf("failed to generate trade records: %w", err)
	}
	ws = append(ws, tradeRecordOps...)

	// 更新账户余额
	// 将 accountCache 传入，确保同一笔交易内的余额修改是累积的
	accountBalanceOps, err := h.updateAccountBalancesFromStates(tradeEvents, stateUpdates, sv, accountCache)
	if err != nil {
		return nil, fmt.Errorf("failed to update account balances: %w", err)
	}
	ws = append(ws, accountBalanceOps...)

	return ws, nil
}

// generateWriteOpsFromTrades ... (此处省略，保持前面的更新)
// 注意：updateAccountBalances 函数已被弃用，此处直接将其内容清空或删除

// saveNewOrder 保存新订单（未成交的情况）
// 现在保存 OrderState 而不是 OrderTx（OrderTx 作为交易原文由 applyResult 统一保存到 v1_txraw_<txid>）
func (h *OrderTxHandler) saveNewOrder(ord *pb.OrderTx, sv StateView, pair string, accountCache map[string]*pb.Account) ([]WriteOp, error) {
	ws := make([]WriteOp, 0)

	// 如果 accountCache 中有更新后的账户，生成对应的 WriteOp
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

	// 创建 OrderState（可变状态）
	orderState := &pb.OrderState{
		OrderId:          ord.Base.TxId,
		FilledBase:       "0",
		FilledQuote:      "0",
		IsFilled:         false,
		Status:           pb.OrderStateStatus_ORDER_OPEN,
		CreateHeight:     ord.Base.ExecutedHeight,
		LastUpdateHeight: ord.Base.ExecutedHeight,
		// 冗余字段，方便查询
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

	// 同时保存订单实体，以便索引重建逻辑（RebuildOrderPriceIndexes）能正常工作
	orderKey := keys.KeyOrderTx(ord.Base.TxId)
	orderData, _ := proto.Marshal(ord)
	ws = append(ws, WriteOp{
		Key:         orderKey,
		Value:       orderData,
		Del:         false,
		SyncStateDB: true,
		Category:    "order",
	})

	// 4. 读取独立存储的订单列表并添加新订单 (Phase 2 重构：改为离散存储)
	accOrderItemKey := keys.KeyAccountOrderItem(ord.Base.FromAddress, ord.Base.TxId)
	ws = append(ws, WriteOp{
		Key:         accOrderItemKey,
		Value:       []byte{1}, // 仅作为标记存在
		Del:         false,
		SyncStateDB: true,
		Category:    "acc_orders_item",
	})

	// 5. 保存账户更新（如果有需要，比如 Nonce 或余额已在外部更新）
	// 注意：在下单逻辑中，账户由于冻结余额，已经在 handleAddNewOrder 中被 Marshal 过了。

	// 创建价格索引（基于 OrderState.IsFilled）
	priceKey67, err := db.PriceToKey128(ord.Price)
	if err != nil {
		return nil, fmt.Errorf("failed to convert price to key: %w", err)
	}

	// Phase 2: 增加 side 参数区分买卖
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

// generateTradeRecords 根据撮合事件生成成交记录
// 撮合事件成对出现（taker 和 maker 各一个），需要合并成一条成交记录
func (h *OrderTxHandler) generateTradeRecords(
	newOrd *pb.OrderTx,
	tradeEvents []matching.TradeUpdate,
	orderUpdates map[string]*pb.OrderTx,
	pair string,
) ([]WriteOp, error) {
	ws := make([]WriteOp, 0)

	// 新订单是 taker，撮合事件成对出现
	// 遍历事件，每两个事件生成一条成交记录
	for i := 0; i < len(tradeEvents)-1; i += 2 {
		takerEv := tradeEvents[i]
		makerEv := tradeEvents[i+1]

		// 确定 taker 和 maker
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

		// 生成成交 ID（使用 taker 订单 ID + maker 订单 ID + 索引）
		tradeID := fmt.Sprintf("%s_%s_%d", takerOrderID[:8], makerOrderID[:8], i/2)
		timestamp := utils.NowRFC3339()

		// 创建成交记录
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

		// 使用时间戳作为 key 前缀，让最新的成交记录排在前面
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

// generateTradeRecordsFromStates 使用 OrderState 生成成交记录
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

		// 安全截取订单 ID，避免越界
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

// updateAccountBalancesFromStates 使用 OrderState 更新账户余额
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

	for _, ev := range tradeEvents {
		orderState, ok := stateUpdates[ev.OrderID]
		if !ok {
			continue
		}

		address := orderState.Owner
		account, ok := accountCache[address]
		if !ok {
			accountKey := keys.KeyAccount(address)
			accountData, exists, err := sv.Get(accountKey)
			if err != nil || !exists {
				return nil, fmt.Errorf("account not found: %s", address)
			}

			account = &pb.Account{}
			if err := proto.Unmarshal(accountData, account); err != nil {
				return nil, fmt.Errorf("failed to unmarshal account %s: %w", address, err)
			}
			accountCache[address] = account
		}

		if account.Balances == nil {
			account.Balances = make(map[string]*pb.TokenBalance)
		}
		if account.Balances[orderState.BaseToken] == nil {
			account.Balances[orderState.BaseToken] = &pb.TokenBalance{
				Balance:            "0",
				MinerLockedBalance: "0",
			}
		}
		if account.Balances[orderState.QuoteToken] == nil {
			account.Balances[orderState.QuoteToken] = &pb.TokenBalance{
				Balance:            "0",
				MinerLockedBalance: "0",
			}
		}

		baseBalance, err := decimal.NewFromString(account.Balances[orderState.BaseToken].Balance)
		if err != nil {
			baseBalance = decimal.Zero
		}
		quoteBalance, err := decimal.NewFromString(account.Balances[orderState.QuoteToken].Balance)
		if err != nil {
			quoteBalance = decimal.Zero
		}

		if orderState.Side == pb.OrderSide_SELL {
			// 卖单：
			// 1. 扣除 OrderFrozenBalance 中的成交部分（这是下单时预扣的）
			frozen, _ := decimal.NewFromString(account.Balances[orderState.BaseToken].OrderFrozenBalance)
			account.Balances[orderState.BaseToken].OrderFrozenBalance = frozen.Sub(ev.TradeAmt).String()

			// 2. 增加 QuoteToken 余额（成交得到的）
			tradeQuoteAmt := ev.TradeAmt.Mul(ev.TradePrice)
			newQuoteBalance := quoteBalance.Add(tradeQuoteAmt)
			account.Balances[orderState.QuoteToken].Balance = newQuoteBalance.String()
		} else {
			// 买单：
			// 1. 扣除 OrderFrozenBalance 中的成交部分（这是下单时预扣的 QuoteToken）
			// 注意：这里需要考虑“价格优化”退款。
			// 下单时冻结的是：amount * LimitPrice
			// 实际消耗的是：tradeAmt * MatchPrice
			// 冻结额度消耗应按照 LimitPrice 计算，以确保最后能完全释放
			limitPrice, _ := decimal.NewFromString(orderState.Price)
			frozenToDeduct := ev.TradeAmt.Mul(limitPrice)

			frozen, _ := decimal.NewFromString(account.Balances[orderState.QuoteToken].OrderFrozenBalance)
			account.Balances[orderState.QuoteToken].OrderFrozenBalance = frozen.Sub(frozenToDeduct).String()

			// 2. 增加 BaseToken 余额（买到的币）
			newBaseBalance := baseBalance.Add(ev.TradeAmt)
			account.Balances[orderState.BaseToken].Balance = newBaseBalance.String()

			// 3. 处理价格优化退款：如果 MatchPrice < LimitPrice，将差额退回 liquid balance
			if ev.TradePrice.LessThan(limitPrice) {
				priceDiff := limitPrice.Sub(ev.TradePrice)
				refundQuote := ev.TradeAmt.Mul(priceDiff)

				// 这里的 refundQuote 其实已经在上面从 OrderFrozenBalance 中扣除了（因为按 LimitPrice 扣的）
				// 而用户实际只需要付 ev.TradeAmt * ev.TradePrice。
				// 所以差额补回到 Balance。
				currentQuoteBal, _ := decimal.NewFromString(account.Balances[orderState.QuoteToken].Balance)
				account.Balances[orderState.QuoteToken].Balance = currentQuoteBal.Add(refundQuote).String()
			}
		}

		// 检查是否超过 MaxUint256（恢复原有检查 logic）
		maxUint256Dec := decimal.NewFromBigInt(MaxUint256, 0)
		for _, bal := range account.Balances {
			b, _ := decimal.NewFromString(bal.Balance)
			if b.GreaterThan(maxUint256Dec) {
				return nil, fmt.Errorf("balance overflow")
			}
			f, _ := decimal.NewFromString(bal.OrderFrozenBalance)
			if f.IsNegative() {
				// 强制归零，防止负数冻结余额导致账户锁死
				bal.OrderFrozenBalance = "0"
			}
		}
	}

	for address, account := range accountCache {
		accountKey := keys.KeyAccount(address)
		accountData, err := proto.Marshal(account)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal account %s: %w", address, err)
		}

		ws = append(ws, WriteOp{
			Key:         accountKey,
			Value:       accountData,
			Del:         false,
			SyncStateDB: true,
			Category:    "account",
		})
	}

	return ws, nil
}
