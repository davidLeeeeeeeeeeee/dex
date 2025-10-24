package matching

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

// TestOneHundredMillionTx 用于演示模拟 x笔订单的撮合性能测试及重启耗时测试
func TestOneHundredMillionTx(t *testing.T) {
	// -------------------
	// 1. 初始化撮合引擎
	// -------------------
	tradeCh := make(chan TradeUpdate, 1000000) // 或比订单量更大的容量
	ob := NewOrderBook(tradeCh)
	// 启动一个消费者 goroutine
	go func() {
		for range tradeCh {
			// 此处可以不做任何事，或简单log，或者统计撮合结果
			//fmt.Println("trade update:", update)
		}
	}()
	// 这里用 65000或更小数量演示，容易耗尽内存/时间
	totalOrders := 65000

	t.Logf("Starting to add %d orders...", totalOrders)

	// -------------------
	// 2. 批量生成并插入订单
	// -------------------
	startAdd := time.Now()
	for i := 0; i < totalOrders; i++ {
		orderID := fmt.Sprintf("order-%d", i)

		// 随机买卖侧
		side := BUY
		if rand.Intn(2) == 0 {
			side = SELL
		}

		// 随机在 [80, 120] 区间的价格
		price := decimal.NewFromFloat(80.0).Add(
			decimal.NewFromFloat(rand.Float64()).Mul(decimal.NewFromFloat(40.0)),
		)

		// 数量随机，且要保证>0
		amount := decimal.NewFromFloat(1.0).Add(
			decimal.NewFromFloat(rand.Float64()).Mul(decimal.NewFromFloat(10.0)),
		)

		_ = ob.AddOrder(&Order{
			ID:     orderID,
			Side:   side,
			Price:  price,
			Amount: amount,
		})
	}
	costAdd := time.Since(startAdd)
	t.Logf("Inserted %d orders, cost: %v", totalOrders, costAdd)

}

// BenchmarkPruneByMarkPrice_65000Orders 基准测试，用于评估在 65000 笔订单下 PruneByMarkPrice 的性能
func BenchmarkPruneByMarkPrice_65000Orders(b *testing.B) {
	rand.Seed(time.Now().UnixNano())
	tradeCh := make(chan TradeUpdate, 1000000) // 或比订单量更大的容量
	ob := NewOrderBook(tradeCh)
	// 启动一个消费者 goroutine
	go func() {
		for range tradeCh {
			// 此处可以不做任何事，或简单log，或者统计撮合结果
			//fmt.Println("trade update:", update)
		}
	}()
	for i := 0; i < b.N; i++ {
		// 1. 初始化 OrderBook

		// 2. 插入 65000 笔随机订单
		for j := 0; j < 65000; j++ {
			side := BUY
			if rand.Intn(2) == 1 {
				side = SELL
			}
			price := decimal.NewFromFloat(50.0).Add(
				decimal.NewFromFloat(rand.Float64()).Mul(decimal.NewFromFloat(200.0)),
			) // 随机价格范围 [50,250)
			amount := decimal.NewFromFloat(1.0).Add(
				decimal.NewFromFloat(rand.Float64()).Mul(decimal.NewFromFloat(10.0)),
			) // 随机交易量范围 [1, 11)

			// 订单 ID 用一个简易序号拼装
			o := &Order{
				ID:     fmt.Sprintf("order-%d", j),
				Side:   side,
				Price:  price,
				Amount: amount,
			}
			_ = ob.AddOrder(o)
		}

		// 3. 调用 PruneByMarkPrice 并统计耗时
		//    假设标记价格是 80.0，只保留 [40, 160] 区间内订单
		startTime := time.Now()
		ob.PruneByMarkPrice(decimal.NewFromFloat(80.0))
		elapsed := time.Since(startTime)

		b.Logf("PruneByMarkPrice took: %v", elapsed)
	}
}

