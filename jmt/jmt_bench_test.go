package smt

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

// ============================================
// JMT 性能基准测试
// ============================================

// BenchmarkJMT_Insert 测试插入性能
func BenchmarkJMT_Insert(b *testing.B) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	keys := make([][]byte, b.N)
	values := make([][]byte, b.N)
	for i := 0; i < b.N; i++ {
		keys[i] = []byte(fmt.Sprintf("key-%08d", i))
		values[i] = []byte(fmt.Sprintf("value-%08d", i))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := jmt.Update([][]byte{keys[i]}, [][]byte{values[i]}, Version(i+1))
		if err != nil {
			b.Fatalf("Update failed: %v", err)
		}
	}
}

// BenchmarkJMT_BatchInsert 测试批量插入性能
func BenchmarkJMT_BatchInsert(b *testing.B) {
	batchSizes := []int{10, 100, 1000}

	for _, batchSize := range batchSizes {
		b.Run(fmt.Sprintf("batch-%d", batchSize), func(b *testing.B) {
			store := NewSimpleVersionedMap()
			jmt := NewJMT(store, sha256.New())

			keys := make([][]byte, batchSize)
			values := make([][]byte, batchSize)
			for i := 0; i < batchSize; i++ {
				keys[i] = []byte(fmt.Sprintf("key-%08d", i))
				values[i] = []byte(fmt.Sprintf("value-%08d", i))
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// 每次迭代用不同的 key 前缀避免覆盖
				for j := 0; j < batchSize; j++ {
					keys[j] = []byte(fmt.Sprintf("key-%d-%08d", i, j))
				}
				_, err := jmt.Update(keys, values, Version(i+1))
				if err != nil {
					b.Fatalf("Update failed: %v", err)
				}
			}
		})
	}
}

// BenchmarkJMT_Get 测试查询性能
func BenchmarkJMT_Get(b *testing.B) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	// 预先插入 10000 个 Key
	count := 10000
	keys := make([][]byte, count)
	values := make([][]byte, count)
	for i := 0; i < count; i++ {
		keys[i] = []byte(fmt.Sprintf("key-%08d", i))
		values[i] = []byte(fmt.Sprintf("value-%08d", i))
	}
	_, err := jmt.Update(keys, values, 1)
	if err != nil {
		b.Fatalf("Setup failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := keys[i%count]
		_, err := jmt.Get(key, 0)
		if err != nil {
			b.Fatalf("Get failed: %v", err)
		}
	}
}

// BenchmarkJMT_Prove 测试 Proof 生成性能
func BenchmarkJMT_Prove(b *testing.B) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	// 预先插入 10000 个 Key
	count := 10000
	keys := make([][]byte, count)
	values := make([][]byte, count)
	for i := 0; i < count; i++ {
		keys[i] = []byte(fmt.Sprintf("key-%08d", i))
		values[i] = []byte(fmt.Sprintf("value-%08d", i))
	}
	_, err := jmt.Update(keys, values, 1)
	if err != nil {
		b.Fatalf("Setup failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := keys[i%count]
		_, err := jmt.Prove(key)
		if err != nil {
			b.Fatalf("Prove failed: %v", err)
		}
	}
}

// BenchmarkJMT_VerifyProof 测试 Proof 验证性能
func BenchmarkJMT_VerifyProof(b *testing.B) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	// 预先插入 1000 个 Key
	count := 1000
	keys := make([][]byte, count)
	values := make([][]byte, count)
	for i := 0; i < count; i++ {
		keys[i] = []byte(fmt.Sprintf("key-%08d", i))
		values[i] = []byte(fmt.Sprintf("value-%08d", i))
	}
	root, err := jmt.Update(keys, values, 1)
	if err != nil {
		b.Fatalf("Setup failed: %v", err)
	}

	// 预生成 Proofs
	proofs := make([]*JMTProof, count)
	for i := 0; i < count; i++ {
		proofs[i], err = jmt.Prove(keys[i])
		if err != nil {
			b.Fatalf("Prove failed: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		proof := proofs[i%count]
		if !VerifyJMTProof(proof, root, sha256.New()) {
			b.Fatalf("Verify failed")
		}
	}
}

// BenchmarkJMT_Delete 测试删除性能
func BenchmarkJMT_Delete(b *testing.B) {
	// 每次迭代重新创建树
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		store := NewSimpleVersionedMap()
		jmt := NewJMT(store, sha256.New())

		// 插入 100 个 Key
		keys := make([][]byte, 100)
		values := make([][]byte, 100)
		for j := 0; j < 100; j++ {
			keys[j] = []byte(fmt.Sprintf("key-%08d", j))
			values[j] = []byte(fmt.Sprintf("value-%08d", j))
		}
		jmt.Update(keys, values, 1)

		b.StartTimer()
		// 删除一个 Key
		_, err := jmt.Delete(keys[50], 2)
		if err != nil {
			b.Fatalf("Delete failed: %v", err)
		}
	}
}

// ============================================
// 内存和大小测试
// ============================================

func TestJMT_ProofSizeAnalysis(t *testing.T) {
	sizes := []int{100, 1000, 10000}

	for _, size := range sizes {
		t.Run(fmt.Sprintf("keys-%d", size), func(t *testing.T) {
			store := NewSimpleVersionedMap()
			jmt := NewJMT(store, sha256.New())

			keys := make([][]byte, size)
			values := make([][]byte, size)
			for i := 0; i < size; i++ {
				keys[i] = []byte(fmt.Sprintf("key-%08d", i))
				values[i] = []byte(fmt.Sprintf("value-%08d", i))
			}

			_, err := jmt.Update(keys, values, 1)
			if err != nil {
				t.Fatalf("Update failed: %v", err)
			}

			// 采样 10 个 Key 的 Proof 大小
			var totalSize, totalSiblings int
			samples := 10
			for i := 0; i < samples; i++ {
				idx := (i * size) / samples
				proof, err := jmt.Prove(keys[idx])
				if err != nil {
					t.Fatalf("Prove failed: %v", err)
				}
				totalSize += proof.ProofSize(32)
				totalSiblings += len(proof.Siblings)
			}

			avgSize := totalSize / samples
			avgSiblings := totalSiblings / samples
			t.Logf("Keys: %d, Avg Proof Size: %d bytes, Avg Siblings: %d",
				size, avgSize, avgSiblings)
		})
	}
}

// ============================================
// 单独 JMT 性能测试
// ============================================

func BenchmarkJMT_SingleInsert(b *testing.B) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := []byte(fmt.Sprintf("key-%08d", i))
		value := []byte(fmt.Sprintf("value-%08d", i))
		_, err := jmt.Update([][]byte{key}, [][]byte{value}, Version(i+1))
		if err != nil {
			b.Fatalf("Update failed: %v", err)
		}
	}
}
