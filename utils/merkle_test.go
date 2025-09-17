// merkle_test.go
package utils

import (
	"strconv"
	"testing"
	"time"
)

// generateTxs 根据传入数量生成一组模拟交易数据，每笔交易为简单的字节切片
func generateTxs(n int) [][]byte {
	txs := make([][]byte, n)
	for i := 0; i < n; i++ {
		// 例如，每笔交易为 "tx_{i}_data" 的字节序列
		txs[i] = []byte("tx_" + strconv.Itoa(i) + "_data")
	}
	return txs
}

// TestBuildBlockHash_100 测试使用 100 笔交易生成区块哈希所花的时间
func TestBuildBlockHash_100(t *testing.T) {
	txs := generateTxs(100)
	start := time.Now()
	hash := BuildBlockHash(txs)
	elapsed := time.Since(start)

	if len(hash) == 0 {
		t.Error("BuildBlockHash 返回空哈希")
	}

	t.Logf("BuildBlockHash with 100 transactions took %s", elapsed)
}

// TestBuildBlockHash_1000 测试使用 1000 笔交易生成区块哈希所花的时间
func TestBuildBlockHash_1000(t *testing.T) {
	txs := generateTxs(1000)
	start := time.Now()
	hash := BuildBlockHash(txs)
	elapsed := time.Since(start)

	if len(hash) == 0 {
		t.Error("BuildBlockHash 返回空哈希")
	}

	t.Logf("BuildBlockHash with 1000 transactions took %s", elapsed)
}

// BenchmarkBuildBlockHash_100 用于基准测试 100 笔交易生成区块哈希的性能
func BenchmarkBuildBlockHash_100(b *testing.B) {
	txs := generateTxs(100)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildBlockHash(txs)
	}
}

// BenchmarkBuildBlockHash_1000 用于基准测试 1000 笔交易生成区块哈希的性能
func BenchmarkBuildBlockHash_1000(b *testing.B) {
	txs := generateTxs(1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildBlockHash(txs)
	}
}
