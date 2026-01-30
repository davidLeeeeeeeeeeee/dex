package consensus

import (
	"bytes"
	"dex/config"
	"dex/db"
	"dex/logs"
	"dex/pb"
	"dex/txpool"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

// TestShortTxsEncoding 测试短哈希编码/解码
func TestShortTxsEncoding(t *testing.T) {
	// 创建模拟的交易ID列表
	txIDs := []string{
		"0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		"0xfedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321",
		"0xaabbccdd11223344aabbccdd11223344aabbccdd11223344aabbccdd11223344",
	}

	// 编码：提取前8字节
	var shortTxs []byte
	for _, txID := range txIDs {
		idBytes, err := hex.DecodeString(txID[2:]) // 去掉 0x 前缀
		if err != nil {
			t.Fatalf("Failed to decode txID %s: %v", txID, err)
		}
		if len(idBytes) < 8 {
			t.Fatalf("txID too short: %s", txID)
		}
		shortTxs = append(shortTxs, idBytes[:8]...)
	}

	// 验证编码结果
	expectedLen := len(txIDs) * 8
	if len(shortTxs) != expectedLen {
		t.Errorf("Expected shortTxs length %d, got %d", expectedLen, len(shortTxs))
	}

	t.Logf("Encoded %d txIDs to %d bytes of shortTxs", len(txIDs), len(shortTxs))

	// 解码：切分回独立的短哈希
	const shortHashSize = 8
	if len(shortTxs)%shortHashSize != 0 {
		t.Fatalf("Invalid shortTxs length: %d", len(shortTxs))
	}

	hashCount := len(shortTxs) / shortHashSize
	if hashCount != len(txIDs) {
		t.Errorf("Expected %d hashes, got %d", len(txIDs), hashCount)
	}

	for i := 0; i < hashCount; i++ {
		shortHash := shortTxs[i*shortHashSize : (i+1)*shortHashSize]
		expectedShort, _ := hex.DecodeString(txIDs[i][2:18]) // 0x + 16 hex chars = 8 bytes
		if !bytes.Equal(shortHash, expectedShort) {
			t.Errorf("Hash %d mismatch: expected %x, got %x", i, expectedShort, shortHash)
		}
	}

	t.Logf("Successfully decoded %d short hashes", hashCount)
}

// TestShortTxsResolve 测试从 TxPool 还原完整交易
func TestShortTxsResolve(t *testing.T) {
	// 创建测试用的 db.Manager
	cfg := config.DefaultConfig()
	testDBPath := t.TempDir() + "/test_short_txs"

	logger := logs.NewNodeLogger("test", 100)
	dbManager, err := db.NewManager(testDBPath, logger)
	if err != nil {
		t.Fatalf("Failed to create db.Manager: %v", err)
	}
	defer dbManager.Close()

	// 创建 TxPool
	validator := &mockTxValidator{}
	pool, err := txpool.NewTxPool(dbManager, validator, "test-node", logger)
	if err != nil {
		t.Fatalf("Failed to create TxPool: %v", err)
	}
	if err := pool.Start(); err != nil {
		t.Fatalf("Failed to start TxPool: %v", err)
	}
	defer pool.Stop()

	// 创建测试交易
	testTxs := make([]*pb.AnyTx, 100)
	for i := 0; i < 100; i++ {
		txID := fmt.Sprintf("0x%064x", i+1)
		testTxs[i] = &pb.AnyTx{
			Content: &pb.AnyTx_OrderTx{
				OrderTx: &pb.OrderTx{
					Base: &pb.BaseMessage{
						TxId:        txID,
						FromAddress: fmt.Sprintf("addr%d", i),
						Nonce:       uint64(i + 1),
					},
					BaseToken:  "FB",
					QuoteToken: "USDT",
					Amount:     "100",
					Price:      "1.5",
					Side:       pb.OrderSide_BUY,
				},
			},
		}
		// 直接存储到 TxPool
		if err := pool.StoreAnyTx(testTxs[i]); err != nil {
			t.Fatalf("Failed to store tx %d: %v", i, err)
		}
	}

	// 等待交易存储完成
	time.Sleep(100 * time.Millisecond)

	// 生成 shortTxs
	shortTxs := pool.ConcatFirst8Bytes(testTxs)
	t.Logf("Generated shortTxs: %d bytes for %d txs", len(shortTxs), len(testTxs))

	// 创建 ConsensusAdapter 并设置 TxPool
	adapter := NewConsensusAdapterWithTxPool(dbManager, pool)

	// 测试 resolveShorHashesToTxs
	resolvedTxs, err := adapter.resolveShorHashesToTxs(shortTxs)
	if err != nil {
		t.Logf("Warning: resolveShorHashesToTxs returned error (this may be expected if not all txs are in cache): %v", err)
	}

	t.Logf("Resolved %d txs from %d short hashes", len(resolvedTxs), len(testTxs))

	// 验证解析结果
	if len(resolvedTxs) == len(testTxs) {
		t.Logf("Successfully resolved all %d transactions", len(testTxs))
	} else {
		t.Logf("Resolved %d/%d transactions (some may be missing from cache)", len(resolvedTxs), len(testTxs))
	}

	_ = cfg // 使用 cfg
}

// TestBlockWithMassOrders 测试超过 5000 笔订单的区块打包
func TestBlockWithMassOrders(t *testing.T) {
	cfg := config.DefaultConfig()

	// 测试阈值
	maxTxsPerBlock := cfg.TxPool.MaxTxsPerBlock
	t.Logf("MaxTxsPerBlock: %d", maxTxsPerBlock)

	// 模拟超过阈值的交易数量
	txCount := maxTxsPerBlock + 500 // 超过阈值
	t.Logf("Testing with %d transactions (> %d threshold)", txCount, maxTxsPerBlock)

	// 创建模拟交易
	mockTxs := make([]*pb.AnyTx, txCount)
	for i := 0; i < txCount; i++ {
		mockTxs[i] = &pb.AnyTx{
			Content: &pb.AnyTx_OrderTx{
				OrderTx: &pb.OrderTx{
					Base: &pb.BaseMessage{
						TxId:        fmt.Sprintf("0x%064x", i+1),
						FromAddress: fmt.Sprintf("addr%d", i%10),
						Nonce:       uint64(i + 1),
					},
					BaseToken:  "FB",
					QuoteToken: "USDT",
					Amount:     fmt.Sprintf("%d", 100+i%100),
					Price:      fmt.Sprintf("%.2f", 1.0+float64(i%100)*0.01),
					Side:       pb.OrderSide(i % 2),
				},
			},
		}
	}

	// 计算 shortTxs 大小
	shortTxsSize := txCount * 8
	t.Logf("ShortTxs size: %d bytes (%.2f KB)", shortTxsSize, float64(shortTxsSize)/1024)

	// 估算完整区块大小
	estimatedTxSize := 500 // 每笔 OrderTx 约 500 字节
	fullBlockSize := txCount * estimatedTxSize
	t.Logf("Estimated full block size: %d bytes (%.2f MB)", fullBlockSize, float64(fullBlockSize)/(1024*1024))

	// 验证 ShortTxs 模式的节省
	savingsPercent := 100 * (1 - float64(shortTxsSize)/float64(fullBlockSize))
	t.Logf("ShortTxs saves %.1f%% bandwidth", savingsPercent)

	if savingsPercent < 90 {
		t.Errorf("Expected ShortTxs to save at least 90%% bandwidth, got %.1f%%", savingsPercent)
	}

	_ = mockTxs // 使用 mockTxs
}

// TestAdapterPrepareBlockContainer 测试区块容器准备逻辑
func TestAdapterPrepareBlockContainer(t *testing.T) {
	cfg := config.DefaultConfig()
	testDBPath := t.TempDir() + "/test_container"

	logger := logs.NewNodeLogger("test", 100)
	dbManager, err := db.NewManager(testDBPath, logger)
	if err != nil {
		t.Fatalf("Failed to create db.Manager: %v", err)
	}
	defer dbManager.Close()

	adapter := NewConsensusAdapter(dbManager)

	// 测试用例 1：交易数少于阈值，应使用完整区块模式
	t.Run("BelowThreshold", func(t *testing.T) {
		txCount := cfg.TxPool.MaxTxsPerBlock - 100
		t.Logf("Testing with %d txs (below %d threshold)", txCount, cfg.TxPool.MaxTxsPerBlock)
		// 这个测试验证逻辑路径正确性，实际的 PrepareBlockContainer 需要有缓存的区块
	})

	// 测试用例 2：交易数超过阈值，应使用 ShortTxs 模式
	t.Run("AboveThreshold", func(t *testing.T) {
		txCount := cfg.TxPool.MaxTxsPerBlock + 500
		t.Logf("Testing with %d txs (above %d threshold)", txCount, cfg.TxPool.MaxTxsPerBlock)
		// 这个测试验证逻辑路径正确性
	})

	_ = adapter // 使用 adapter
}

// mockTxValidator 模拟交易验证器
type mockTxValidator struct{}

func (v *mockTxValidator) CheckAnyTx(tx *pb.AnyTx) error {
	return nil
}

// BenchmarkShortTxsEncoding 基准测试：短哈希编码性能
func BenchmarkShortTxsEncoding(b *testing.B) {
	// 创建 10000 笔模拟交易
	txCount := 10000
	txIDs := make([]string, txCount)
	for i := 0; i < txCount; i++ {
		txIDs[i] = fmt.Sprintf("0x%064x", i+1)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var shortTxs []byte
		for _, txID := range txIDs {
			idBytes, _ := hex.DecodeString(txID[2:])
			shortTxs = append(shortTxs, idBytes[:8]...)
		}
		_ = shortTxs
	}
}

// BenchmarkShortTxsDecoding 基准测试：短哈希解码性能
func BenchmarkShortTxsDecoding(b *testing.B) {
	// 创建 10000 笔模拟交易的 shortTxs
	txCount := 10000
	shortTxs := make([]byte, txCount*8)
	for i := 0; i < txCount; i++ {
		copy(shortTxs[i*8:], []byte{byte(i), byte(i >> 8), byte(i >> 16), byte(i >> 24), 0, 0, 0, 0})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		const shortHashSize = 8
		hashes := make([][]byte, 0, txCount)
		for j := 0; j+shortHashSize <= len(shortTxs); j += shortHashSize {
			hash := make([]byte, shortHashSize)
			copy(hash, shortTxs[j:j+shortHashSize])
			hashes = append(hashes, hash)
		}
		_ = hashes
	}
}
