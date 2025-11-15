package vm

import (
	"dex/db"
	"dex/keys"
	"dex/matching"
	"dex/pb"
	"dex/utils"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
)

// OrderTxHandler 订单交易处理器
type OrderTxHandler struct {
	// 可以注入其他依赖，如OrderBookManager等
}

func (h *OrderTxHandler) Kind() string {
	return "order"
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

	// 5. 重建订单簿：从StateView扫描当前交易对的所有未成交订单
	orderBook, err := h.rebuildOrderBook(sv, pair)
	if err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  fmt.Sprintf("failed to rebuild order book: %v", err),
		}, err
	}

	// 6. 收集撮合事件
	var tradeEvents []matching.TradeUpdate
	orderBook.SetTradeSink(func(ev matching.TradeUpdate) {
		tradeEvents = append(tradeEvents, ev)
	})

	// 7. 将新订单转换为matching.Order并添加到订单簿
	newOrder, err := h.convertToMatchingOrder(ord)
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

// rebuildOrderBook 从StateView重建指定交易对的订单簿
func (h *OrderTxHandler) rebuildOrderBook(sv StateView, pair string) (*matching.OrderBook, error) {
	// 创建订单簿，使用TradeSink收集事件
	ob := matching.NewOrderBookWithSink(nil)

	// 扫描所有未成交订单的价格索引
	prefix := keys.KeyOrderPriceIndexPrefix(pair, false)
	indexMap, err := sv.Scan(prefix)
	if err != nil {
		return nil, fmt.Errorf("failed to scan order price index: %w", err)
	}

	// 从索引key中提取订单ID并加载订单
	for indexKey := range indexMap {
		// 索引key格式: v1_pair:<pair>|is_filled:false|price:<67位>|order_id:<txID>
		// 提取order_id部分
		orderID := extractOrderIDFromIndexKey(indexKey)
		if orderID == "" {
			continue
		}

		// 加载完整订单
		orderKey := keys.KeyOrder(orderID)
		orderData, exists, err := sv.Get(orderKey)
		if err != nil || !exists {
			continue // 跳过无法加载的订单
		}

		var orderTx pb.OrderTx
		if err := json.Unmarshal(orderData, &orderTx); err != nil {
			continue
		}

		// 转换为matching.Order
		matchOrder, err := h.convertToMatchingOrder(&orderTx)
		if err != nil {
			continue
		}

		// 添加到订单簿（不触发撮合，因为我们只是重建状态）
		// 注意：这里直接添加，不会触发撮合事件，因为sink是nil
		_ = ob.AddOrder(matchOrder)
	}

	return ob, nil
}

// extractOrderIDFromIndexKey 从价格索引key中提取订单ID
// 索引key格式: v1_pair:<pair>|is_filled:false|price:<67位>|order_id:<txID>
func extractOrderIDFromIndexKey(indexKey string) string {
	parts := strings.Split(indexKey, "|order_id:")
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}

// convertToMatchingOrder 将pb.OrderTx转换为matching.Order
func (h *OrderTxHandler) convertToMatchingOrder(ord *pb.OrderTx) (*matching.Order, error) {
	if ord == nil || ord.Base == nil {
		return nil, fmt.Errorf("invalid order")
	}

	price, err := decimal.NewFromString(ord.Price)
	if err != nil {
		return nil, fmt.Errorf("invalid price: %w", err)
	}

	amount, err := decimal.NewFromString(ord.Amount)
	if err != nil {
		return nil, fmt.Errorf("invalid amount: %w", err)
	}

	// 计算剩余数量 = 总数量 - 已成交数量
	filledBase, _ := decimal.NewFromString(ord.FilledBase)
	remainingAmount := amount.Sub(filledBase)
	if remainingAmount.LessThan(decimal.Zero) {
		remainingAmount = decimal.Zero
	}

	// TODO: 确定订单方向（BUY/SELL）
	// 目前的设计问题：OrderTx没有明确的买卖方向字段
	// 临时方案：根据base_token和quote_token的字典序判断
	// 如果base_token < quote_token，认为是BUY，否则是SELL
	// 这是一个简化的假设，实际应该有明确的direction字段
	side := matching.BUY
	if ord.BaseToken > ord.QuoteToken {
		side = matching.SELL
	}

	return &matching.Order{
		ID:     ord.Base.TxId,
		Side:   side,
		Price:  price,
		Amount: remainingAmount,
	}, nil
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

		filledBase = filledBase.Add(ev.TradeAmt)
		filledQuote = filledQuote.Add(ev.TradeAmt.Mul(ev.TradePrice))

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

	// TODO: 更新账户余额和持仓
	// 这里需要根据成交信息更新买卖双方的余额

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
