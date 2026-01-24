package db

import (
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"dex/utils"
	"fmt"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestRebuildOrderPriceIndexes 测试从订单数据重建价格索引
// 这个测试验证了：轻节点只同步订单数据（v1_order_*），不同步索引（v1_order_price_index_*），
// 然后可以从订单数据重建索引
func TestRebuildOrderPriceIndexes(t *testing.T) {
	// 1. 创建临时数据库
	tmpDir := t.TempDir()
	dbMgr, err := NewManager(tmpDir, logs.NewNodeLogger("test", 0))
	require.NoError(t, err, "failed to create DB manager")
	defer dbMgr.Close()

	// 启动写队列
	dbMgr.InitWriteQueue(10000, 200*time.Millisecond)

	// 2. 创建测试订单（模拟全节点写入订单数据和索引）
	testOrders := []struct {
		orderID    string
		baseToken  string
		quoteToken string
		price      string
		amount     string
		isFilled   bool
	}{
		{"order_1", "BTC", "USDT", "50000", "1.0", false},
		{"order_2", "BTC", "USDT", "51000", "0.5", false},
		{"order_3", "BTC", "USDT", "49000", "2.0", false},
		{"order_4", "ETH", "USDT", "3000", "5.0", false},
		{"order_5", "ETH", "USDT", "3100", "3.0", false},
		{"order_6", "BTC", "USDT", "52000", "1.5", true}, // 已成交订单
	}

	// 3. 写入订单数据和索引（模拟全节点的完整数据）
	for _, tc := range testOrders {
		order := &pb.OrderTx{
			Base: &pb.BaseMessage{
				TxId:        tc.orderID,
				FromAddress: "test_user",
			},
			BaseToken:  tc.baseToken,
			QuoteToken: tc.quoteToken,
			Op:         pb.OrderOp_ADD,
			Price:      tc.price,
			Amount:     tc.amount,
		}

		// 写入 OrderState（存储成交信息）
		orderState := &pb.OrderState{
			OrderId:    tc.orderID,
			BaseToken:  tc.baseToken,
			QuoteToken: tc.quoteToken,
			Amount:     tc.amount,
			Price:      tc.price,
			IsFilled:   tc.isFilled,
		}
		stateData, _ := proto.Marshal(orderState)
		stateKey := keys.KeyOrderState(tc.orderID)
		dbMgr.EnqueueSet(stateKey, string(stateData))

		// 序列化订单
		orderData, err := proto.Marshal(order)
		require.NoError(t, err)

		// 写入订单数据（这个会同步到 StateDB）
		orderKey := keys.KeyOrder(tc.orderID)
		dbMgr.EnqueueSet(orderKey, string(orderData))

		// 写入价格索引（这个不会同步到 StateDB）
		pair := utils.GeneratePairKey(tc.baseToken, tc.quoteToken)
		priceKey67, err := PriceToKey128(tc.price)
		require.NoError(t, err)

		indexKey := keys.KeyOrderPriceIndex(pair, tc.isFilled, priceKey67, tc.orderID)
		indexData, _ := proto.Marshal(&pb.OrderPriceIndex{Ok: true})
		dbMgr.EnqueueSet(indexKey, string(indexData))
	}

	// 强制刷盘
	err = dbMgr.ForceFlush()
	require.NoError(t, err)
	time.Sleep(300 * time.Millisecond) // 增加等待时间确保写入完成

	// 4. 验证全节点状态：订单数据和索引都存在
	t.Run("FullNode_HasBothOrdersAndIndexes", func(t *testing.T) {
		// 验证订单数据存在
		orderCount := countKeysWithPrefix(t, dbMgr, "v1_order_")
		assert.Equal(t, 6, orderCount, "should have 6 orders")

		// 验证索引存在
		btcIndexCount := countKeysWithPrefix(t, dbMgr, "v1_pair:BTC_USDT|is_filled:false|")
		assert.Equal(t, 3, btcIndexCount, "should have 3 BTC unfilled order indexes")

		ethIndexCount := countKeysWithPrefix(t, dbMgr, "v1_pair:ETH_USDT|is_filled:false|")
		assert.Equal(t, 2, ethIndexCount, "should have 2 ETH unfilled order indexes")

		filledIndexCount := countKeysWithPrefix(t, dbMgr, "v1_pair:BTC_USDT|is_filled:true|")
		assert.Equal(t, 1, filledIndexCount, "should have 1 BTC filled order index")
	})

	// 5. 模拟轻节点场景：删除所有索引，只保留订单数据
	t.Run("LightNode_DeleteIndexes", func(t *testing.T) {
		// 删除所有价格索引
		deleteKeysWithPrefix(t, dbMgr, "v1_pair:")

		// 验证索引已删除
		indexCount := countKeysWithPrefix(t, dbMgr, "v1_pair:")
		assert.Equal(t, 0, indexCount, "all indexes should be deleted")

		// 验证订单数据仍然存在
		orderCount := countKeysWithPrefix(t, dbMgr, "v1_order_")
		assert.Equal(t, 6, orderCount, "orders should still exist")
	})

	// 6. 从订单数据重建索引
	t.Run("LightNode_RebuildIndexes", func(t *testing.T) {
		// 调用重建函数
		rebuiltCount, err := RebuildOrderPriceIndexes(dbMgr)
		require.NoError(t, err)
		assert.Equal(t, 6, rebuiltCount, "should rebuild 6 order indexes")

		// 等待写入完成
		time.Sleep(200 * time.Millisecond)

		// 验证索引已重建
		btcIndexCount := countKeysWithPrefix(t, dbMgr, "v1_pair:BTC_USDT|is_filled:false|")
		assert.Equal(t, 3, btcIndexCount, "should have 3 BTC unfilled order indexes after rebuild")

		ethIndexCount := countKeysWithPrefix(t, dbMgr, "v1_pair:ETH_USDT|is_filled:false|")
		assert.Equal(t, 2, ethIndexCount, "should have 2 ETH unfilled order indexes after rebuild")

		filledIndexCount := countKeysWithPrefix(t, dbMgr, "v1_pair:BTC_USDT|is_filled:true|")
		assert.Equal(t, 1, filledIndexCount, "should have 1 BTC filled order index after rebuild")
	})

	// 7. 验证重建的索引内容正确
	t.Run("LightNode_VerifyRebuiltIndexes", func(t *testing.T) {
		// 扫描 BTC_USDT 未成交订单索引
		pairs := []string{"BTC_USDT"}
		result, err := dbMgr.ScanOrdersByPairs(pairs)
		require.NoError(t, err)

		btcOrders := result["BTC_USDT"]
		assert.Equal(t, 3, len(btcOrders), "should have 3 BTC unfilled orders")

		// 验证可以从索引中提取订单ID
		orderIDs := make(map[string]bool)
		for indexKey := range btcOrders {
			// 解析 indexKey: "v1_pair:BTC_USDT|is_filled:false|price:xxx|order_id:order_1"
			orderID := extractOrderIDFromIndexKey(indexKey)
			if orderID != "" {
				orderIDs[orderID] = true
			}
		}

		assert.True(t, orderIDs["order_1"], "should find order_1")
		assert.True(t, orderIDs["order_2"], "should find order_2")
		assert.True(t, orderIDs["order_3"], "should find order_3")
	})
}

// TestRebuildOrderPriceIndexes_EmptyDB 测试空数据库的情况
func TestRebuildOrderPriceIndexes_EmptyDB(t *testing.T) {
	tmpDir := t.TempDir()
	dbMgr, err := NewManager(tmpDir, logs.NewNodeLogger("test", 0))
	require.NoError(t, err)
	defer dbMgr.Close()

	dbMgr.InitWriteQueue(10000, 200*time.Millisecond)

	// 重建空数据库的索引
	rebuiltCount, err := RebuildOrderPriceIndexes(dbMgr)
	require.NoError(t, err)
	assert.Equal(t, 0, rebuiltCount, "should rebuild 0 indexes for empty DB")
}

// TestRebuildOrderPriceIndexes_Performance 测试大量订单的重建性能
func TestRebuildOrderPriceIndexes_Performance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping performance test in short mode")
	}

	tmpDir := t.TempDir()
	dbMgr, err := NewManager(tmpDir, logs.NewNodeLogger("test", 0))
	require.NoError(t, err)
	defer dbMgr.Close()

	dbMgr.InitWriteQueue(10000, 200*time.Millisecond)

	// 创建 1000 个订单
	orderCount := 1000
	t.Logf("Creating %d orders...", orderCount)

	for i := 0; i < orderCount; i++ {
		order := &pb.OrderTx{
			Base: &pb.BaseMessage{
				TxId:        fmt.Sprintf("order_%d", i),
				FromAddress: "test_user",
			},
			BaseToken:  "BTC",
			QuoteToken: "USDT",
			Op:         pb.OrderOp_ADD,
			Price:      fmt.Sprintf("%d", 50000+i),
			Amount:     "1.0",
		}

		orderData, _ := proto.Marshal(order)
		orderKey := keys.KeyOrder(order.Base.TxId)
		dbMgr.EnqueueSet(orderKey, string(orderData))
	}

	err = dbMgr.ForceFlush()
	require.NoError(t, err)
	time.Sleep(500 * time.Millisecond) // 增加等待时间确保写入完成

	// 验证订单是否全部写入
	actualOrderCount := countKeysWithPrefix(t, dbMgr, "v1_order_")
	require.Equal(t, orderCount, actualOrderCount, "all orders should be written before rebuild")

	// 测试重建性能
	start := time.Now()
	rebuiltCount, err := RebuildOrderPriceIndexes(dbMgr)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, orderCount, rebuiltCount)
	t.Logf("Rebuilt %d order indexes in %v (%.2f orders/sec)",
		rebuiltCount, elapsed, float64(rebuiltCount)/elapsed.Seconds())

	// 等待写入完成
	time.Sleep(200 * time.Millisecond)

	// 验证索引正确性
	indexCount := countKeysWithPrefix(t, dbMgr, "v1_pair:BTC_USDT|is_filled:false|")
	assert.Equal(t, orderCount, indexCount, "should have correct number of indexes")
}

