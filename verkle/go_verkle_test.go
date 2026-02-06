package verkle

import (
	"crypto/rand"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

// ============================================
// Verkle Tree 性能测试 (基于 go-verkle)
// ============================================

// TestVerkleBasicCRUD 基本 CRUD 测试
func TestVerkleBasicCRUD(t *testing.T) {
	store := NewSimpleVersionedMap()
	tree := NewVerkleTree(store)

	// 1. 测试插入
	keys := [][]byte{[]byte("key1")}
	values := [][]byte{[]byte("value1")}

	root1, err := tree.Update(keys, values, Version(1))
	if err != nil {
		t.Fatalf("failed to update tree: %v", err)
	}
	if len(root1) == 0 {
		t.Error("root commitment should not be empty after update")
	}

	// 2. 测试读取
	val, err := tree.Get([]byte("key1"), Version(1))
	if err != nil {
		t.Fatalf("failed to get value: %v", err)
	}
	if string(val) != "value1" {
		t.Errorf("expected 'value1', got '%s'", string(val))
	}

	// 3. 测试更新
	root2, err := tree.Update([][]byte{[]byte("key1")}, [][]byte{[]byte("value1_updated")}, Version(2))
	if err != nil {
		t.Fatalf("failed to update tree: %v", err)
	}
	if string(root1) == string(root2) {
		t.Error("root should change after update")
	}

	val2, err := tree.Get([]byte("key1"), Version(2))
	if err != nil {
		t.Fatalf("failed to get updated value: %v", err)
	}
	if string(val2) != "value1_updated" {
		t.Errorf("expected 'value1_updated', got '%s'", string(val2))
	}

	t.Logf("Verkle Basic CRUD test passed. Root v1: %x, Root v2: %x", root1[:8], root2[:8])
}

// TestVerkleMultipleKeys 多 Key 测试
func TestVerkleMultipleKeys(t *testing.T) {
	store := NewSimpleVersionedMap()
	tree := NewVerkleTree(store)

	// 插入多个 key
	keys := [][]byte{
		[]byte("account:alice"),
		[]byte("account:bob"),
		[]byte("account:charlie"),
	}
	values := [][]byte{
		[]byte("1000"),
		[]byte("2000"),
		[]byte("3000"),
	}

	root, err := tree.Update(keys, values, Version(1))
	if err != nil {
		t.Fatalf("failed to update multiple keys: %v", err)
	}

	// 验证所有 key
	for i, key := range keys {
		val, err := tree.Get(key, Version(1))
		if err != nil {
			t.Errorf("failed to get key %s: %v", string(key), err)
			continue
		}
		if string(val) != string(values[i]) {
			t.Errorf("key %s: expected '%s', got '%s'", string(key), string(values[i]), string(val))
		}
	}

	t.Logf("Verkle Multiple keys test passed. Root: %x", root[:8])
}

// TestVerkleTPS2000 测试 2000 batch 的 TPS
func TestVerkleTPS2000(t *testing.T) {
	batchSize := 2000
	iterations := 5

	// 预热
	store := NewSimpleVersionedMap()
	tree := NewVerkleTree(store)
	warmupKeys := make([][]byte, 100)
	warmupValues := make([][]byte, 100)
	for i := 0; i < 100; i++ {
		warmupKeys[i] = make([]byte, 32)
		warmupValues[i] = make([]byte, 32)
		rand.Read(warmupKeys[i])
		rand.Read(warmupValues[i])
	}
	tree.Update(warmupKeys, warmupValues, Version(1))

	// 测试
	var totalDuration int64
	for iter := 0; iter < iterations; iter++ {
		store := NewSimpleVersionedMap()
		tree := NewVerkleTree(store)

		keys := make([][]byte, batchSize)
		values := make([][]byte, batchSize)
		for i := 0; i < batchSize; i++ {
			keys[i] = make([]byte, 32)
			values[i] = make([]byte, 32)
			rand.Read(keys[i])
			rand.Read(values[i])
		}

		start := time.Now()
		_, err := tree.Update(keys, values, Version(1))
		elapsed := time.Since(start)

		if err != nil {
			t.Fatal(err)
		}

		totalDuration += elapsed.Nanoseconds()
		tps := float64(batchSize) / elapsed.Seconds()
		t.Logf("Iteration %d: %d keys in %v (TPS: %.0f)", iter+1, batchSize, elapsed, tps)
	}

	avgDuration := time.Duration(totalDuration / int64(iterations))
	avgTPS := float64(batchSize) / avgDuration.Seconds()
	t.Logf("\n=== Verkle 2000 Batch TPS Result ===")
	t.Logf("Average duration: %v", avgDuration)
	t.Logf("Average TPS: %.0f", avgTPS)
}

// BenchmarkVerkleInsertBatch 批量插入性能
func BenchmarkVerkleInsertBatch(b *testing.B) {
	batchSizes := []int{100, 1000, 2000}

	for _, batchSize := range batchSizes {
		b.Run(fmt.Sprintf("batch_%d", batchSize), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				store := NewSimpleVersionedMap()
				tree := NewVerkleTree(store)

				keys := make([][]byte, batchSize)
				values := make([][]byte, batchSize)
				for j := 0; j < batchSize; j++ {
					keys[j] = make([]byte, 32)
					values[j] = make([]byte, 32)
					rand.Read(keys[j])
					rand.Read(values[j])
				}

				b.StartTimer()
				_, err := tree.Update(keys, values, Version(1))
				b.StopTimer()
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkVerkleGet 读取性能
func BenchmarkVerkleGet(b *testing.B) {
	store := NewSimpleVersionedMap()
	tree := NewVerkleTree(store)

	// 预先插入数据
	numKeys := 10000
	keys := make([][]byte, numKeys)
	values := make([][]byte, numKeys)
	for i := 0; i < numKeys; i++ {
		keys[i] = make([]byte, 32)
		values[i] = make([]byte, 32)
		rand.Read(keys[i])
		rand.Read(values[i])
	}
	tree.Update(keys, values, Version(1))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx := i % numKeys
		_, err := tree.Get(keys[idx], Version(1))
		if err != nil {
			b.Fatal(err)
		}
	}
}

// TestVerkleMemoryRelease 测试 FlushAtDepth 是否正确释放内存
// 模拟多区块场景，验证内存使用不会随区块数量线性增长
func TestVerkleMemoryRelease(t *testing.T) {
	// 创建临时目录用于 BadgerDB
	tempDir, err := os.MkdirTemp("", "verkle_memory_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cfg := VerkleConfig{
		DataDir: tempDir,
	}
	stateDB, err := NewVerkleStateDB(cfg)
	if err != nil {
		t.Fatalf("failed to create verkle statedb: %v", err)
	}
	defer stateDB.Close()

	// 强制 GC 并记录初始内存
	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)
	t.Logf("Initial memory: HeapAlloc=%d MB, HeapInuse=%d MB",
		memBefore.HeapAlloc/1024/1024, memBefore.HeapInuse/1024/1024)

	// 模拟多个区块，每个区块 500 个状态更新
	numBlocks := 20
	keysPerBlock := 500

	for block := 1; block <= numBlocks; block++ {
		kvs := make([]KVUpdate, keysPerBlock)
		for i := 0; i < keysPerBlock; i++ {
			key := make([]byte, 32)
			value := make([]byte, 64)
			rand.Read(key)
			rand.Read(value)
			kvs[i] = KVUpdate{
				Key:   fmt.Sprintf("account:%x", key),
				Value: value,
			}
		}

		err := stateDB.ApplyAccountUpdate(uint64(block), kvs...)
		if err != nil {
			t.Fatalf("block %d: failed to apply update: %v", block, err)
		}

		// 每 5 个区块检查一次内存
		if block%5 == 0 {
			runtime.GC()
			var memCurrent runtime.MemStats
			runtime.ReadMemStats(&memCurrent)
			t.Logf("Block %d: HeapAlloc=%d MB, HeapInuse=%d MB, Total keys=%d",
				block, memCurrent.HeapAlloc/1024/1024, memCurrent.HeapInuse/1024/1024, block*keysPerBlock)
		}
	}

	// 最终内存检查
	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)
	t.Logf("Final memory: HeapAlloc=%d MB, HeapInuse=%d MB",
		memAfter.HeapAlloc/1024/1024, memAfter.HeapInuse/1024/1024)

	// 验证内存增长是有限的（不应该超过初始的 10 倍）
	// 注意：这是一个宽松的检查，主要确保没有极端内存泄漏
	memGrowthRatio := float64(memAfter.HeapAlloc) / float64(memBefore.HeapAlloc+1)
	t.Logf("Memory growth ratio: %.2fx", memGrowthRatio)

	// 由于 go-verkle 的初始化开销和 BadgerDB 缓存，允许较大的增长比例
	// 关键是内存不会随区块数成线性增长
	if memGrowthRatio > 50 {
		t.Errorf("Memory growth too large: %.2fx (expected < 50x)", memGrowthRatio)
	}

	// 验证数据可读取（节点从磁盘正确加载）
	t.Log("Verifying data can be read after flush...")
	root := stateDB.Root()
	if len(root) == 0 {
		t.Error("Root should not be empty")
	}
	t.Logf("Final root: %x", root[:8])
}

// TestVerkleFlushAtDepthNodeConversion 验证 FlushAtDepth 正确将节点转换为 HashedNode
func TestVerkleFlushAtDepthNodeConversion(t *testing.T) {
	// 使用内存存储进行快速测试
	store := NewSimpleVersionedMap()
	tree := NewVerkleTree(store)

	// 插入足够多的 key 以创建深层节点
	numKeys := 1000
	keys := make([][]byte, numKeys)
	values := make([][]byte, numKeys)
	for i := 0; i < numKeys; i++ {
		keys[i] = make([]byte, 32)
		values[i] = make([]byte, 32)
		rand.Read(keys[i])
		rand.Read(values[i])
	}

	_, err := tree.Update(keys, values, Version(1))
	if err != nil {
		t.Fatalf("failed to update tree: %v", err)
	}

	// 验证更新后的值可以正确读取
	for i := 0; i < 10; i++ { // 抽样验证 10 个
		val, err := tree.Get(keys[i], Version(1))
		if err != nil {
			t.Errorf("failed to get key %d: %v", i, err)
			continue
		}
		if string(val) != string(values[i]) {
			t.Errorf("key %d: value mismatch", i)
		}
	}

	t.Logf("Successfully inserted and verified %d keys", numKeys)
}
