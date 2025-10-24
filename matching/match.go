package matching

import (
	"container/heap"
	"errors"
	"fmt"
	_ "strconv"

	"github.com/shopspring/decimal"
)

// PruneByMarkPrice 根据标记价格过滤订单并重建订单簿
// 只保留 [0.5*markPrice, 2.0*markPrice] 区间内的所有订单
// OrderBook 的 PruneByMarkPrice 现在只负责内存中的清理工作
func (ob *OrderBook) PruneByMarkPrice(markPrice decimal.Decimal) {
	lowerBound := markPrice.Mul(decimal.NewFromFloat(0.5)) // 0.5 * markPrice
	upperBound := markPrice.Mul(decimal.NewFromFloat(2.0)) // 2.0 * markPrice

	// 清理 buyMap
	for price, pl := range ob.buyMap {
		if price.Cmp(lowerBound) < 0 || price.Cmp(upperBound) > 0 {
			for _, order := range pl.Orders {
				ob.removeOrderIndex(order.ID)
			}
			delete(ob.buyMap, price)
		}
	}

	// 清理 sellMap
	for price, pl := range ob.sellMap {
		if price.Cmp(lowerBound) < 0 || price.Cmp(upperBound) > 0 {
			for _, order := range pl.Orders {
				ob.removeOrderIndex(order.ID)
			}
			delete(ob.sellMap, price)
		}
	}

	// 重建堆
	ob.buyHeap = &MaxPriceHeap{}
	for _, pl := range ob.buyMap {
		heap.Push(ob.buyHeap, pl)
	}

	ob.sellHeap = &MinPriceHeap{}
	for _, pl := range ob.sellMap {
		heap.Push(ob.sellHeap, pl)
	}
}

// AddOrder 新增订单并（可选）尝试局部撮合
func (ob *OrderBook) AddOrder(o *Order) error {
	// 这里用 decimal.Zero 比较
	if o.Amount.Cmp(decimal.Zero) <= 0 {
		return errors.New("order amount must be positive")
	}
	lowerBound := decimal.NewFromFloat(1e-33)
	upperBound := decimal.NewFromFloat(1e33)
	if o.Price.Cmp(lowerBound) < 0 || o.Price.Cmp(upperBound) > 0 {
		return fmt.Errorf("price out of range in OrderBook: %s", o.Price.String())
	}
	// 1. 加入/更新对应 priceMap
	var lvl *PriceLevel
	if o.Side == BUY {
		lvl = ob.buyMap[o.Price]
		if lvl == nil {
			lvl = &PriceLevel{Price: o.Price}
			ob.buyMap[o.Price] = lvl
			heap.Push(ob.buyHeap, lvl)
		}
	} else { // SELL
		lvl = ob.sellMap[o.Price]
		if lvl == nil {
			lvl = &PriceLevel{Price: o.Price}
			ob.sellMap[o.Price] = lvl
			heap.Push(ob.sellHeap, lvl)
		}
	}
	lvl.Orders = append(lvl.Orders, o)

	// 2. 加入订单ID索引
	ob.orderIndex[o.ID] = &OrderRef{Order: o, PriceLevel: lvl}

	// 3. 可选: 是否立即撮合 => 看价格是否有交叉
	if o.Side == BUY {
		// 如果买单价格 >= 当前最优卖价，则尝试撮合
		bestSellPrice := ob.getBestSellPrice()
		// bestSellPrice > 0 => bestSellPrice.Cmp(decimal.Zero) > 0
		if bestSellPrice.Cmp(decimal.Zero) > 0 && o.Price.Cmp(bestSellPrice) >= 0 {
			ob.match(BUY)
		}
	} else { // SELL
		// 如果卖单价格 <= 当前最优买价，则尝试撮合
		bestBuyPrice := ob.getBestBuyPrice()
		if bestBuyPrice.Cmp(decimal.Zero) > 0 && o.Price.Cmp(bestBuyPrice) <= 0 {
			ob.match(SELL)
		}
	}

	return nil
}

