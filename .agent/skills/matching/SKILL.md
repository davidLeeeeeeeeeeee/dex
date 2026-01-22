---
name: Matching Engine
description: Order book matching engine with price-time priority, heap-based order management, and trade event generation.
triggers:
  - 撮合引擎
  - 订单匹配
  - 买卖单
  - 价格优先
  - order matching
  - price level
---

# Matching (撮合引擎) 模块指南

实现订单簿撮合逻辑，采用价格-时间优先级算法。

## 目录结构

```
matching/
├── match.go           # ⭐ 核心撮合逻辑
├── types.go           # Order, OrderBook, PriceLevel 等类型
├── price_heap.go      # 价格堆实现（买卖双向）
├── matching_test.go   # 单元测试
├── boundary_test.go   # 边界条件测试
└── trade_sink_test.go # 成交事件测试
```

## 核心数据结构

```go
type Order struct {
    ID       string
    Side     OrderSide      // BUY / SELL
    Price    decimal.Decimal
    Amount   decimal.Decimal
    Time     time.Time
}

type OrderBook struct {
    buyHeap  *PriceHeap    // 买单堆（价格降序）
    sellHeap *PriceHeap    // 卖单堆（价格升序）
    orders   map[string]*Order
}

type TradeUpdate struct {
    OrderID    string
    TradeAmt   decimal.Decimal
    TradePrice decimal.Decimal
    RemainAmt  decimal.Decimal
    IsFilled   bool
}
```

## 撮合规则

1. **价格优先**: 买单价高者优先，卖单价低者优先
2. **时间优先**: 同价格时，先到者优先
3. **成交价格**: 使用挂单价格（Maker Price）
4. **部分成交**: 支持订单部分成交

## 关键流程

```
NewOrder 进入
    ↓
检查对手盘是否有可撮合订单
    ↓ 有
执行 executeTrade()
    ↓
生成 TradeUpdate 事件（成对）
    ↓
更新/移除已成交订单
    ↓
剩余部分挂入订单簿
```

## 与 VM 的集成

VM 的 `order_handler.go` 调用 matching：
```go
// 创建临时 OrderBook
ob := matching.NewOrderBook()

// 加载现有订单
for _, order := range existingOrders {
    ob.AddOrder(order)
}

// 执行新订单撮合
tradeEvents := ob.Match(newOrder)

// 根据 tradeEvents 生成 WriteOp
```

## 开发规范

1. **纯计算模块**: 不依赖数据库，不产生副作用
2. **Decimal 精度**: 使用 `shopspring/decimal`，避免浮点误差
3. **事件成对**: 每笔成交产生两个 TradeUpdate（maker + taker）

## 测试

```bash
go test ./matching/... -v
go test ./matching/ -run TestBoundary -v
```

