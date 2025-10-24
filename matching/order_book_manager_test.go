package matching_test

import (
	"fmt"
	"github.com/stretchr/testify/require"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"

	"dex/db"
	pb "dex/db" // pb = proto定义的别名
	"dex/matching"
)

// TestOrderBookManager_Basic 测试 OrderBookManager 的基本功能
// 是**“下单数据的存储 + 在给定价格区间内加载订单 + 剪枝重建”**的一个综合自测
func TestOrderBookManager_Basic(t *testing.T) {
	// ----------- 1. 初始化一个临时数据库 -----------
	tmpDir := filepath.Join(os.TempDir(), "test_db_orderbook")
	// 在测试结束时清理
	defer os.RemoveAll(tmpDir)

	dbMgr, err := db.GetInstance(tmpDir)
	if err != nil {
		t.Fatalf("failed to open DB: %v", err)
	}
	defer dbMgr.Close()
	dbMgr.InitWriteQueue(10000, 200*time.Millisecond)
	// ----------- 2. 往 DB 里写入一些模拟订单 -----------
	// 假设我们写 4 条订单：价格分别是 1、2、5、10
	// 对交易对 (BaseToken=USDT, QuoteToken=BTC)，都先构造 OrderTx
	testOrders := []struct {
		price  string
		amount string
		txId   string
	}{
		{"1", "100", "txid_1"},
		{"2", "80", "txid_2"},
		{"5", "50", "txid_3"},
		{"10", "20", "txid_4"},
	}

	for _, od := range testOrders {
		orderTx := &pb.OrderTx{
			Base: &pb.BaseMessage{
				TxId: od.txId,
			},
			BaseToken:  "USDT",
			QuoteToken: "BTC",
			Op:         pb.OrderOp_ADD, // 简化都设为ADD => BUY
			Amount:     od.amount,
			Price:      od.price,
		}
		// 利用你的 SaveOrderTx 逻辑写入 DB (它同时会写二级索引 key)
		err := db.SaveOrderTx(dbMgr, orderTx)
		assert.NoError(t, err)
	} // 等 300ms，等待批量写入
	time.Sleep(300 * time.Millisecond)

	// ----------- 3. 创建 OrderBookManager -----------
	obm := matching.NewOrderBookManager(dbMgr)

	// ----------- 4. 测试 LoadOrdersInRange -----------
	// 我们只加载 [2, 6] 区间 => 应该只加载到 price=2、price=5
	lower := decimal.NewFromInt(2)
	upper := decimal.NewFromInt(6)
	err = obm.LoadOrdersInRange("USDT", "BTC", lower, upper)
	assert.NoError(t, err)

	// 验证：内存中的订单簿，应该有 2 条订单( price=2, price=5 )
	ob := obm.GetOrderBook("USDT", "BTC")
	buyOrdersCount := 0
	for _, pl := range ob.BuyMap() { // BuyMap()是你可以自己给OrderBook加个Getter
		buyOrdersCount += len(pl.Orders)
	}
	assert.Equal(t, 2, buyOrdersCount, "should only load orders within [2,6] range")

	// ----------- 5. 测试 PruneAndRebuild -----------
	// 假设 markPrice=2 => prune 后只保留 [1.0, 4.0] (0.5~2.0倍)
	// 但是 1 已经不在内存里； 2,5 在内存，但 5 现在超出(4.0)要被 prune 掉
	// prune完只剩 price=2
	// 然后它会从DB加载 [1.0,4.0] => 又会把 price=1 和 price=2 加回来
	pair := matching.TradingPair{BaseToken: "USDT", QuoteToken: "BTC"}
	markPrice := decimal.NewFromInt(2)
	err = obm.PruneAndRebuild(pair, markPrice)
	assert.NoError(t, err)
	fmt.Println("After prune, buyMap has:")
	for p := range ob.BuyMap() {
		fmt.Println("  ", p.String())
	}
	fmt.Println("After prune, sellMap has:")
	for p := range ob.SellMap() {
		fmt.Println("  ", p.String())
	}
	// 现在在内存中的价格应当是 1 和 2
	ob2 := obm.GetOrderBook("USDT", "BTC")
	pricesFound := make(map[string]struct{})
	for price, pl := range ob2.BuyMap() {
		if len(pl.Orders) > 0 {
			pricesFound[price.String()] = struct{}{}
		}
	}
	// 排序一下再断言
	// or just check using a map
	assert.Contains(t, pricesFound, "1", "price=1 should be in range after rebuild")
	assert.Contains(t, pricesFound, "2", "price=2 should be in range after rebuild")
	require.Equal(t, 2, len(pricesFound), "should only have 1 and 2")
}