// TestOrderBook_Matching 测试撮合逻辑
func TestOrderBook_Matching(t *testing.T) {
	// 建一个带缓冲的 channel，用于接收撮合事件
	tradeCh := make(chan TradeUpdate, 50) // 多给些缓冲

	// 创建 OrderBook，不需要数据库，仅测试内存撮合
	ob := NewOrderBook(tradeCh)

	// ---------------- 1) 添加两个买单 ----------------
	//    buy1: ID="buy1", 价格=10, 数量=5
	//    buy2: ID="buy2", 价格=9,  数量=4
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

	// 因为还没有卖单，所以撮合事件应该=0
	assert.Equal(t, 0, len(tradeCh), "no trades should have occurred yet")

	// ---------------- 2) 添加一个不交叉的卖单: price=11, amount=2 ----------------
	//    价格=11 > 最高买价10 => 不会成交
	err = ob.AddOrder(&Order{
		ID:     "sell1",
		Side:   SELL,
		Price:  decimal.NewFromInt(11),
		Amount: decimal.NewFromInt(2),
	})
	assert.NoError(t, err)
	assert.Equal(t, 0, len(tradeCh), "no trades, because no price crossing")

	// ---------------- 3) 来一个可交叉的卖单: price=9, amount=3 ----------------
	//    卖价9 <= 最高买价10 => 立即与 buy1 价格=10 的订单成交
	err = ob.AddOrder(&Order{
		ID:     "sell2",
		Side:   SELL,
		Price:  decimal.NewFromInt(9),
		Amount: decimal.NewFromInt(3),
	})
	assert.NoError(t, err)

	// 预期 tradeCh 会产生 2 条事件(买侧、卖侧各一条)
	if assert.Equal(t, 2, len(tradeCh), "there should be 2 TradeUpdate events") {
		ev1 := <-tradeCh
		ev2 := <-tradeCh
		// 区分哪边是 buy1
		if ev1.OrderID == "buy1" {
			assert.Equal(t, "sell2", ev2.OrderID)
		} else {
			assert.Equal(t, "buy1", ev2.OrderID)
			ev1, ev2 = ev2, ev1
		}
		// ev1 => buy1
		assert.True(t, ev1.TradePrice.Equal(decimal.NewFromInt(10)))
		assert.True(t, ev1.TradeAmt.Equal(decimal.NewFromInt(3)))
		assert.True(t, ev1.RemainAmt.Equal(decimal.NewFromInt(2))) // buy1 剩2
		assert.False(t, ev1.IsFilled)
		// ev2 => sell2
		assert.True(t, ev2.TradePrice.Equal(decimal.NewFromInt(10)))
		assert.True(t, ev2.TradeAmt.Equal(decimal.NewFromInt(3)))
		assert.True(t, ev2.RemainAmt.Equal(decimal.Zero))
		assert.True(t, ev2.IsFilled)
	}
	// 此时：buy1 剩2, buy2 剩4, sell1=price11,amt2, sell2 耗尽移除

	// ---------------- 4) 再来一个卖单：price=10, amount=5 => 与 buy1 剩2 交叉 ----------------
	err = ob.AddOrder(&Order{
		ID:     "sell3",
		Side:   SELL,
		Price:  decimal.NewFromInt(10),
		Amount: decimal.NewFromInt(5),
	})
	assert.NoError(t, err)
	// 预计撮合出2手: buy1 耗尽, sell3 剩3
	if assert.Equal(t, 2, len(tradeCh), "expect 2 events for partial fill") {
		ev1 := <-tradeCh
		ev2 := <-tradeCh
		// 区分哪边是 buy1
		if ev1.OrderID == "buy1" {
			assert.Equal(t, "sell3", ev2.OrderID)
		} else {
			assert.Equal(t, "sell3", ev1.OrderID)
			ev1, ev2 = ev2, ev1
		}
		// ev1 => buy1
		assert.True(t, ev1.TradePrice.Equal(decimal.NewFromInt(10)))
		assert.True(t, ev1.TradeAmt.Equal(decimal.NewFromInt(2)))
		assert.True(t, ev1.RemainAmt.Equal(decimal.Zero))
		assert.True(t, ev1.IsFilled) // buy1 完

		// ev2 => sell3
		assert.True(t, ev2.TradePrice.Equal(decimal.NewFromInt(10)))
		assert.True(t, ev2.TradeAmt.Equal(decimal.NewFromInt(2)))
		assert.True(t, ev2.RemainAmt.Equal(decimal.NewFromInt(3)))
		assert.False(t, ev2.IsFilled)
	}
	// 现在：buy1移除(全), buy2还4, sell1=11,2, sell3=10,3

	// ---------------- 5) 再补充“买10 撮合 卖10”的场景 ----------------
	// 新的买单: price=10, amount=5 => 与卖单 sell3(价格10,剩3) 同价 => 立刻撮合
	err = ob.AddOrder(&Order{
		ID:     "buy3",
		Side:   BUY,
		Price:  decimal.NewFromInt(10),
		Amount: decimal.NewFromInt(5),
	})
	assert.NoError(t, err)

	// 此时 sell3(价格10,剩3) 会全部成交 => remain=0, isFilled=true
	// buy3(数量5) => 撮合3 => remain=2
	// => 产生2条事件
	if assert.Equal(t, 2, len(tradeCh), "expect 2 events for same-price matching") {
		ev1 := <-tradeCh
		ev2 := <-tradeCh

		// 区分哪边是 buy3
		if ev1.OrderID == "buy3" {
			assert.Equal(t, "sell3", ev2.OrderID)
		} else {
			assert.Equal(t, "sell3", ev1.OrderID)
			ev1, ev2 = ev2, ev1
		}
		// ev1 => buy3
		assert.True(t, ev1.TradePrice.Equal(decimal.NewFromInt(10)))
		assert.True(t, ev1.TradeAmt.Equal(decimal.NewFromInt(3)))
		assert.True(t, ev1.RemainAmt.Equal(decimal.NewFromInt(2))) // buy3 还剩2
		assert.False(t, ev1.IsFilled)

		// ev2 => sell3
		assert.True(t, ev2.TradePrice.Equal(decimal.NewFromInt(10)))
		assert.True(t, ev2.TradeAmt.Equal(decimal.NewFromInt(3)))
		assert.True(t, ev2.RemainAmt.Equal(decimal.Zero))
		assert.True(t, ev2.IsFilled)
	}

	// 最终订单簿：
	//   buy2 => 价格=9, 剩余=4
	//   buy3 => 价格=10, 剩余=2
	//   sell1 => 价格=11, 剩余=2
	//   sell3 => 耗尽
	// ---------------- 测试结束 ----------------
}
