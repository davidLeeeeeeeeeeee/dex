package verkle

import (
	"crypto/rand"
	"fmt"
	"testing"
	"time"
)

// ============================================
// Go-Verkle 性能测试
// ============================================

// TestGoVerkleBasicCRUD 基本 CRUD 测试
func TestGoVerkleBasicCRUD(t *testing.T) {
	store := NewSimpleVersionedMap()
	tree := NewGoVerkleTree(store)

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

	t.Logf("Go-Verkle Basic CRUD test passed. Root v1: %x, Root v2: %x", root1[:8], root2[:8])
}

// TestGoVerkleMultipleKeys 多 Key 测试
func TestGoVerkleMultipleKeys(t *testing.T) {
	store := NewSimpleVersionedMap()
	tree := NewGoVerkleTree(store)

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

	t.Logf("Go-Verkle Multiple keys test passed. Root: %x", root[:8])
}

// TestGoVerkleTPS2000 测试 2000 batch 的 TPS
func TestGoVerkleTPS2000(t *testing.T) {
	batchSize := 2000
	iterations := 5

	// 预热
	store := NewSimpleVersionedMap()
	tree := NewGoVerkleTree(store)
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
		tree := NewGoVerkleTree(store)

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
	t.Logf("\n=== Go-Verkle 2000 Batch TPS Result ===")
	t.Logf("Average duration: %v", avgDuration)
	t.Logf("Average TPS: %.0f", avgTPS)
}

// BenchmarkGoVerkleInsertBatch go-verkle 批量插入性能
func BenchmarkGoVerkleInsertBatch(b *testing.B) {
	batchSizes := []int{100, 1000, 2000}

	for _, batchSize := range batchSizes {
		b.Run(fmt.Sprintf("batch_%d", batchSize), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				store := NewSimpleVersionedMap()
				tree := NewGoVerkleTree(store)

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

// BenchmarkGoVerkleGet go-verkle 读取性能
func BenchmarkGoVerkleGet(b *testing.B) {
	store := NewSimpleVersionedMap()
	tree := NewGoVerkleTree(store)

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

// BenchmarkComparison 对比原生实现和 go-verkle
func BenchmarkComparison(b *testing.B) {
	batchSize := 1000

	b.Run("Native_Verkle", func(b *testing.B) {
		NumWorkers = 32
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
			tree.Update(keys, values, Version(1))
			b.StopTimer()
		}
	})

	b.Run("Go_Verkle", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			store := NewSimpleVersionedMap()
			tree := NewGoVerkleTree(store)

			keys := make([][]byte, batchSize)
			values := make([][]byte, batchSize)
			for j := 0; j < batchSize; j++ {
				keys[j] = make([]byte, 32)
				values[j] = make([]byte, 32)
				rand.Read(keys[j])
				rand.Read(values[j])
			}

			b.StartTimer()
			tree.Update(keys, values, Version(1))
			b.StopTimer()
		}
	})
}
