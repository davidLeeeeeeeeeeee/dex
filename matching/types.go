package matching

import (
	"dex/pb"
	"errors"
	"fmt"

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

// OrderBook 包含买、卖双方的价格映射、堆，以及订单ID索引
type OrderBook struct {
	buyMap  map[decimal.Decimal]*PriceLevel
	sellMap map[decimal.Decimal]*PriceLevel

	buyHeap  *MaxPriceHeap // 堆顶是最高买价
	sellHeap *MinPriceHeap // 堆顶是最低卖价

	orderIndex map[string]*OrderRef

	// 用于发送撮合事件
	tradeCh chan<- TradeUpdate
}

func NewOrderBook(tradeCh chan<- TradeUpdate) *OrderBook {
	return &OrderBook{
		buyMap:     make(map[decimal.Decimal]*PriceLevel),
		sellMap:    make(map[decimal.Decimal]*PriceLevel),
		buyHeap:    &MaxPriceHeap{},
		sellHeap:   &MinPriceHeap{},
		orderIndex: make(map[string]*OrderRef),
		tradeCh:    tradeCh,
	}
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
	// side：示例中, OrderOp_ADD = BUY, OrderOp_REMOVE = SELL
	var side OrderSide
	if o.Op == pb.OrderOp_ADD {
		side = BUY
	} else {
		side = SELL
	}

	price, err := decimal.NewFromString(o.Price)
	if err != nil {
		return nil, fmt.Errorf("parse price error: %v", err)
	}
	amount, err := decimal.NewFromString(o.Amount)
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