// getBestBuyOrder 获取买方堆顶的第一个订单(不一定是队列第一个，但下标0不会越界)
func (ob *OrderBook) getBestBuyOrder() *Order {
	if ob.buyHeap.Len() == 0 {
		return nil
	}
	pl := (*ob.buyHeap)[0]
	if len(pl.Orders) == 0 {
		return nil
	}
	return pl.Orders[0]
}

// getBestSellOrder 获取卖方堆顶的第一个订单
func (ob *OrderBook) getBestSellOrder() *Order {
	if ob.sellHeap.Len() == 0 {
		return nil
	}
	pl := (*ob.sellHeap)[0]
	if len(pl.Orders) == 0 {
		return nil
	}
	return pl.Orders[0]
}

// match 与对手方最优价格进行撮合；
// side 表示当前新来的订单方向 (BUY => 这批新买单；SELL => 这批新卖单)。
func (ob *OrderBook) match(side OrderSide) {
	for {
		var changed bool

		if side == BUY {
			// 1) 若买堆空 => 无可撮合
			if ob.buyHeap.Len() == 0 {
				break
			}
			bestBuy := (*ob.buyHeap)[0]
			if len(bestBuy.Orders) == 0 {
				// 该价位上订单都被撮合光了，弹出并继续
				heap.Pop(ob.buyHeap)
				delete(ob.buyMap, bestBuy.Price)
				continue
			}
			// 2) 检查是否跟最优卖价交叉
			bestSellPrice := ob.getBestSellPrice()
			if bestSellPrice.IsZero() {
				// 卖堆没有订单，无法成交
				break
			}
			// 如果买价 < 卖价 => 不交叉 => 终止
			if bestBuy.Price.Cmp(bestSellPrice) < 0 {
				break
			}
			// 3) 交叉 => 执行撮合
			changed = ob.executeTrade(bestBuy, BUY)

		} else {
			// side == SELL
			if ob.sellHeap.Len() == 0 {
				break
			}
			bestSell := (*ob.sellHeap)[0]
			if len(bestSell.Orders) == 0 {
				heap.Pop(ob.sellHeap)
				delete(ob.sellMap, bestSell.Price)
				continue
			}
			bestBuyPrice := ob.getBestBuyPrice()
			if bestBuyPrice.IsZero() {
				break
			}
			// 如果卖价 > 买价 => 不交叉
			if bestSell.Price.Cmp(bestBuyPrice) > 0 {
				break
			}
			changed = ob.executeTrade(bestSell, SELL)
		}

		// 如果本轮撮合没有实际成交，直接退出避免死循环
		if !changed {
			break
		}
	}
}

// 这里是撮合核心 executeTrade
func (ob *OrderBook) executeTrade(pl *PriceLevel, side OrderSide) bool {
	changed := false
	for i := 0; i < len(pl.Orders); {
		o := pl.Orders[i]
		if o.Amount.Cmp(decimal.Zero) <= 0 {
			// 已耗尽则从切片移除
			pl.Orders = append(pl.Orders[:i], pl.Orders[i+1:]...)
			ob.removeOrderIndex(o.ID)
			continue
		}

		// 找到对手单(若 side==BUY，就找最优卖；若 side==SELL，就找最优买)
		var opp *Order
		if side == BUY {
			opp = ob.getBestSellOrder()
		} else {
			opp = ob.getBestBuyOrder()
		}
		if opp == nil {
			// 没有对手单，撮合结束
			break
		}

		// 判断是否有交叉价格，若不交叉也break ...
		// ...

		tradeAmt := minDecimal(o.Amount, opp.Amount)
		if tradeAmt.Cmp(decimal.Zero) <= 0 {
			break
		}
		changed = true

		// 假设撮合价取对手单价格
		actualPrice := opp.Price

		// 减少各自剩余量
		o.Amount = o.Amount.Sub(tradeAmt)
		opp.Amount = opp.Amount.Sub(tradeAmt)

		// ★ 在这里发送撮合事件 => Manager 后台去DB更新
		ob.tradeCh <- TradeUpdate{
			OrderID:    o.ID,
			TradeAmt:   tradeAmt,
			TradePrice: actualPrice,
			RemainAmt:  o.Amount,
			IsFilled:   o.Amount.Cmp(decimal.Zero) == 0,
		}
		ob.tradeCh <- TradeUpdate{
			OrderID:    opp.ID,
			TradeAmt:   tradeAmt,
			TradePrice: actualPrice,
			RemainAmt:  opp.Amount,
			IsFilled:   opp.Amount.Cmp(decimal.Zero) == 0,
		}

		// 如果对手单耗尽 => remove
		if opp.Amount.Cmp(decimal.Zero) <= 0 {
			ob.removeFromPriceLevel(opp)
		}

		// 如果当前订单耗尽 => remove
		if o.Amount.Cmp(decimal.Zero) <= 0 {
			pl.Orders = append(pl.Orders[:i], pl.Orders[i+1:]...)
			ob.removeOrderIndex(o.ID)
			continue
		}
		i++ // 只有当 o 还剩余时才 i++ 前进
	}
	return changed
}

