package matching_test

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"dex/db"
	"dex/matching"
)

// 说明：
// 1) 适配新的 DB 管理器：db.NewManager + InitWriteQueue + ForceFlush + Close
// 2) 适配新的订单写入：dbMgr.SaveOrderTx(...)（方法而非包级函数）
// 3) 仍然测试：区间加载 + 剪枝重建（先 prune 后 DB 回灌）
// 4) 使用 t.TempDir()，测试目录自动清理
func TestOrderBookManager_Basic(t *testing.T) {
	// 1. 初始化临时 DB
	tmpDir := t.TempDir()

	dbMgr, err := db.NewManager(tmpDir)
	require.NoError(t, err, "failed to open DB")
	defer dbMgr.Close()

	// 启动写队列
	dbMgr.InitWriteQueue(10000, 200*time.Millisecond)

	// 2. 写入4条订单（BUY），价格: 1,2,5,10
	type od = struct {
		price, amount, txID string
	}
	testOrders := []od{
		{"1", "100", "txid_1"},
		{"2", "80", "txid_2"},
		{"5", "50", "txid_3"},
		{"10", "20", "txid_4"},
	}
	for _, o := range testOrders {
		orderTx := &db.OrderTx{
			Base: &db.BaseMessage{TxId: o.txID},
			// 注意：BaseToken/QuoteToken 与索引键生成一致
			BaseToken:  "USDT",
			QuoteToken: "BTC",
			Op:         db.OrderOp_ADD, // BUY
			IsFilled:   false,
			Amount:     o.amount,
			Price:      o.price,
		}
		require.NoError(t, dbMgr.SaveOrderTx(orderTx))
	}
	// 刷盘，确保下面的只读遍历能读到
	dbMgr.ForceFlush()
	time.Sleep(50 * time.Millisecond)

	// 3. 创建 OrderBookManager
	obm := matching.NewOrderBookManager(dbMgr)
	defer obm.Stop()

	// 4. 仅加载 [2,6] 区间（应命中 2、5）
	lower := decimal.NewFromInt(2)
	upper := decimal.NewFromInt(6)
	err = obm.LoadOrdersInRange("USDT", "BTC", lower, upper)
	require.NoError(t, err)

	ob := obm.GetOrderBook("USDT", "BTC")
	buyOrdersCount := 0
	for _, pl := range ob.BuyMap() {
		buyOrdersCount += len(pl.Orders)
	}
	assert.Equal(t, 2, buyOrdersCount, "should only load orders within [2,6] range")

	// 5. PruneAndRebuild：markPrice=2 → 区间[1.0,4.0]；先 prune 再从DB回灌
	// 期望最终内存里价格只剩 1 和 2
	pair := matching.TradingPair{BaseToken: "USDT", QuoteToken: "BTC"}
	markPrice := decimal.NewFromInt(2)
	err = obm.PruneAndRebuild(pair, markPrice)
	require.NoError(t, err)

	ob2 := obm.GetOrderBook("USDT", "BTC")
	pricesFound := map[string]struct{}{}
	for price, pl := range ob2.BuyMap() {
		if len(pl.Orders) > 0 {
			pricesFound[price.String()] = struct{}{}
		}
	}
	assert.Contains(t, pricesFound, "1", "price=1 should be in range after rebuild")
	assert.Contains(t, pricesFound, "2", "price=2 should be in range after rebuild")
	require.Equal(t, 2, len(pricesFound), "should only have 1 and 2")
}

// 大量订单 + 重建 + 撮合耗时测试。
// 在 go test -short 下默认跳过，避免 CI/本地快速跑超时。
func TestOrderBookManager_MassiveRebuild_WithMatching(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping massive test in -short mode")
	}

	tmpDir := t.TempDir()
	dbMgr, err := db.NewManager(tmpDir)
	require.NoError(t, err, "failed to open DB")
	dbMgr.InitWriteQueue(10000, 200*time.Millisecond)
	defer dbMgr.Close()

	const totalOrders = 65000
	const baseToken = "USDT"
	const quoteToken = "BTC"

	rand.Seed(time.Now().UnixNano())

	t.Logf("Inserting %d orders into DB...", totalOrders)
	insertStart := time.Now()
	for i := 1; i <= totalOrders; i++ {
		priceVal := rand.Intn(1000) + 1
		price := decimal.NewFromInt(int64(priceVal))

		amount := decimal.NewFromInt(100)
		op := db.OrderOp_ADD // BUY
		if rand.Intn(2) == 0 {
			op = db.OrderOp_REMOVE // SELL
		}

		txID := fmt.Sprintf("bulk_txid_%d", i)
		orderTx := &db.OrderTx{
			Base:       &db.BaseMessage{TxId: txID},
			BaseToken:  baseToken,
			QuoteToken: quoteToken,
			Op:         op,
			IsFilled:   false,
			Amount:     amount.String(),
			Price:      price.String(),
		}
		require.NoError(t, dbMgr.SaveOrderTx(orderTx), "failed to save orderTx %d", i)
	}
	dbMgr.ForceFlush()
	time.Sleep(100 * time.Millisecond)
	insertElapsed := time.Since(insertStart)
	t.Logf("Finished inserting %d orders. Took: %v", totalOrders, insertElapsed)

	obm := matching.NewOrderBookManager(dbMgr)
	defer obm.Stop()

	pair := matching.TradingPair{BaseToken: baseToken, QuoteToken: quoteToken}
	markPrice := decimal.NewFromInt(500)

	t.Logf("Starting PruneAndRebuild (with matching) at markPrice=%s ...", markPrice)
	rebuildStart := time.Now()
	err = obm.PruneAndRebuild(pair, markPrice)
	require.NoError(t, err, "PruneAndRebuild error")
	rebuildElapsed := time.Since(rebuildStart)
	t.Logf("[Result] PruneAndRebuild (with matching) took: %v", rebuildElapsed)

	ob := obm.GetOrderBook(baseToken, quoteToken)
	buyMapLen := len(ob.BuyMap())
	sellMapLen := len(ob.SellMap())
	t.Logf("After rebuild, buyMap has %d price levels, sellMap has %d price levels", buyMapLen, sellMapLen)
	require.True(t, buyMapLen > 0 || sellMapLen > 0, "should have some prices left after prune")

	maxAllowed := 10 * time.Second
	require.Truef(t, rebuildElapsed < maxAllowed, "PruneAndRebuild took too long: %v, should be < %v", rebuildElapsed, maxAllowed)
}
