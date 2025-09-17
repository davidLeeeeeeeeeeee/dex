// db_order_test.go
package db

import (
	"fmt"
	"github.com/dgraph-io/badger/v4"
	"math/rand"
	"strconv"
	"testing"
	"time"
)

// TestBulkInsertOrders 批量写入测试订单到数据库，并测量写入耗时
func TestBulkInsertOrders(t *testing.T) {
	mgr, err := NewManager("./data")
	if err != nil {
		t.Fatalf("Failed to get DB instance: %v", err)
	}
	defer mgr.Close()

	// 为了防止影响测试结果，可以先清空旧数据(可选)
	cleanupAllTestOrders(mgr, t)

	// 准备生成多少个订单
	const totalOrders = 65000

	// 随机种子
	rand.Seed(time.Now().UnixNano())

	start := time.Now()
	for i := 0; i < totalOrders; i++ {
		price := fmt.Sprintf("%.2f", (rand.Float64()*9000)+1000) // 1000~10000之间
		orderTxId := "ordertx_" + strconv.Itoa(i)
		order := &OrderTx{
			Base: &BaseMessage{
				TxId:         orderTxId,
				FromAddress:  "testAddr",
				TargetHeight: uint64(100 + i),
			},
			BaseToken:  "ETH",
			QuoteToken: "USDT",
			Price:      price,
			Op:         OrderOp_ADD,
		}

		err := SaveOrderTx(mgr, order)
		if err != nil {
			t.Fatalf("SaveOrderTx error on %s: %v", orderTxId, err)
		}
	}
	elapsed := time.Since(start)

	t.Logf("Inserted %d orders. Elapsed time: %v, avg: %v/op",
		totalOrders, elapsed, elapsed/time.Duration(totalOrders))
}

// TestQueryPriceRange 测试从数据库中查找特定价格区间的订单，并把结果读到内存，统计耗时
func TestQueryPriceRange(t *testing.T) {
	mgr, err := NewManager("./data")
	if err != nil {
		t.Fatalf("Failed to get DB instance: %v", err)
	}
	defer mgr.Close()

	// 设定要查询的交易对 & 价格范围
	pair := "ETH_USDT"
	minPrice := 1000.00
	maxPrice := 9000.00

	start := time.Now()
	ordersInRange, err := queryOrdersByPriceRange(mgr, pair, minPrice, maxPrice)
	if err != nil {
		t.Fatalf("queryOrdersByPriceRange error: %v", err)
	}
	elapsed := time.Since(start)

	t.Logf("Found %d orders in [%.2f, %.2f], Elapsed time: %v",
		len(ordersInRange), minPrice, maxPrice, elapsed)
}

// queryOrdersByPriceRange 演示如何按照 "pair:...|price:...|order_id:..." 进行区间扫描
func queryOrdersByPriceRange(mgr *Manager, pair string, minPrice, maxPrice float64) ([]*OrderTx, error) {
	var result []*OrderTx

	// 比如二级索引key形如:  "pair:ETH_USDT|price:123.45|order_id:xxx"
	// 这里构造前缀或Seek开始/结束key
	startKey := []byte(fmt.Sprintf("pair:%s|is_filled:false|price:%f", pair, minPrice))
	endKey := []byte(fmt.Sprintf("pair:%s|is_filled:false|price:%f", pair, maxPrice))

	err := mgr.Db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		// Seek到startKey，然后往后遍历，直到超过endKey
		for it.Seek(startKey); it.Valid(); it.Next() {
			item := it.Item()
			keyBytes := item.Key()

			// 如果 key > endKey，说明已经超出区间，可以break
			if compareBytes(keyBytes, endKey) > 0 {
				break
			}

			// 检查一下是否真的是同一个前缀"pair:xxx|price:",也可再细化判断
			_, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			// valBytes 里存的是  OrderPrice proto (在 SaveOrderTx 里写的)
			// 这里我们只需要拿到 orderId 就可以再去读真正的 OrderTx
			// 但为了示例简单，我们可以直接从 key 里解析 orderId

			orderId := parseOrderIdFromKey(string(keyBytes))
			if orderId == "" {
				// 如果解析失败就跳过
				continue
			}

			// 根据 orderId 拿到真正的 OrderTx
			order, err := GetOrderTx(mgr, orderId)
			if err != nil {
				// 如果找不到，可能已经被remove之类，可选择跳过或继续
				continue
			}
			// 这里可以再判断一下 price 是否真在 [minPrice, maxPrice]之间
			// (因为字符串格式化比较易有精度问题)
			pf, _ := strconv.ParseFloat(order.Price, 64)
			if pf >= minPrice && pf <= maxPrice {
				result = append(result, order)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// parseOrderIdFromKey 简易示例，从 key 形如 "pair:ETH_USDT|price:123.45|order_id:ordertx_666"
// 提取出 "ordertx_666"
func parseOrderIdFromKey(fullKey string) string {
	// 可以更安全地用 strings.Split 之类处理，这里做示例
	// 假定一定包含 "order_id:"
	idx := len(fullKey)
	prefix := "order_id:"
	pos := -1
	for i := 0; i < len(fullKey); i++ {
		if i+len(prefix) <= len(fullKey) && fullKey[i:i+len(prefix)] == prefix {
			pos = i + len(prefix)
			break
		}
	}
	if pos < 0 {
		return ""
	}
	return fullKey[pos:idx]
}

// compareBytes 比较两个[]byte大小
func compareBytes(a, b []byte) int {
	return bytesCompare(a, b)
}

// bytesCompare 实现一个简单的字节切片比较
func bytesCompare(a, b []byte) int {
	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}
	for i := 0; i < minLen; i++ {
		if a[i] < b[i] {
			return -1
		} else if a[i] > b[i] {
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}

// cleanupAllTestOrders 可选辅助函数：清理所有形如 "order_" 和 "pair:..." 的记录
func cleanupAllTestOrders(mgr *Manager, t *testing.T) {
	err := mgr.Db.Update(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			k := item.Key()

			keyStr := string(k)
			// 这里简单判断：如果是 order_... 或 pair:... 就删
			if len(keyStr) >= 6 && keyStr[:6] == "order_" {
				if err := txn.Delete(k); err != nil {
					return err
				}
			}
			if len(keyStr) >= 5 && keyStr[:5] == "pair:" {
				if err := txn.Delete(k); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Logf("cleanupAllTestOrders error: %v", err)
	}
}