// removeFromPriceLevel 当对手订单撮合完后，需从对应priceLevel删除
func (ob *OrderBook) removeFromPriceLevel(o *Order) {
	ref, ok := ob.orderIndex[o.ID]
	if !ok {
		return
	}
	pl := ref.PriceLevel

	// 从 pl.Orders 中删除
	newOrders := pl.Orders[:0]
	for _, od := range pl.Orders {
		if od.ID != o.ID {
			newOrders = append(newOrders, od)
		}
	}
	pl.Orders = newOrders

	ob.removeOrderIndex(o.ID)
}

// removeOrderIndex 从orderIndex移除
func (ob *OrderBook) removeOrderIndex(orderID string) {
	delete(ob.orderIndex, orderID)
}

// CancelOrder 通过订单ID删除订单
func (ob *OrderBook) CancelOrder(orderID string) error {
	ref, ok := ob.orderIndex[orderID]
	if !ok {
		return errors.New("order not found")
	}
	pl := ref.PriceLevel

	// 从pl.Orders中删除
	newOrders := pl.Orders[:0]
	for _, od := range pl.Orders {
		if od.ID != orderID {
			newOrders = append(newOrders, od)
		}
	}
	pl.Orders = newOrders
	ob.removeOrderIndex(orderID)
	return nil
}

// getBestBuyPrice 获取当前最高买价
func (ob *OrderBook) getBestBuyPrice() decimal.Decimal {
	if ob.buyHeap.Len() == 0 {
		return decimal.Zero
	}
	return (*ob.buyHeap)[0].Price
}

// getBestSellPrice 获取当前最低卖价
func (ob *OrderBook) getBestSellPrice() decimal.Decimal {
	if ob.sellHeap.Len() == 0 {
		// 表示无卖单时
		// 这里可以返回一个极大的值，或者就返回0以做特殊判断
		// 选择返回0（外部判断 if Cmp(decimal.Zero)>0 表示有卖单）
		return decimal.Zero
	}
	return (*ob.sellHeap)[0].Price
}

// minDecimal 替代原有的min函数，用于 decimal
func minDecimal(a, b decimal.Decimal) decimal.Decimal {
	if a.Cmp(b) < 0 {
		return a
	}
	return b
}

// BuyMap 返回买单 price->PriceLevel 的映射
func (ob *OrderBook) BuyMap() map[decimal.Decimal]*PriceLevel {
	return ob.buyMap
}
func (ob *OrderBook) SellMap() map[decimal.Decimal]*PriceLevel {
	return ob.sellMap
}

// 每轮按如下步骤调用，即可保证内存可控
// 1、调用重建价格堆，市场价的0.5~2.0
// 2、DB读取0.5~2.0到堆
// 3、内存读取0.5~2.0到推
