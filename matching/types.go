package matching

import (
	"dex/pb"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/shopspring/decimal"
)

//OrderBookManager 里持有一个 tradeCh，负责接收所有撮合事件。
//每个 OrderBook 在构造时拿到这个通道引用，每次 executeTrade() 完都往里发送 TradeUpdate。
//OrderBookManager 自己开 goroutine（handleTradeUpdates）持续从通道里读，然后去 db.UpdateOrderTxInDB 做数据库更新。
//这样即可实现“一旦有撮合就能立刻更新”，不管部分还是全部。

// TradeUpdate 表示一次撮合事件，可能是部分成交或全部成交
type TradeUpdate struct {
	OrderID    string          // 订单ID
	TradeAmt   decimal.Decimal // 本次撮合成交量
	TradePrice decimal.Decimal // 本次撮合使用的价格
	RemainAmt  decimal.Decimal // 订单剩余量(撮合后)
	IsFilled   bool            // 是否已完全成交
}

// TradeSink 用于接收撮合事件的回调函数。
// 在不同的使用场景里可以注入：
//   - 链下撮合服务：sink 把事件写入 channel/goroutine，再更新 DB
//   - VM 预执行：sink 把事件追加到 slice，交由 VM 生成 WriteOp
type TradeSink func(TradeUpdate)

// OrderBook 包含买、卖双方的价格映射、堆，以及订单ID索引
type OrderBook struct {
	mu sync.RWMutex // 保护并发访问

	buyMap  map[decimal.Decimal]*PriceLevel
	sellMap map[decimal.Decimal]*PriceLevel

	buyHeap  *MaxPriceHeap // 堆顶是最高买价
	sellHeap *MinPriceHeap // 堆顶是最低卖价

	orderIndex map[string]*OrderRef

	// 用于发送撮合事件（可选），由上层注入。
	onTrade TradeSink
}

// NewOrderBookWithSink 使用自定义 TradeSink 创建订单簿。
// sink 可以为 nil，此时撮合事件会被忽略（只更新内存订单簿状态）。
func NewOrderBookWithSink(sink TradeSink) *OrderBook {
	return &OrderBook{
		buyMap:     make(map[decimal.Decimal]*PriceLevel),
		sellMap:    make(map[decimal.Decimal]*PriceLevel),
		buyHeap:    &MaxPriceHeap{},
		sellHeap:   &MinPriceHeap{},
		orderIndex: make(map[string]*OrderRef),
		onTrade:    sink,
	}
}

// SetTradeSink 设置或更新 TradeSink，用于在订单簿创建后动态注入事件处理器
func (ob *OrderBook) SetTradeSink(sink TradeSink) {
	ob.onTrade = sink
}

// extractOrderTx 尝试从 AnyTx 提取 OrderTx
func extractOrderTx(a *pb.AnyTx) *pb.OrderTx {
	content := a.GetContent()
	otx, ok := content.(*pb.AnyTx_OrderTx)
	if !ok {
		return nil
	}
	return otx.OrderTx
}

// convertOrderTxToOrder 将 db.OrderTx 转为 match.go 里的 Order 结构
func convertOrderTxToOrder(o *pb.OrderTx) (*Order, error) {
	if o == nil || o.Base == nil {
		return nil, errors.New("orderTx invalid")
	}
	// 直接使用订单的 Side 字段判断买卖方向
	var side OrderSide
	if o.Side == pb.OrderSide_SELL {
		side = SELL
	} else {
		side = BUY
	}

	price, err := parsePositiveIntegerDecimal(o.Price, "price")
	if err != nil {
		return nil, fmt.Errorf("parse price error: %v", err)
	}
	amount, err := parsePositiveIntegerDecimal(o.Amount, "amount")
	if err != nil {
		return nil, fmt.Errorf("parse amount error: %v", err)
	}
	// 二次校验
	lowerBound := decimal.NewFromFloat(1e-33)
	upperBound := decimal.NewFromFloat(1e33)
	if price.Cmp(lowerBound) < 0 || price.Cmp(upperBound) > 0 {
		return nil, fmt.Errorf("price out of range [1e-33, 1e33]")
	}
	return &Order{
		ID:     o.Base.TxId,
		Side:   side,
		Price:  price,
		Amount: amount,
	}, nil
}

func parsePositiveIntegerDecimal(raw string, field string) (decimal.Decimal, error) {
	if raw == "" {
		return decimal.Zero, fmt.Errorf("%s is empty", field)
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return decimal.Zero, fmt.Errorf("%s must be integer digits", field)
		}
	}
	v, ok := new(big.Int).SetString(raw, 10)
	if !ok || v.Sign() <= 0 {
		return decimal.Zero, fmt.Errorf("%s must be positive integer", field)
	}
	return decimal.NewFromBigInt(v, 0), nil
}
