package handlers

import (
	"dex/keys"
	"dex/pb"
	"dex/utils"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
)

// OrderBookEntry 订单簿条目
type OrderBookEntry struct {
	Price          string `json:"price"`
	Amount         string `json:"amount"`
	Total          string `json:"total"`
	PendingCount   int    `json:"pendingCount"`   // 待确认订单数量
	ConfirmedCount int    `json:"confirmedCount"` // 已确认订单数量
}

// OrderBookResponse 订单簿响应
type OrderBookResponse struct {
	Pair       string           `json:"pair"`
	Bids       []OrderBookEntry `json:"bids"` // 买单（按价格降序）
	Asks       []OrderBookEntry `json:"asks"` // 卖单（按价格升序）
	LastUpdate string           `json:"lastUpdate"`
}

// OrderBookDebugEntry 用于诊断的详细订单条目
type OrderBookDebugEntry struct {
	OrderID     string `json:"order_id"`
	Price       string `json:"price"`
	Amount      string `json:"amount"`
	FilledBase  string `json:"filled_base"`
	FilledQuote string `json:"filled_quote"`
	Remain      string `json:"remain"`
	IsFilled    bool   `json:"is_filled"`
	Side        string `json:"side"`
	IndexKey    string `json:"index_key"`
}

// HandleOrderBookDebug 用于诊断订单簿数据不一致问题
func (hm *HandlerManager) HandleOrderBookDebug(w http.ResponseWriter, r *http.Request) {
	pair := r.URL.Query().Get("pair")
	if pair == "" {
		http.Error(w, "missing pair parameter", http.StatusBadRequest)
		return
	}

	// 从数据库扫描未成交订单索引
	indexData, err := hm.dbManager.ScanOrdersByPairs([]string{pair})
	if err != nil {
		http.Error(w, "failed to scan orders: "+err.Error(), http.StatusInternalServerError)
		return
	}

	debugEntries := make([]OrderBookDebugEntry, 0)
	pairIndexes := indexData[pair]

	for indexKey := range pairIndexes {
		orderID := extractOrderIDFromIndexKey(indexKey)
		if orderID == "" {
			continue
		}

		// 优先从 OrderState 读取（新版本）
		orderStateKey := keys.KeyOrderState(orderID)
		orderStateBytes, err := hm.dbManager.Get(orderStateKey)
		if err == nil && orderStateBytes != nil {
			var orderState pb.OrderState
			if err := proto.Unmarshal(orderStateBytes, &orderState); err != nil {
				continue
			}

			amount, _ := decimal.NewFromString(orderState.Amount)
			filledBase, _ := decimal.NewFromString(orderState.FilledBase)
			remain := amount.Sub(filledBase)

			side := "buy"
			if orderState.Side == pb.OrderSide_SELL {
				side = "sell"
			}

			debugEntries = append(debugEntries, OrderBookDebugEntry{
				OrderID:     orderID,
				Price:       orderState.Price,
				Amount:      orderState.Amount,
				FilledBase:  orderState.FilledBase,
				FilledQuote: orderState.FilledQuote,
				Remain:      remain.String(),
				IsFilled:    orderState.IsFilled,
				Side:        side,
				IndexKey:    indexKey,
			})
			continue
		}

		// 兼容旧数据：从 OrderTx 读取
		orderKey := keys.KeyOrder(orderID)
		orderBytes, err := hm.dbManager.Get(orderKey)
		if err != nil || orderBytes == nil {
			debugEntries = append(debugEntries, OrderBookDebugEntry{
				OrderID:  orderID,
				IndexKey: indexKey,
				Side:     "ERROR: order not found",
			})
			continue
		}

		var order pb.OrderTx
		if err := proto.Unmarshal(orderBytes, &order); err != nil {
			continue
		}

		amount, _ := decimal.NewFromString(order.Amount)
		// 旧版本没有 FilledBase，假设未成交
		remain := amount

		side := "buy"
		if order.Side == pb.OrderSide_SELL {
			side = "sell"
		}

		debugEntries = append(debugEntries, OrderBookDebugEntry{
			OrderID:     orderID,
			Price:       order.Price,
			Amount:      order.Amount,
			FilledBase:  "0",
			FilledQuote: "0",
			Remain:      remain.String(),
			IsFilled:    false,
			Side:        side,
			IndexKey:    indexKey,
		})
	}

	// 按价格排序
	sort.Slice(debugEntries, func(i, j int) bool {
		pi, _ := decimal.NewFromString(debugEntries[i].Price)
		pj, _ := decimal.NewFromString(debugEntries[j].Price)
		return pi.GreaterThan(pj)
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"pair":        pair,
		"total_count": len(debugEntries),
		"index_count": len(pairIndexes),
		"orders":      debugEntries,
	})
}

