package verkle

import (
	"crypto/rand"
	"fmt"
	"testing"
	"time"
)

// ============================================
// Verkle Tree 性能基准测试
// ============================================

// BenchmarkVerkleInsertSingle 单次插入性能
func BenchmarkVerkleInsertSingle(b *testing.B) {
	store := NewSimpleVersionedMap()
	tree := NewVerkleTree(store)

	// 预生成随机 key-value
	keys := make([][]byte, b.N)
	values := make([][]byte, b.N)
	for i := 0; i < b.N; i++ {
		keys[i] = make([]byte, 32)
		values[i] = make([]byte, 64)
		rand.Read(keys[i])
		rand.Read(values[i])
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := tree.Update([][]byte{keys[i]}, [][]byte{values[i]}, Version(i+1))
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkVerkleInsertBatch 批量插入性能
func BenchmarkVerkleInsertBatch(b *testing.B) {
	batchSizes := []int{10, 100, 1000}

	for _, batchSize := range batchSizes {
		b.Run(fmt.Sprintf("batch_%d", batchSize), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				store := NewSimpleVersionedMap()
				tree := NewVerkleTree(store)

				keys := make([][]byte, batchSize)
				values := make([][]byte, batchSize)
				for j := 0; j < batchSize; j++ {
					keys[j] = make([]byte, 32)
					values[j] = make([]byte, 64)
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
		values[i] = make([]byte, 64)
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

// BenchmarkVerkleProve 证明生成性能
func BenchmarkVerkleProve(b *testing.B) {
	store := NewSimpleVersionedMap()
	tree := NewVerkleTree(store)

	// 预先插入数据
	numKeys := 1000
	keys := make([][]byte, numKeys)
	values := make([][]byte, numKeys)
	for i := 0; i < numKeys; i++ {
		keys[i] = make([]byte, 32)
		values[i] = make([]byte, 64)
		rand.Read(keys[i])
		rand.Read(values[i])
	}
	tree.Update(keys, values, Version(1))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx := i % numKeys
		_, err := tree.Prove(keys[idx])
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPedersenCommit Pedersen 承诺计算性能
func BenchmarkPedersenCommit(b *testing.B) {
	committer := NewPedersenCommitter()

	// 预生成随机值
	var values [256][]byte
	for i := 0; i < 256; i++ {
		values[i] = make([]byte, 32)
		rand.Read(values[i])
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := committer.CommitToVector(values)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPedersenCommitParallel 并行 Pedersen 承诺计算性能
func BenchmarkPedersenCommitParallel(b *testing.B) {
	committer := NewPedersenCommitter()

	// 预生成随机值
	var values [256][]byte
	for i := 0; i < 256; i++ {
		values[i] = make([]byte, 32)
		rand.Read(values[i])
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := committer.CommitToVectorParallel(values)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPedersenCommitComparison 串行 vs 并行对比
func BenchmarkPedersenCommitComparison(b *testing.B) {
	committer := NewPedersenCommitter()

	var values [256][]byte
	for i := 0; i < 256; i++ {
		values[i] = make([]byte, 32)
		rand.Read(values[i])
	}

	b.Run("Serial", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			committer.CommitToVector(values)
		}
	})

	b.Run("Parallel_8workers", func(b *testing.B) {
		NumWorkers = 8
		for i := 0; i < b.N; i++ {
			committer.CommitToVectorParallel(values)
		}
	})

	b.Run("Parallel_16workers", func(b *testing.B) {
		NumWorkers = 16
		for i := 0; i < b.N; i++ {
			committer.CommitToVectorParallel(values)
		}
	})

	b.Run("Parallel_32workers", func(b *testing.B) {
		NumWorkers = 32
		for i := 0; i < b.N; i++ {
			committer.CommitToVectorParallel(values)
		}
	})
}

// BenchmarkPedersenUpdate Pedersen 增量更新性能
func BenchmarkPedersenUpdate(b *testing.B) {
	committer := NewPedersenCommitter()

	// 初始承诺
	var values [256][]byte
	for i := 0; i < 256; i++ {
		values[i] = make([]byte, 32)
		rand.Read(values[i])
	}
	commitment, _ := committer.CommitToVector(values)

	// 预生成新值
	newValues := make([][]byte, b.N)
	for i := 0; i < b.N; i++ {
		newValues[i] = make([]byte, 32)
		rand.Read(newValues[i])
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx := i % 256
		_, err := committer.UpdateCommitment(commitment, idx, newValues[i], values[idx])
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkNodeEncodeDecode 节点编解码性能
func BenchmarkNodeEncodeDecode(b *testing.B) {
	b.Run("InternalNode256", func(b *testing.B) {
		node := NewInternalNode256()
		// 添加一些子节点
		for i := 0; i < 64; i++ {
			commitment := make([]byte, 32)
			rand.Read(commitment)
			node.SetChild(byte(i*4), commitment)
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			data := EncodeInternalNode256(node)
			_, err := DecodeInternalNode256(data)
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("LeafNode", func(b *testing.B) {
		var stem [31]byte
		rand.Read(stem[:])
		node := NewLeafNode(stem)
		// 添加一些值
		for i := 0; i < 64; i++ {
			value := make([]byte, 64)
			rand.Read(value)
			node.SetValue(byte(i*4), value)
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			data := EncodeLeafNode(node)
			_, err := DecodeLeafNode(data)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkToVerkleKey Key 转换性能
func BenchmarkToVerkleKey(b *testing.B) {
	keys := make([][]byte, 1000)
	for i := 0; i < 1000; i++ {
		keys[i] = make([]byte, 32)
		rand.Read(keys[i])
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ToVerkleKey(keys[i%1000])
	}
}

// ============================================
// 大规模测试
// ============================================

// BenchmarkVerkleScalability 可扩展性测试
func BenchmarkVerkleScalability(b *testing.B) {
	scales := []int{1000, 10000, 100000}

	for _, scale := range scales {
		b.Run(fmt.Sprintf("scale_%d", scale), func(b *testing.B) {
			store := NewSimpleVersionedMap()
			tree := NewVerkleTree(store)

			// 插入指定数量的 key
			keys := make([][]byte, scale)
			values := make([][]byte, scale)
			for i := 0; i < scale; i++ {
				keys[i] = make([]byte, 32)
				values[i] = make([]byte, 64)
				rand.Read(keys[i])
				rand.Read(values[i])
			}

			// 分批插入
			batchSize := 1000
			for i := 0; i < scale; i += batchSize {
				end := i + batchSize
				if end > scale {
					end = scale
				}
				tree.Update(keys[i:end], values[i:end], Version(i/batchSize+1))
			}

			// 测试查询性能
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				idx := i % scale
				tree.Get(keys[idx], 0)
			}
		})
	}
}

// ============================================
// TPS 测试
// ============================================

// BenchmarkVerkleTPS2000Batch 测试 2000 条批量写入的实际 TPS
func BenchmarkVerkleTPS2000Batch(b *testing.B) {
	// 使用 32 workers 获得最佳性能
	NumWorkers = 32

	batchSize := 2000

	b.Run("batch_2000", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			store := NewSimpleVersionedMap()
			tree := NewVerkleTree(store)

			// 预生成 2000 个 key-value
			keys := make([][]byte, batchSize)
			values := make([][]byte, batchSize)
			for i := 0; i < batchSize; i++ {
				keys[i] = make([]byte, 32)
				values[i] = make([]byte, 64)
				rand.Read(keys[i])
				rand.Read(values[i])
			}

			b.StartTimer()
			_, err := tree.Update(keys, values, Version(1))
			b.StopTimer()

			if err != nil {
				b.Fatal(err)
			}
		}
	})

	// 计算连续多批次 TPS
	b.Run("batch_2000x10_continuous", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			store := NewSimpleVersionedMap()
			tree := NewVerkleTree(store)

			totalKeys := batchSize * 10 // 20000 keys
			keys := make([][]byte, totalKeys)
			values := make([][]byte, totalKeys)
			for i := 0; i < totalKeys; i++ {
				keys[i] = make([]byte, 32)
				values[i] = make([]byte, 64)
				rand.Read(keys[i])
				rand.Read(values[i])
			}

			b.StartTimer()
			for batch := 0; batch < 10; batch++ {
				start := batch * batchSize
				end := start + batchSize
				_, err := tree.Update(keys[start:end], values[start:end], Version(batch+1))
				if err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
		}
	})
}

// TestVerkleTPS2000 实际测试 2000 batch 的 TPS 并打印结果
func TestVerkleTPS2000(t *testing.T) {
	NumWorkers = 32

	batchSize := 2000
	iterations := 5

	// 预热
	store := NewSimpleVersionedMap()
	tree := NewVerkleTree(store)
	warmupKeys := make([][]byte, 100)
	warmupValues := make([][]byte, 100)
	for i := 0; i < 100; i++ {
		warmupKeys[i] = make([]byte, 32)
		warmupValues[i] = make([]byte, 64)
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
			values[i] = make([]byte, 64)
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
	t.Logf("\n=== 2000 Batch TPS Result ===")
	t.Logf("Average duration: %v", avgDuration)
	t.Logf("Average TPS: %.0f", avgTPS)
}