// ========== 辅助函数 ==========

// countKeysWithPrefix 计算指定前缀的 key 数量
func countKeysWithPrefix(t *testing.T, dbMgr *Manager, prefix string) int {
	count := 0
	err := dbMgr.Db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()

		p := []byte(prefix)
		for it.Seek(p); it.ValidForPrefix(p); it.Next() {
			count++
		}
		return nil
	})
	require.NoError(t, err)
	return count
}

// deleteKeysWithPrefix 删除指定前缀的所有 key
func deleteKeysWithPrefix(t *testing.T, dbMgr *Manager, prefix string) {
	err := dbMgr.Db.Update(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()

		p := []byte(prefix)
		for it.Seek(p); it.ValidForPrefix(p); it.Next() {
			key := it.Item().KeyCopy(nil)
			if err := txn.Delete(key); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
}

// extractOrderIDFromIndexKey 从索引 key 中提取订单 ID
// 索引格式: "v1_pair:BTC_USDT|is_filled:false|price:xxx|order_id:order_1"
func extractOrderIDFromIndexKey(indexKey string) string {
	// 简单实现：查找 "|order_id:" 后面的部分
	const marker = "|order_id:"
	for i := len(indexKey) - len(marker); i >= 0; i-- {
		if i+len(marker) <= len(indexKey) && indexKey[i:i+len(marker)] == marker {
			return indexKey[i+len(marker):]
		}
	}
	return ""
}
