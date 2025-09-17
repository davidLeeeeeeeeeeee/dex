package db_test

import (
	"fmt"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"dex/db"
)

// 1. TestWriteHugeData 用于写入约 2GB 的数据，并关闭数据库
func TestWriteHugeData(t *testing.T) {
	mgr, err := db.NewManager("./data")
	mgr.InitWriteQueue(1000, 200*time.Millisecond)
	require.NoError(t, err, "failed to get DB instance")
	defer mgr.Close()

	// 举例：写入 100 万笔 Transaction，每笔大小视具体字段情况而定
	// 实际要根据你实际Transaction大小、写入条数是否确实可达2G而微调
	const totalTxCount = 65000

	t.Logf("开始写入 %d 笔交易...", totalTxCount)
	start := time.Now()
	for i := 0; i < totalTxCount; i++ {
		tx := &db.Transaction{
			Base: &db.BaseMessage{
				TxId:         fmt.Sprintf("tx_huge_data_%d", i),
				FromAddress:  randomString(20),
				TargetHeight: rand.Uint64(),
				PublicKey:    randomString(50),
				Signature:    randomString(128),
				Nonce:        rand.Uint64(),
			},
			To:     randomString(20),
			Amount: "1000000",
		}

		err := db.SaveTransaction(mgr, tx)
		if err != nil {
			t.Fatalf("failed to save tx %d: %v", i, err)
		}
	}

	elapsed := time.Since(start)
	t.Logf("写入完毕，总耗时: %v", elapsed)
	// 此时测试函数会在 defer 的 mgr.Close() 中关闭数据库

	// 可以在这里打印写入完成后的数据库大小等信息（例如用os.Stat去看db文件大小）。
}

// 2. TestQueryMultipleTransactions 用于重新打开数据库，随机查询 1000 笔交易，并统计耗时
func TestQueryMultipleTransactions_Parallel(t *testing.T) {
	mgr, err := db.NewManager("./data")
	mgr.InitWriteQueue(1000, 200*time.Millisecond)
	require.NoError(t, err, "failed to get DB instance")
	defer mgr.Close()

	const queryCount = 1000
	const concurrency = 16 // 并发度，可根据 CPU 核心数/测试目的进行调节

	// 生成需要查询的 txID
	txIDs := make([]string, queryCount)
	for i := 0; i < queryCount; i++ {
		randomIndex := rand.Intn(65000) // 这里假设写入阶段曾写过100万笔
		txIDs[i] = fmt.Sprintf("tx_huge_data_%d", randomIndex)
	}

	t.Logf("开始并发查询 %d 笔交易，goroutine数量: %d", queryCount, concurrency)
	start := time.Now()

	// 使用WaitGroup等待所有goroutine完成
	var wg sync.WaitGroup
	wg.Add(concurrency)

	// 每个goroutine要处理的数量
	batchSize := queryCount / concurrency

	for g := 0; g < concurrency; g++ {
		go func(gid int) {
			defer wg.Done()

			// 计算本 goroutine 的起止下标
			begin := gid * batchSize
			end := begin + batchSize
			// 如果不能整除，有余数，最后一批可能需要到 queryCount
			if gid == concurrency-1 {
				end = queryCount
			}

			// 执行查询
			for i := begin; i < end; i++ {
				tx, err := db.GetTransaction(mgr, txIDs[i])
				// 如果发生错误，或者结果为空，可以记录下来或者用 t.Errorf
				if err != nil {
					t.Errorf("failed to get transaction %s: %v", txIDs[i], err)
					continue
				}
				if tx == nil {
					t.Errorf("transaction is nil %s", txIDs[i])
					continue
				}
				t.Logf(txIDs[i], tx.To, tx.Amount)
			}
		}(g)
	}

	// 等待所有goroutine完成
	wg.Wait()

	elapsed := time.Since(start)
	t.Logf("并发查询完毕，总耗时: %v, 平均单次: %v",
		elapsed, elapsed/time.Duration(queryCount))
}

// TestPriceTruncation 验证在 [1e-33, 1e33] 区间内带有小数的价格是否会被“向下取整”截断
func TestPriceTruncation(t *testing.T) {
	// 1) 给定一个带有小数的价格（位于合法区间）
	input := "1.23456789e-30"
	// 预期：乘以1e33后大约是 1234.56789，再转换big.Int => 1234

	// 2) 调用 PriceToKey128
	key, err := db.PriceToKey128(input)
	assert.NoError(t, err, "不应报错，因为价格在合法区间内")
	t.Logf("Truncation test: input=%s => key=%s (len=%d)", input, key, len(key))

	// 3) 反向解析回 decimal
	decValue, err := db.KeyToPriceDecimal(key)
	assert.NoError(t, err)
	t.Logf("Parsed back decimal (no float64): %s", decValue)

	// 4) 比较原始值与解析后的值
	original, _ := decimal.NewFromString(input)
	diff := original.Sub(decValue).Abs()

	t.Logf("Original = %s\nDecoded = %s\nDiff    = %s", original, decValue, diff)

	// 验证截断：解析后 decValue 应 <= 原值
	assert.True(t, decValue.LessThanOrEqual(original),
		"期望解析后的小数值 <= 原始值(说明发生了向下截断)")

	// 差值应在一个合理范围内（例如不超过 1e-33）
	threshold := decimal.New(1, 33)
	assert.True(t, diff.LessThan(threshold),
		fmt.Sprintf("截断后的差值应小于 %v, 实际 diff=%s", threshold, diff))
}

// 辅助函数: 生成一个长度为n的随机字符串
func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := 0; i < n; i++ {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}
