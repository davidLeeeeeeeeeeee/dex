package matching

import (
	"dex/db"
	"dex/logs"
	"dex/utils"
	"fmt"
	"strings"
	"sync"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
)

// TradingPair 结构体用于区分不同交易对
type TradingPair struct {
	BaseToken  string
	QuoteToken string
}

// OrderBookManager 管理多个交易对的订单簿
type OrderBookManager struct {
	dbMgr      *db.Manager
	orderBooks map[TradingPair]*OrderBook
	tradeCh    chan TradeUpdate // 用于接收撮合后的事件(部分/全部成交)
	stopCh     chan struct{}
	mu         sync.RWMutex
	wg         sync.WaitGroup
}

// NewOrderBookManager 创建订单簿管理器，并启动后台协程监听撮合事件
func NewOrderBookManager(dbMgr *db.Manager) *OrderBookManager {
	obm := &OrderBookManager{
		dbMgr:      dbMgr,
		orderBooks: make(map[TradingPair]*OrderBook),
		tradeCh:    make(chan TradeUpdate, 65000), // 视需求可设置更大或更小缓冲
		stopCh:     make(chan struct{}),
	}
	obm.wg.Add(1)
	// 启动后台 goroutine 处理撮合事件（无论部分还是全部）
	go obm.handleTradeUpdates()

	return obm
}

// Stop 停止后台事件循环（可在进程退出时调用）
func (obm *OrderBookManager) Stop() {
	close(obm.stopCh)
	// 2. 等后台goroutine全部退出
	obm.wg.Wait()
}

// GetOrderBook 获取指定交易对的订单簿；若不存在则创建
func (obm *OrderBookManager) GetOrderBook(baseToken, quoteToken string) *OrderBook {
	obm.mu.Lock()
	defer obm.mu.Unlock()

	pair := TradingPair{BaseToken: baseToken, QuoteToken: quoteToken}
	ob, exists := obm.orderBooks[pair]
	if !exists {
		// 新建 OrderBook，注意要把 tradeCh 传进去
		ob = NewOrderBook(obm.tradeCh)
		obm.orderBooks[pair] = ob
	}
	return ob
}

// handleTradeUpdates 循环从 tradeCh 读事件并更新数据库(或做其他操作)
func (obm *OrderBookManager) handleTradeUpdates() {
	defer obm.wg.Done()
	for {
		select {
		case <-obm.stopCh:
			// 收到停止信号，退出循环
			return
		case ev := <-obm.tradeCh:
			// 这里你可以更新数据库
			// 调用一个 DB 函数，比如 db.UpdateOrderTxInDB 来增加撮合量
			err := obm.dbMgr.UpdateOrderTxInDB(ev.OrderID, ev.TradeAmt, ev.TradePrice)
			if err != nil {
				logs.Verbose("[OrderBookManager] failed to update DB for order %s: %v", ev.OrderID, err)
			}

			// 可选：如果 IsFilled == true，可以额外做删除订单之类操作
			// ...
			//log.Printf("[handleTradeUpdates] order=%s traded=%s remain=%s price=%s filled=%v",
			//	ev.OrderID, ev.TradeAmt, ev.RemainAmt, ev.TradePrice, ev.IsFilled)
		}
	}
}

// LoadOrdersInRange 在 OrderBookManager 层面处理数据库操作
func (obm *OrderBookManager) LoadOrdersInRange(
	baseToken, quoteToken string,
	lowerPrice, upperPrice decimal.Decimal,
) error {
	// 1) 用 priceToKey128 得到两个 67 位字符串
	lowerKeyStr, err := db.PriceToKey128(lowerPrice.String())
	if err != nil {
		return fmt.Errorf("invalid lowerPrice: %v", err)
	}
	upperKeyStr, err := db.PriceToKey128(upperPrice.String())
	if err != nil {
		return fmt.Errorf("invalid upperPrice: %v", err)
	}

	// 2) 拼接成 “pair:base_quote|is_filled:false|price:0000...xxx”
	pair := utils.GeneratePairKey(baseToken, quoteToken)
	prefix := fmt.Sprintf("pair:%s|is_filled:false|price:", pair)
	lowestKey := prefix + lowerKeyStr
	highestKey := prefix + upperKeyStr

	ob := obm.GetOrderBook(baseToken, quoteToken)

	// 3) 打开 DB 的只读事务
	return obm.dbMgr.View(func(txn *db.TransactionView) error {
		it := txn.NewIterator()
		defer it.Close()

		for it.Seek([]byte(lowestKey)); it.Valid(); it.Next() {
			item := it.Item()
			keyBytes := item.Key()
			k := string(keyBytes)

			// 如果超出 highestKey，就跳出
			if k > highestKey {
				break
			}

			// 1) 先解析 key，拿到 order_id
			//    key 格式: "pair:base_quote|price:000...67|order_id:xxxxx"
			//    可以先找 "|order_id:" 再取后面部分
			parts := strings.Split(k, "|order_id:")
			if len(parts) != 2 {
				// 格式不匹配，跳过
				continue
			}
			orderID := parts[1]

			// 2) 读取二级索引的值，这里其实只是个 OrderPriceIndex
			val, err := item.ValueCopy(nil)
			if err != nil {
				continue
			}
			var idx db.OrderPriceIndex
			if err := proto.Unmarshal(val, &idx); err != nil {
				// 不是一个有效的 OrderPriceIndex 结构，跳过
				continue
			}

			// 3) 再去拿完整订单
			orderTx, err := db.GetOrderTx(obm.dbMgr, orderID)
			if err != nil || orderTx == nil {
				continue
			}

			// 4) 把 OrderTx 转成撮合层 Order
			order, err := convertOrderTxToOrder(orderTx)
			if err != nil {
				continue
			}

			// 5) 加入内存
			_ = ob.AddOrder(order)
		}
		return nil
	})
}

func (obm *OrderBookManager) PruneAndRebuild(pair TradingPair, markPrice decimal.Decimal) error {
	ob := obm.GetOrderBook(pair.BaseToken, pair.QuoteToken)

	// 先在内存里 prune
	ob.PruneByMarkPrice(markPrice)

	// 再从数据库加载 [0.5 * markPrice, 2.0 * markPrice] 区间订单
	lowerBound := markPrice.Mul(decimal.NewFromFloat(0.5))
	upperBound := markPrice.Mul(decimal.NewFromFloat(2.0))
	return obm.LoadOrdersInRange(pair.BaseToken, pair.QuoteToken, lowerBound, upperBound)
}