func TestOrderBookManager_MassiveRebuild_WithMatching(t *testing.T) {

	dbMgr, err := db.GetInstance("C:\\Users\\Administrator\\GolandProjects\\awesomeProject1\\data")
	require.NoError(t, err, "failed to open DB")
	dbMgr.InitWriteQueue(10000, 200*time.Millisecond)
	// 2. 往 DB 写入大量模拟订单。例如写 65000 条
	const totalOrders = 65000
	baseToken := "USDT"
	quoteToken := "BTC"

	t.Logf("Inserting %d orders into DB...", totalOrders)
	insertStart := time.Now()

	// 为了让随机数每次都不一样，你也可以加:
	// rand.Seed(time.Now().UnixNano())
	for i := 1; i <= totalOrders; i++ {
		// 价格随机 [1..1000]
		priceVal := rand.Intn(1000) + 1
		price := decimal.NewFromInt(int64(priceVal))

		// 固定 amount=100
		amount := decimal.NewFromInt(100)

		// 随机决定订单是买单(ADD)还是卖单(REMOVE)
		op := pb.OrderOp_ADD // BUY
		if rand.Intn(2) == 0 {
			op = pb.OrderOp_REMOVE // SELL
		}

		txID := fmt.Sprintf("bulk_txid_%d", i)
		orderTx := &pb.OrderTx{
			Base: &pb.BaseMessage{
				TxId: txID,
			},
			BaseToken:  baseToken,
			QuoteToken: quoteToken,
			Op:         op,
			IsFilled:   false,
			Amount:     amount.String(),
			Price:      price.String(),
		}
		err = db.SaveOrderTx(dbMgr, orderTx)
		require.NoError(t, err, "failed to save orderTx %d", i)
	}
	insertElapsed := time.Since(insertStart)
	t.Logf("Finished inserting %d orders. Took: %v", totalOrders, insertElapsed)

	// 3. 创建 OrderBookManager
	obm := matching.NewOrderBookManager(dbMgr)

	// 4. 执行一次 PruneAndRebuild，并测量耗时
	//   让 markPrice=500 => 保留 [250, 1000] 区间，其余都 prune。
	//   这样区间内有一部分随机的BUY(ADD)和SELL(REMOVE)，就会产生实质撮合。
	pair := matching.TradingPair{BaseToken: baseToken, QuoteToken: quoteToken}
	markPrice := decimal.NewFromInt(500)

	t.Logf("Starting PruneAndRebuild (with matching) at markPrice=%s ...", markPrice)
	rebuildStart := time.Now()
	err = obm.PruneAndRebuild(pair, markPrice)
	rebuildElapsed := time.Since(rebuildStart)
	require.NoError(t, err, "PruneAndRebuild error")

	t.Logf("[Result] PruneAndRebuild (with matching) took: %v", rebuildElapsed)

	// 5. 检查内存中的价格层/订单数
	ob := obm.GetOrderBook(baseToken, quoteToken)
	buyMapLen := len(ob.BuyMap())
	sellMapLen := len(ob.SellMap())
	t.Logf("After rebuild, buyMap has %d price levels, sellMap has %d price levels",
		buyMapLen, sellMapLen)
	require.True(t, buyMapLen > 0 || sellMapLen > 0, "should have some prices left after prune")

	// 6. 性能断言 (如希望在2s内完成)
	maxAllowed := 10 * time.Second
	require.Truef(t, rebuildElapsed < maxAllowed,
		"PruneAndRebuild took too long: %v, should be < %v",
		rebuildElapsed, maxAllowed,
	)

	obm.Stop()
	dbMgr.Close()

}
