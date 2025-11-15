package matching

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

// TestOrderBook_WithSink_EmitsTradeUpdates
//
// 验证使用 NewOrderBookWithSink 创建的订单簿，在发生撮合时会
// 通过注入的 TradeSink 正确地回调撮合事件，而不是依赖内部 channel。
func TestOrderBook_WithSink_EmitsTradeUpdates(t *testing.T) {
	var events []TradeUpdate

	// 使用自定义 sink 收集撮合事件
	ob := NewOrderBookWithSink(func(ev TradeUpdate) {
		events = append(events, ev)
	})

	// 1) 先挂两个买单
	// buy1: price=10, amount=5
	// buy2: price=9,  amount=4
	err := ob.AddOrder(&Order{
		ID:     "buy1",
		Side:   BUY,
		Price:  decimal.NewFromInt(10),
		Amount: decimal.NewFromInt(5),
	})
	assert.NoError(t, err)

	err = ob.AddOrder(&Order{
		ID:     "buy2",
		Side:   BUY,
		Price:  decimal.NewFromInt(9),
		Amount: decimal.NewFromInt(4),
	})
	assert.NoError(t, err)

	// 此时没有卖单，不应产生撮合事件
	assert.Len(t, events, 0)

	// 2) 挂一个不交叉的卖单: price=11, amount=2
	//    因为 11 > bestBid(10)，不应成交
	err = ob.AddOrder(&Order{
		ID:     "sell1",
		Side:   SELL,
		Price:  decimal.NewFromInt(11),
		Amount: decimal.NewFromInt(2),
	})
	assert.NoError(t, err)
	assert.Len(t, events, 0)

	// 3) 挂一个可交叉的卖单: price=9, amount=3
	//    期望与 buy1(price=10) 成交 => 产生 2 条 TradeUpdate 事件
	err = ob.AddOrder(&Order{
		ID:     "sell2",
		Side:   SELL,
		Price:  decimal.NewFromInt(9),
		Amount: decimal.NewFromInt(3),
	})
	assert.NoError(t, err)

	if assert.Len(t, events, 2, "there should be 2 TradeUpdate events from sink") {
		ev1 := events[0]
		ev2 := events[1]

		// 区分哪条事件对应 buy1
		if ev1.OrderID == "buy1" {
			assert.Equal(t, "sell2", ev2.OrderID)
		} else {
			assert.Equal(t, "sell2", ev1.OrderID)
			ev1, ev2 = ev2, ev1
		}

		// ev1 => buy1
		assert.True(t, ev1.TradeAmt.Equal(decimal.NewFromInt(3)))
		assert.True(t, ev1.RemainAmt.Equal(decimal.NewFromInt(2)))
		// ev2 => sell2
		assert.True(t, ev2.TradeAmt.Equal(decimal.NewFromInt(3)))
		assert.True(t, ev2.RemainAmt.Equal(decimal.Zero))
	}
}