// HandleOrderBook 处理订单簿查询请求
func (hm *HandlerManager) HandleOrderBook(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleOrderBook")

	pair := r.URL.Query().Get("pair")
	if pair == "" {
		http.Error(w, "missing pair parameter", http.StatusBadRequest)
		return
	}

	// 从数据库扫描未成交订单索引
	// 175. 聚合买卖盘 - Phase 2: 使用 ScanKVWithLimit 优化
	bidPrices := make(map[string]*priceLevelDataForAgg) // price -> level data
	askPrices := make(map[string]*priceLevelDataForAgg)

	// 分别加载买盘和卖盘 (限制前端显示各 100 条深度)
	// 买盘 (Side_BUY = 0)
	buyPrefix := keys.KeyOrderPriceIndexPrefix(pair, pb.OrderSide_BUY, false)
	buyOrders, err := hm.dbManager.ScanKVWithLimitReverse(buyPrefix, 500)
	if err != nil {
		// Log the error but continue to process what we have, or return an error if critical
		// For now, we'll just skip if there's an error scanning buy orders
		// http.Error(w, "failed to scan buy orders: "+err.Error(), http.StatusInternalServerError)
		// return
	} else {
		for indexKey := range buyOrders {
			hm.processOrderForAgg(indexKey, bidPrices, askPrices)
		}
	}

	// 卖盘 (Side_SELL = 1)
	sellPrefix := keys.KeyOrderPriceIndexPrefix(pair, pb.OrderSide_SELL, false)
	sellOrders, err := hm.dbManager.ScanKVWithLimit(sellPrefix, 500)
	if err != nil {
		// Log the error but continue to process what we have, or return an error if critical
		// For now, we'll just skip if there's an error scanning sell orders
		// http.Error(w, "failed to scan sell orders: "+err.Error(), http.StatusInternalServerError)
		// return
	} else {
		for indexKey := range sellOrders {
			hm.processOrderForAgg(indexKey, bidPrices, askPrices)
		}
	}

	// 转换为响应格式
	bids := aggregatePriceToEntriesWithStatus(bidPrices, true)  // 降序
	asks := aggregatePriceToEntriesWithStatus(askPrices, false) // 升序

	resp := OrderBookResponse{
		Pair:       pair,
		Bids:       bids,
		Asks:       asks,
		LastUpdate: utils.NowRFC3339(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// processOrderForAgg 辅助函数，用于处理单个订单并聚合到价格层级
func (hm *HandlerManager) processOrderForAgg(indexKey string, bidPrices, askPrices map[string]*priceLevelDataForAgg) {
	orderID := extractOrderIDFromIndexKey(indexKey)
	if orderID == "" {
		return
	}

	// 优先从 OrderState 读取（新版本）
	orderStateKey := keys.KeyOrderState(orderID)
	orderStateBytes, err := hm.dbManager.Get(orderStateKey)
	if err == nil && orderStateBytes != nil {
		var orderState pb.OrderState
		if err := proto.Unmarshal(orderStateBytes, &orderState); err != nil {
			return
		}
		if orderState.IsFilled || orderState.Status == pb.OrderStateStatus_ORDER_CANCELLED {
			return
		}

		if _, err := decimal.NewFromString(orderState.Price); err != nil {
			return
		}
		amount, err := decimal.NewFromString(orderState.Amount)
		if err != nil {
			return
		}
		filledBase, _ := decimal.NewFromString(orderState.FilledBase)
		remainAmount := amount.Sub(filledBase)
		if remainAmount.LessThanOrEqual(decimal.Zero) {
			return
		}

		priceStr := orderState.Price
		// OrderState 没有 Base.Status，假设已确认
		isPending := false // For OrderState, we assume it's confirmed unless explicitly marked otherwise

		if orderState.Side == pb.OrderSide_SELL {
			if askPrices[priceStr] == nil {
				askPrices[priceStr] = &priceLevelDataForAgg{}
			}
			askPrices[priceStr].amount = askPrices[priceStr].amount.Add(remainAmount)
			if isPending {
				askPrices[priceStr].pendingCount++
			} else {
				askPrices[priceStr].confirmedCount++
			}
		} else { // BUY side
			if bidPrices[priceStr] == nil {
				bidPrices[priceStr] = &priceLevelDataForAgg{}
			}
			bidPrices[priceStr].amount = bidPrices[priceStr].amount.Add(remainAmount)
			if isPending {
				bidPrices[priceStr].pendingCount++
			} else {
				bidPrices[priceStr].confirmedCount++
			}
		}
	}
}

// extractOrderIDFromIndexKey 从索引 key 中提取订单 ID
// key 格式: v1_pair:<pair>|is_filled:false|price:<67位>|order_id:<txID>
func extractOrderIDFromIndexKey(key string) string {
	const marker = "|order_id:"
	idx := strings.LastIndex(key, marker)
	if idx == -1 {
		return ""
	}
	return key[idx+len(marker):]
}

// aggregatePriceToEntries 将价格聚合转换为条目列表（兼容旧接口）
func aggregatePriceToEntries(priceMap map[string]decimal.Decimal, descending bool) []OrderBookEntry {
	entries := make([]OrderBookEntry, 0, len(priceMap))
	cumTotal := decimal.Zero

	// 先按价格排序
	type pricePair struct {
		price  decimal.Decimal
		amount decimal.Decimal
	}
	var pairs []pricePair
	for priceStr, amount := range priceMap {
		price, _ := decimal.NewFromString(priceStr)
		pairs = append(pairs, pricePair{price: price, amount: amount})
	}

	sort.Slice(pairs, func(i, j int) bool {
		if descending {
			return pairs[i].price.GreaterThan(pairs[j].price)
		}
		return pairs[i].price.LessThan(pairs[j].price)
	})

	// 计算累计总量
	for _, p := range pairs {
		cumTotal = cumTotal.Add(p.amount.Mul(p.price))
		entries = append(entries, OrderBookEntry{
			Price:  p.price.String(),
			Amount: p.amount.String(),
			Total:  cumTotal.String(),
		})
	}

	return entries
}

// priceLevelDataForAgg 用于聚合时存储价格层级数据（避免与局部类型重复）
type priceLevelDataForAgg struct {
	amount         decimal.Decimal
	pendingCount   int
	confirmedCount int
}

// aggregatePriceToEntriesWithStatus 将价格聚合转换为条目列表（包含状态信息）
func aggregatePriceToEntriesWithStatus(priceMap map[string]*priceLevelDataForAgg, descending bool) []OrderBookEntry {
	entries := make([]OrderBookEntry, 0, len(priceMap))
	cumTotal := decimal.Zero

	// 先按价格排序
	type pricePair struct {
		price          decimal.Decimal
		amount         decimal.Decimal
		pendingCount   int
		confirmedCount int
	}
	var pairs []pricePair
	for priceStr, data := range priceMap {
		price, _ := decimal.NewFromString(priceStr)
		pairs = append(pairs, pricePair{
			price:          price,
			amount:         data.amount,
			pendingCount:   data.pendingCount,
			confirmedCount: data.confirmedCount,
		})
	}

	sort.Slice(pairs, func(i, j int) bool {
		if descending {
			return pairs[i].price.GreaterThan(pairs[j].price)
		}
		return pairs[i].price.LessThan(pairs[j].price)
	})

	// 计算累计总量
	for _, p := range pairs {
		cumTotal = cumTotal.Add(p.amount.Mul(p.price))
		entries = append(entries, OrderBookEntry{
			Price:          p.price.String(),
			Amount:         p.amount.String(),
			Total:          cumTotal.String(),
			PendingCount:   p.pendingCount,
			ConfirmedCount: p.confirmedCount,
		})
	}

	return entries
}

// TradeRecord 成交记录
type TradeRecord struct {
	ID           string `json:"id"`
	Time         string `json:"time"`
	Price        string `json:"price"`
	Amount       string `json:"amount"`
	Side         string `json:"side"` // "buy" or "sell"
	MakerOrderID string `json:"maker_order_id,omitempty"`
	TakerOrderID string `json:"taker_order_id,omitempty"`
}

// HandleTrades 处理成交记录查询请求
func (hm *HandlerManager) HandleTrades(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleTrades")

	pair := r.URL.Query().Get("pair")
	if pair == "" {
		http.Error(w, "missing pair parameter", http.StatusBadRequest)
		return
	}

	// 从数据库扫描成交记录
	limit := 100
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	prefix := keys.KeyTradeRecordPrefix(pair)
	trades, err := hm.dbManager.ScanByPrefix(prefix, limit)
	if err != nil {
		http.Error(w, "failed to scan trades: "+err.Error(), http.StatusInternalServerError)
		return
	}

	records := make([]TradeRecord, 0) // 初始化为空切片，确保 JSON 输出 [] 而不是 null
	for _, data := range trades {
		var trade pb.TradeRecord
		if err := proto.Unmarshal([]byte(data), &trade); err != nil {
			continue
		}
		side := "buy"
		if trade.TakerSide == pb.OrderSide_SELL {
			side = "sell"
		}
		records = append(records, TradeRecord{
			ID:           trade.TradeId,
			Time:         trade.Timestamp,
			Price:        trade.Price,
			Amount:       trade.Amount,
			Side:         side,
			MakerOrderID: trade.MakerOrderId,
			TakerOrderID: trade.TakerOrderId,
		})
	}

	// 按时间倒序排列，最新的在前面
	sort.Slice(records, func(i, j int) bool {
		return records[i].Time > records[j].Time
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(records)
}
