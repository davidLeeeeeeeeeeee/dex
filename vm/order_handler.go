package vm

import (
	"dex/db"
	"dex/keys"
	"dex/matching"
	"dex/pb"
	"dex/utils"
	"encoding/json"
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
		}, fmt.Errorf("invalid order amount: %s", ord.Amount)
	}

	priceDec, err := decimal.NewFromString(ord.Price)
	if err != nil || priceDec.LessThanOrEqual(decimal.Zero) {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "invalid order price",
		}, fmt.Errorf("invalid order price: %s", ord.Price)
	}

	// 2. 读取账户
	accountKey := keys.KeyAccount(ord.Base.FromAddress)
	accountData, exists, err := sv.Get(accountKey)
	if err != nil || !exists {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "account not found",
		}, fmt.Errorf("account not found: %s", ord.Base.FromAddress)
	}

	var account pb.Account
	if err := json.Unmarshal(accountData, &account); err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse account",
		}, err
	}

	// 3. 检查base_token余额
	if account.Balances == nil || account.Balances[ord.BaseToken] == nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "insufficient base token balance",
		}, fmt.Errorf("no balance for base token: %s", ord.BaseToken)
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
	newOrder, err := convertToMatchingOrder(ord)
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
	ws, err := h.generateWriteOpsFromTrades(ord, tradeEvents, sv, pair)
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
func (h *OrderTxHandler) handleRemoveOrder(ord *pb.OrderTx, sv StateView) ([]WriteOp, *Receipt, error) {
	if ord.OpTargetId == "" {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "op_target_id is required for REMOVE operation",
		}, fmt.Errorf("op_target_id is required")
	}

	// 读取要撤销的订单
	targetOrderKey := keys.KeyOrder(ord.OpTargetId)
	targetOrderData, exists, err := sv.Get(targetOrderKey)
	if err != nil || !exists {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "target order not found",
		}, fmt.Errorf("target order not found: %s", ord.OpTargetId)
	}

	var targetOrder pb.OrderTx
	if err := json.Unmarshal(targetOrderData, &targetOrder); err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse target order",
		}, err
	}

	// 验证权限：只有订单创建者可以撤单
	if targetOrder.Base.FromAddress != ord.Base.FromAddress {
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
	if err := json.Unmarshal(accountData, &account); err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse account",
		}, err
	}

	ws := make([]WriteOp, 0)

	// 删除订单
	ws = append(ws, WriteOp{
		Key:         targetOrderKey,
		Value:       nil,
		Del:         true,
		SyncStateDB: false,
		Category:    "order",
	})

	// 从账户的订单列表中移除
	if account.Orders != nil {
		newOrders := make([]string, 0)
		for _, orderId := range account.Orders {
			if orderId != ord.OpTargetId {
				newOrders = append(newOrders, orderId)
			}
		}
		account.Orders = newOrders
	}

	// 保存更新后的账户
	updatedAccountData, err := json.Marshal(&account)
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
	pair := fmt.Sprintf("%s_%s", targetOrder.BaseToken, targetOrder.QuoteToken)
	priceIndexKey := keys.KeyOrderPriceIndex(pair, targetOrder.IsFilled, targetOrder.Price, ord.OpTargetId)

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
func (h *OrderTxHandler) generateWriteOpsFromTrades(
	newOrd *pb.OrderTx,
	tradeEvents []matching.TradeUpdate,
	sv StateView,
	pair string,
) ([]WriteOp, error) {
	ws := make([]WriteOp, 0)

	// 如果没有撮合事件，说明订单未成交，直接保存订单
	if len(tradeEvents) == 0 {
		return h.saveNewOrder(newOrd, sv, pair)
	}

	// 处理每个撮合事件
	orderUpdates := make(map[string]*pb.OrderTx) // orderID -> updated OrderTx

	for _, ev := range tradeEvents {
		// 加载订单
		var orderTx *pb.OrderTx
		if ev.OrderID == newOrd.Base.TxId {
			// 这是新订单
			orderTx = newOrd
		} else {
			// 这是已存在的订单，从StateView加载
			orderKey := keys.KeyOrder(ev.OrderID)
			orderData, exists, err := sv.Get(orderKey)
			if err != nil || !exists {
				continue
			}
			orderTx = &pb.OrderTx{}
			if err := proto.Unmarshal(orderData, orderTx); err != nil {
				continue
			}
		}

		// 更新订单的成交信息
		filledBase, _ := decimal.NewFromString(orderTx.FilledBase)
		filledQuote, _ := decimal.NewFromString(orderTx.FilledQuote)

		// 根据订单方向更新成交量
		// ev.TradeAmt 是交易对的 base currency 数量（例如 BTC_USDT 中的 BTC）
		// ev.TradePrice 是 quote_token/base_token 的价格（例如 USDT/BTC）
		if orderTx.BaseToken < orderTx.QuoteToken {
			// 卖单：base_token 是交易对的第一个币种
			filledBase = filledBase.Add(ev.TradeAmt)
			filledQuote = filledQuote.Add(ev.TradeAmt.Mul(ev.TradePrice))
		} else {
			// 买单：base_token 是交易对的第二个币种
			filledBase = filledBase.Add(ev.TradeAmt.Mul(ev.TradePrice))
			filledQuote = filledQuote.Add(ev.TradeAmt)
		}

		orderTx.FilledBase = filledBase.String()
		orderTx.FilledQuote = filledQuote.String()
		orderTx.IsFilled = ev.IsFilled

		orderUpdates[ev.OrderID] = orderTx
	}

	// 生成WriteOps保存所有更新的订单
	for orderID, orderTx := range orderUpdates {
		// 保存订单
		orderKey := keys.KeyOrder(orderID)
		orderData, err := proto.Marshal(orderTx)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal order %s: %w", orderID, err)
		}

		ws = append(ws, WriteOp{
			Key:         orderKey,
			Value:       orderData,
			Del:         false,
			SyncStateDB: false,
			Category:    "order",
		})

		// 更新价格索引
		priceKey67, err := db.PriceToKey128(orderTx.Price)
		if err != nil {
			return nil, fmt.Errorf("failed to convert price to key: %w", err)
		}

		// 删除旧的未成交索引
		oldIndexKey := keys.KeyOrderPriceIndex(pair, false, priceKey67, orderID)
		ws = append(ws, WriteOp{
			Key:         oldIndexKey,
			Value:       nil,
			Del:         true,
			SyncStateDB: false,
			Category:    "index",
		})

		// 如果订单已完全成交，创建新的已成交索引
		if orderTx.IsFilled {
			newIndexKey := keys.KeyOrderPriceIndex(pair, true, priceKey67, orderID)
			indexData, _ := proto.Marshal(&pb.OrderPriceIndex{Ok: true})
			ws = append(ws, WriteOp{
				Key:         newIndexKey,
				Value:       indexData,
				Del:         false,
				SyncStateDB: false,
				Category:    "index",
			})
		} else {
			// 如果未完全成交，保留未成交索引（实际上已经存在，这里重新写入）
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

	// 更新账户余额
	// 根据撮合事件更新买卖双方的余额
	accountBalanceOps, err := h.updateAccountBalances(tradeEvents, orderUpdates, sv)
	if err != nil {
		return nil, fmt.Errorf("failed to update account balances: %w", err)
	}
	ws = append(ws, accountBalanceOps...)

	return ws, nil
}

// updateAccountBalances 根据撮合事件更新账户余额
//
// 撮合逻辑：
// - 对于订单 A (base_token=X, quote_token=Y, amount=a, price=p)
//   - 成交量 tradeAmt（base_token 数量）
//   - 成交金额 tradeAmt * price（quote_token 数量）
//   - 订单持有者：减少 base_token，增加 quote_token
//
// 例如：
// - Alice 卖单：base_token=BTC, quote_token=USDT, amount=1, price=50000
//   - 成交 0.5 BTC
//   - Alice: BTC -= 0.5, USDT += 0.5 * 50000 = 25000
// - Bob 买单：base_token=USDT, quote_token=BTC, amount=25000, price=50000
//   - 成交 25000 USDT
//   - Bob: USDT -= 25000, BTC += 25000 / 50000 = 0.5
func (h *OrderTxHandler) updateAccountBalances(
	tradeEvents []matching.TradeUpdate,
	orderUpdates map[string]*pb.OrderTx,
	sv StateView,
) ([]WriteOp, error) {
	ws := make([]WriteOp, 0)
	accountCache := make(map[string]*pb.Account) // address -> Account

	for _, ev := range tradeEvents {
		// 1. 获取订单信息
		orderTx, ok := orderUpdates[ev.OrderID]
		if !ok {
			continue
		}

		// 2. 加载或获取缓存的账户
		address := orderTx.Base.FromAddress
		account, ok := accountCache[address]
		if !ok {
			accountKey := keys.KeyAccount(address)
			accountData, exists, err := sv.Get(accountKey)
			if err != nil || !exists {
				return nil, fmt.Errorf("account not found: %s", address)
			}

			account = &pb.Account{}
			if err := json.Unmarshal(accountData, account); err != nil {
				return nil, fmt.Errorf("failed to unmarshal account %s: %w", address, err)
			}
			accountCache[address] = account
		}

		// 3. 初始化余额（如果不存在）
		if account.Balances == nil {
			account.Balances = make(map[string]*pb.TokenBalance)
		}
		if account.Balances[orderTx.BaseToken] == nil {
			account.Balances[orderTx.BaseToken] = &pb.TokenBalance{
				Balance:                "0",
				MinerLockedBalance:     "0",
				CandidateLockedBalance: "0",
			}
		}
		if account.Balances[orderTx.QuoteToken] == nil {
			account.Balances[orderTx.QuoteToken] = &pb.TokenBalance{
				Balance:                "0",
				MinerLockedBalance:     "0",
				CandidateLockedBalance: "0",
			}
		}

		// 4. 计算余额变化
		// TradeAmt 是撮合引擎返回的成交量，单位是交易对的基础币种（按字母排序后的第一个币种）
		// 例如对于 BTC_USDT 交易对，TradeAmt 的单位是 BTC
		//
		// 对于卖单（base_token=BTC, quote_token=USDT）：
		//   - 减少 BTC：TradeAmt
		//   - 增加 USDT：TradeAmt * TradePrice
		//
		// 对于买单（base_token=USDT, quote_token=BTC）：
		//   - 减少 USDT：TradeAmt * TradePrice
		//   - 增加 BTC：TradeAmt
		baseBalance, err := decimal.NewFromString(account.Balances[orderTx.BaseToken].Balance)
		if err != nil {
			baseBalance = decimal.Zero
		}
		quoteBalance, err := decimal.NewFromString(account.Balances[orderTx.QuoteToken].Balance)
		if err != nil {
			quoteBalance = decimal.Zero
		}

		var newBaseBalance, newQuoteBalance decimal.Decimal
		var baseDecrease, quoteIncrease decimal.Decimal

		if orderTx.BaseToken < orderTx.QuoteToken {
			// 卖单：base_token 是交易对的第一个币种
			baseDecrease = ev.TradeAmt
			quoteIncrease = ev.TradeAmt.Mul(ev.TradePrice)
		} else {
			// 买单：base_token 是交易对的第二个币种
			baseDecrease = ev.TradeAmt.Mul(ev.TradePrice)
			quoteIncrease = ev.TradeAmt
		}

		newBaseBalance = baseBalance.Sub(baseDecrease)
		if newBaseBalance.LessThan(decimal.Zero) {
			return nil, fmt.Errorf("insufficient %s balance for account %s (current=%s, need=%s)",
				orderTx.BaseToken, address, baseBalance, baseDecrease)
		}

		newQuoteBalance = quoteBalance.Add(quoteIncrease)

		// 更新余额
		account.Balances[orderTx.BaseToken].Balance = newBaseBalance.String()
		account.Balances[orderTx.QuoteToken].Balance = newQuoteBalance.String()
	}

	// 5. 保存所有更新的账户
	for address, account := range accountCache {
		accountKey := keys.KeyAccount(address)
		accountData, err := json.Marshal(account)
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

// saveNewOrder 保存新订单（未成交的情况）
func (h *OrderTxHandler) saveNewOrder(ord *pb.OrderTx, sv StateView, pair string) ([]WriteOp, error) {
	ws := make([]WriteOp, 0)

	// 保存订单
	orderKey := keys.KeyOrder(ord.Base.TxId)
	orderData, err := proto.Marshal(ord)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal order: %w", err)
	}

	ws = append(ws, WriteOp{
		Key:         orderKey,
		Value:       orderData,
		Del:         false,
		SyncStateDB: false,
		Category:    "order",
	})

	// 读取账户并添加订单到账户的订单列表
	accountKey := keys.KeyAccount(ord.Base.FromAddress)
	accountData, exists, err := sv.Get(accountKey)
	if err == nil && exists {
		var account pb.Account
		if err := json.Unmarshal(accountData, &account); err == nil {
			if account.Orders == nil {
				account.Orders = make([]string, 0)
			}
			account.Orders = append(account.Orders, ord.Base.TxId)

			updatedAccountData, _ := json.Marshal(&account)
			ws = append(ws, WriteOp{
				Key:         accountKey,
				Value:       updatedAccountData,
				Del:         false,
				SyncStateDB: true,
				Category:    "account",
			})
		}
	}

	// 创建价格索引
	priceKey67, err := db.PriceToKey128(ord.Price)
	if err != nil {
		return nil, fmt.Errorf("failed to convert price to key: %w", err)
	}

	priceIndexKey := keys.KeyOrderPriceIndex(pair, ord.IsFilled, priceKey67, ord.Base.TxId)
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
