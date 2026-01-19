package handlers

import (
	"dex/keys"
	"dex/pb"
	"dex/utils"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
)

// OrderBookEntry 订单簿条目
type OrderBookEntry struct {
	Price  string `json:"price"`
	Amount string `json:"amount"`
	Total  string `json:"total"`
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
		filledBase, _ := decimal.NewFromString(order.FilledBase)
		filledQuote, _ := decimal.NewFromString(order.FilledQuote)

		filledTrade := filledBase
		if order.BaseToken > order.QuoteToken {
			filledTrade = filledQuote
		}
		remain := amount.Sub(filledTrade)

		// 根据 base_token/quote_token 顺序判断买卖方向（向后兼容）
		side := "buy"
		if order.BaseToken < order.QuoteToken {
			side = "sell"
		}

		debugEntries = append(debugEntries, OrderBookDebugEntry{
			OrderID:     orderID,
			Price:       order.Price,
			Amount:      order.Amount,
			FilledBase:  order.FilledBase,
			FilledQuote: order.FilledQuote,
			Remain:      remain.String(),
			IsFilled:    order.IsFilled,
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
	indexData, err := hm.dbManager.ScanOrdersByPairs([]string{pair})
	if err != nil {
		http.Error(w, "failed to scan orders: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 聚合买卖盘
	bidPrices := make(map[string]decimal.Decimal) // price -> total amount
	askPrices := make(map[string]decimal.Decimal)

	// 从索引 key 中提取订单 ID，然后加载订单数据
	pairIndexes := indexData[pair]
	for indexKey := range pairIndexes {
		// 索引 key 格式: v1_pair:<pair>|is_filled:false|price:<67位>|order_id:<txID>
		// 从 key 中提取 order_id
		orderID := extractOrderIDFromIndexKey(indexKey)
		if orderID == "" {
			continue
		}

		// 加载订单数据
		orderKey := keys.KeyOrder(orderID)
		orderBytes, err := hm.dbManager.Get(orderKey)
		if err != nil || orderBytes == nil {
			continue
		}

		var order pb.OrderTx
		if err := proto.Unmarshal(orderBytes, &order); err != nil {
			continue
		}
		if order.IsFilled {
			continue // 跳过已完全成交的订单
		}

		if _, err := decimal.NewFromString(order.Price); err != nil {
			continue
		}
		amount, err := decimal.NewFromString(order.Amount)
		if err != nil {
			continue
		}
		filledBase, _ := decimal.NewFromString(order.FilledBase)
		filledQuote, _ := decimal.NewFromString(order.FilledQuote)
		filledTrade := filledBase
		if order.BaseToken > order.QuoteToken {
			filledTrade = filledQuote
		}
		remainAmount := amount.Sub(filledTrade)
		if remainAmount.LessThanOrEqual(decimal.Zero) {
			continue
		}

		priceStr := order.Price
		// 根据 base_token/quote_token 顺序判断买卖方向（向后兼容）
		// - base_token < quote_token（字母顺序在前）→ SELL（卖出 base_token）
		// - base_token > quote_token（字母顺序在后）→ BUY（用 base_token 买入 quote_token）
		isSell := order.BaseToken < order.QuoteToken
		if isSell {
			askPrices[priceStr] = askPrices[priceStr].Add(remainAmount)
		} else {
			bidPrices[priceStr] = bidPrices[priceStr].Add(remainAmount)
		}
	}

	// 转换为响应格式
	bids := aggregatePriceToEntries(bidPrices, true)  // 降序
	asks := aggregatePriceToEntries(askPrices, false) // 升序

	resp := OrderBookResponse{
		Pair:       pair,
		Bids:       bids,
		Asks:       asks,
		LastUpdate: utils.NowRFC3339(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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

// aggregatePriceToEntries 将价格聚合转换为条目列表
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
	prefix := keys.KeyTradeRecordPrefix(pair)
	trades, err := hm.dbManager.ScanByPrefix(prefix, 100) // 最多返回100条
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
