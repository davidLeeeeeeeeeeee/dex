package matching

import (
	"github.com/shopspring/decimal"
)

// --------------------- 数据结构定义 ---------------------

// OrderSide 表示买或卖
type OrderSide int

const (
	BUY OrderSide = iota
	SELL
)

// Order 表示一条订单
type Order struct {
	ID     string          // 订单ID
	Side   OrderSide       // 买 or 卖
	Price  decimal.Decimal // 价格
	Amount decimal.Decimal // 剩余数量
}

// PriceLevel 代表某一价格层的订单列表
type PriceLevel struct {
	Price  decimal.Decimal
	Orders []*Order // 在一个简化实现里用切片即可；可改用双向链表等
}

// OrderRef 用来快速定位订单和对应的priceLevel
type OrderRef struct {
	Order      *Order
	PriceLevel *PriceLevel
}

// --------------------- 堆(买方: MaxPriceHeap) ---------------------

type MaxPriceHeap []*PriceLevel

func (h MaxPriceHeap) Len() int { return len(h) }
func (h MaxPriceHeap) Less(i, j int) bool {
	// 想要最大堆，就让大的在前
	return h[i].Price.Cmp(h[j].Price) > 0
}
func (h MaxPriceHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}
func (h *MaxPriceHeap) Push(x interface{}) {
	*h = append(*h, x.(*PriceLevel))
}
func (h *MaxPriceHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[0 : n-1]
	return item
}

// --------------------- 堆(卖方: MinPriceHeap) ---------------------

type MinPriceHeap []*PriceLevel

func (h MinPriceHeap) Len() int { return len(h) }
func (h MinPriceHeap) Less(i, j int) bool {
	// 想要最小堆，就让小的在前
	return h[i].Price.Cmp(h[j].Price) < 0
}
func (h MinPriceHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}
func (h *MinPriceHeap) Push(x interface{}) {
	*h = append(*h, x.(*PriceLevel))
}
func (h *MinPriceHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[0 : n-1]
	return item
}
