package vm

import (
	"dex/db"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestLightNode_RebuildOrderIndexFromStateDB
// 集成测试：验证轻节点从 StateDB 同步后能够重建订单索引
//
// 测试场景：
// 1. 全节点：创建订单数据和索引，同步到 StateDB
// 2. 轻节点：只从 StateDB 同步订单数据（不包含索引）
// 3. 轻节点：从订单数据重建价格索引
// 4. 验证：重建的索引与全节点一致
func TestLightNode_RebuildOrderIndexFromStateDB(t *testing.T) {
	t.Log("🚀 ========== 轻节点索引重建集成测试 ==========")

	// ========== 第一步：全节点创建数据 ==========
	t.Log("📦 Step 1: Full node creates orders and indexes")

	fullNodeDir := t.TempDir()
	fullNodeDB, err := db.NewManager(fullNodeDir, logs.NewNodeLogger("test", 0))
	require.NoError(t, err, "Failed to create full node DB")
	defer fullNodeDB.Close()

	fullNodeDB.InitWriteQueue(10000, 200*time.Millisecond)

	// 初始化 VM
	registry := NewHandlerRegistry()
	require.NoError(t, RegisterDefaultHandlers(registry))
	cache := NewSpecExecLRU(100)
	executor := NewExecutor(fullNodeDB, registry, cache)

	// 创建测试账户
	aliceAddr := "alice_fullnode"
	bobAddr := "bob_fullnode"

	// 初始化账户余额
	createTestAccount(t, fullNodeDB, aliceAddr, map[string]string{
		"BTC":  "10",
		"USDT": "100000",
	})
	createTestAccount(t, fullNodeDB, bobAddr, map[string]string{
		"ETH":  "50",
		"USDT": "200000",
	})

	require.NoError(t, fullNodeDB.ForceFlush())
	time.Sleep(300 * time.Millisecond)

	// 创建订单交易
	testOrders := []struct {
		from       string
		baseToken  string
		quoteToken string
		price      string
		amount     string
		side       pb.OrderSide
	}{
		{aliceAddr, "BTC", "USDT", "50000", "1", pb.OrderSide_SELL},
		{aliceAddr, "BTC", "USDT", "51000", "1", pb.OrderSide_SELL},
		{aliceAddr, "BTC", "USDT", "49000", "2", pb.OrderSide_SELL},
		{bobAddr, "ETH", "USDT", "3000", "5", pb.OrderSide_SELL},
		{bobAddr, "ETH", "USDT", "3100", "3", pb.OrderSide_SELL},
	}

	orderIDs := make([]string, 0, len(testOrders))
	for i, tc := range testOrders {
		orderID := tc.from + "_order_" + string(rune('0'+i))
		orderIDs = append(orderIDs, orderID)

		orderTx := &pb.AnyTx{
			Content: &pb.AnyTx_OrderTx{
				OrderTx: &pb.OrderTx{
					Base: &pb.BaseMessage{
						TxId:        orderID,
						FromAddress: tc.from,
						Status:      pb.Status_PENDING,
					},
					BaseToken:  tc.baseToken,
					QuoteToken: tc.quoteToken,
					Op:         pb.OrderOp_ADD,
					Side:       tc.side,
					Price:      tc.price,
					Amount:     tc.amount,
					// 注意: FilledBase, FilledQuote, IsFilled 已移至 OrderState
				},
			},
		}

		block := &pb.Block{
			BlockHash: "block_" + string(rune('0'+i)),
			Header: &pb.BlockHeader{
				PrevBlockHash: "prev_" + string(rune('0'+i)),
				Height:        uint64(i + 1),
			},
			Body: []*pb.AnyTx{orderTx},
		}

		// 执行区块
		result, err := executor.PreExecuteBlock(block)
		require.NoError(t, err)
		require.True(t, result.Valid, "Block should be valid")

		err = executor.CommitFinalizedBlock(block)
		require.NoError(t, err)
	}

	require.NoError(t, fullNodeDB.ForceFlush())
	time.Sleep(300 * time.Millisecond)

	t.Logf("✅ Full node created %d orders", len(orderIDs))

	// 验证全节点的订单数据和索引
	fullNodeOrderCount := countDBKeysWithPrefix(t, fullNodeDB, "v1_order_")
	// Key include side: pair:%s|side:%d|is_filled:%t|
	btcPrefix := fmt.Sprintf("v1_pair:BTC_USDT|side:%d|is_filled:false|", pb.OrderSide_SELL)
	ethPrefix := fmt.Sprintf("v1_pair:ETH_USDT|side:%d|is_filled:false|", pb.OrderSide_SELL)
	fullNodeBTCIndexCount := countDBKeysWithPrefix(t, fullNodeDB, btcPrefix)
	fullNodeETHIndexCount := countDBKeysWithPrefix(t, fullNodeDB, ethPrefix)

	t.Logf("📊 Full node stats:")
	t.Logf("  - Orders: %d", fullNodeOrderCount)
	t.Logf("  - BTC indexes: %d", fullNodeBTCIndexCount)
	t.Logf("  - ETH indexes: %d", fullNodeETHIndexCount)

	assert.Equal(t, 5, fullNodeOrderCount, "Full node should have 5 orders")
	assert.Equal(t, 3, fullNodeBTCIndexCount, "Full node should have 3 BTC indexes")
	assert.Equal(t, 2, fullNodeETHIndexCount, "Full node should have 2 ETH indexes")

	// ========== 第二步：模拟轻节点从 StateDB 同步 ==========
	t.Log("📦 Step 2: Light node syncs from StateDB")

	lightNodeDir := t.TempDir()
	lightNodeDB, err := db.NewManager(lightNodeDir, logs.NewNodeLogger("test", 0))
	require.NoError(t, err, "Failed to create light node DB")
	defer lightNodeDB.Close()

	lightNodeDB.InitWriteQueue(10000, 200*time.Millisecond)

	// 从全节点的 StateDB 同步订单数据（不包含索引）
	// 为了模拟轻节点同步，我们直接从全节点数据库扫描出这些状态并注入到轻节点
	syncedCount := 0

	// 1. 同步订单实体 (v1_order_)
	orders, _ := fullNodeDB.Scan("v1_order_")
	for k, v := range orders {
		if !strings.HasPrefix(k, "v1_orderstate_") {
			lightNodeDB.EnqueueSet(k, string(v))
			syncedCount++
		}
	}

	// 2. 同步订单状态 (v1_orderstate_)
	orderStates, _ := fullNodeDB.Scan("v1_orderstate_")
	for k, v := range orderStates {
		lightNodeDB.EnqueueSet(k, string(v))
	}

	// 3. 同步账户订单列表 (v1_acc_orders_)
	accOrders, _ := fullNodeDB.Scan("v1_acc_orders_")
	for k, v := range accOrders {
		lightNodeDB.EnqueueSet(k, string(v))
	}

	// 强制刷盘，确保重建时能读到
	require.NoError(t, lightNodeDB.ForceFlush())

	require.NoError(t, lightNodeDB.ForceFlush())
	time.Sleep(300 * time.Millisecond)

	t.Logf("✅ Light node synced %d orders from StateDB", syncedCount)

	// 验证轻节点只有订单数据，没有索引
	lightNodeOrderCount := countDBKeysWithPrefix(t, lightNodeDB, "v1_order_")
	lightNodeIndexCount := countDBKeysWithPrefix(t, lightNodeDB, "v1_pair:")

	t.Logf("📊 Light node stats (before rebuild):")
	t.Logf("  - Orders: %d", lightNodeOrderCount)
	t.Logf("  - Indexes: %d", lightNodeIndexCount)

	assert.Equal(t, 5, lightNodeOrderCount, "Light node should have 5 orders")
	assert.Equal(t, 0, lightNodeIndexCount, "Light node should have 0 indexes before rebuild")

	// ========== 第三步：轻节点重建索引 ==========
	t.Log("📦 Step 3: Light node rebuilds indexes from order data")

	rebuiltCount, err := db.RebuildOrderPriceIndexes(lightNodeDB)
	require.NoError(t, err, "Failed to rebuild indexes")
	time.Sleep(300 * time.Millisecond)

	t.Logf("✅ Light node rebuilt %d indexes", rebuiltCount)

	// 验证重建后的索引数量
	btcPrefix2 := fmt.Sprintf("v1_pair:BTC_USDT|side:%d|is_filled:false|", pb.OrderSide_SELL)
	ethPrefix2 := fmt.Sprintf("v1_pair:ETH_USDT|side:%d|is_filled:false|", pb.OrderSide_SELL)
	lightNodeBTCIndexCount := countDBKeysWithPrefix(t, lightNodeDB, btcPrefix2)
	lightNodeETHIndexCount := countDBKeysWithPrefix(t, lightNodeDB, ethPrefix2)

	t.Logf("📊 Light node stats (after rebuild):")
	t.Logf("  - BTC indexes: %d", lightNodeBTCIndexCount)
	t.Logf("  - ETH indexes: %d", lightNodeETHIndexCount)

	assert.Equal(t, 5, rebuiltCount, "Should rebuild 5 indexes")
	assert.Equal(t, 3, lightNodeBTCIndexCount, "Light node should have 3 BTC indexes")
	assert.Equal(t, 2, lightNodeETHIndexCount, "Light node should have 2 ETH indexes")

	// ========== 第四步：验证索引内容一致性 ==========
	t.Log("📦 Step 4: Verify index consistency between full node and light node")

	// 验证 BTC 索引
	fullNodeBTCOrders, err := fullNodeDB.ScanOrdersByPairs([]string{"BTC_USDT"})
	require.NoError(t, err)

	lightNodeBTCOrders, err := lightNodeDB.ScanOrdersByPairs([]string{"BTC_USDT"})
	require.NoError(t, err)

	assert.Equal(t, len(fullNodeBTCOrders["BTC_USDT"]), len(lightNodeBTCOrders["BTC_USDT"]),
		"BTC order count should match")

	// 验证 ETH 索引
	fullNodeETHOrders, err := fullNodeDB.ScanOrdersByPairs([]string{"ETH_USDT"})
	require.NoError(t, err)

	lightNodeETHOrders, err := lightNodeDB.ScanOrdersByPairs([]string{"ETH_USDT"})
	require.NoError(t, err)

	assert.Equal(t, len(fullNodeETHOrders["ETH_USDT"]), len(lightNodeETHOrders["ETH_USDT"]),
		"ETH order count should match")

	// 验证订单数据一致性
	for _, orderID := range orderIDs {
		fullNodeOrder := getOrder(t, fullNodeDB, orderID)
		lightNodeOrder := getOrder(t, lightNodeDB, orderID)

		assert.Equal(t, fullNodeOrder.Price, lightNodeOrder.Price,
			"Order %s price should match", orderID)
		assert.Equal(t, fullNodeOrder.Amount, lightNodeOrder.Amount,
			"Order %s amount should match", orderID)
	}

	t.Log("✅ Index consistency verified")

	// ========== 测试总结 ==========
	t.Log("🎉 ========== Integration Test Summary ==========")
	t.Log("✅ Full node: Created orders and indexes")
	t.Log("✅ StateDB: Synced order data (not indexes)")
	t.Log("✅ Light node: Synced from StateDB")
	t.Log("✅ Light node: Rebuilt indexes from order data")
	t.Log("✅ Consistency: Full node and light node indexes match")
	t.Log("🎉 Light node index rebuild integration test PASSED!")
}

// ========== 辅助函数 ==========

// createTestAccount 创建测试账户（使用分离存储）
func createTestAccount(t *testing.T, dbMgr *db.Manager, address string, balances map[string]string) {
	// 创建账户（不含余额）
	account := &pb.Account{
		Address: address,
	}

	// 使用 proto 序列化账户
	accountData, err := proto.Marshal(account)
	require.NoError(t, err)

	accountKey := keys.KeyAccount(address)
	dbMgr.EnqueueSet(accountKey, string(accountData))

	// 使用 KeyBalance 分离存储余额
	for token, amount := range balances {
		bal := &pb.TokenBalanceRecord{
			Balance: &pb.TokenBalance{
				Balance:            amount,
				MinerLockedBalance: "0",
			},
		}
		balData, err := proto.Marshal(bal)
		require.NoError(t, err)
		balKey := keys.KeyBalance(address, token)
		dbMgr.EnqueueSet(balKey, string(balData))
	}
}

// getOrder 获取订单数据
func getOrder(t *testing.T, dbMgr *db.Manager, orderID string) *pb.OrderTx {
	raw, closer, err := dbMgr.Db.Get([]byte(keys.KeyOrder(orderID)))
	require.NoError(t, err)
	val := make([]byte, len(raw))
	copy(val, raw)
	closer.Close()
	var order pb.OrderTx
	require.NoError(t, proto.Unmarshal(val, &order))
	return &order
}

// countDBKeysWithPrefix 计算指定前缀的 key 数量
func countDBKeysWithPrefix(t *testing.T, dbMgr *db.Manager, prefix string) int {
	count := 0
	p := []byte(prefix)
	upper := make([]byte, len(p))
	copy(upper, p)
	for i := len(upper) - 1; i >= 0; i-- {
		upper[i]++
		if upper[i] != 0 {
			break
		}
	}
	iter, err := dbMgr.Db.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: upper})
	require.NoError(t, err)
	defer iter.Close()
	for iter.SeekGE(p); iter.Valid(); iter.Next() {
		count++
	}
	require.NoError(t, iter.Error())
	return count
}
