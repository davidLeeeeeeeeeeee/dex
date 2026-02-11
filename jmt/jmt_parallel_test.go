package smt

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"

	"dex/config"

	"github.com/dgraph-io/badger/v2"
)

// BenchmarkParallelUpdate 对比并行与串行更新的性能
func BenchmarkParallelUpdate(b *testing.B) {
	batchSizes := []int{100, 500, 1000, 2000}

	for _, size := range batchSizes {
		b.Run(fmt.Sprintf("Serial-%d", size), func(b *testing.B) {
			tmpDir, _ := os.MkdirTemp("", "jmt_serial")
			defer os.RemoveAll(tmpDir)

			cfg_default := config.DefaultConfig()
			opts := badger.DefaultOptions(tmpDir).WithLogger(nil).WithSyncWrites(false)
			opts.MaxTableSize = cfg_default.Database.BaseTableSize
			db, _ := badger.Open(opts)
			defer db.Close()

			store := NewVersionedBadgerStore(db, []byte("jmt:"))
			jmt := NewJMT(store, sha256.New())

			keys := make([][]byte, size)
			values := make([][]byte, size)
			for i := 0; i < size; i++ {
				keys[i] = []byte(fmt.Sprintf("key-%08d", i))
				values[i] = []byte(fmt.Sprintf("value-%08d", i))
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sess, _ := store.NewSession()
				for j := 0; j < size; j++ {
					keys[j][4] = byte(i % 256)
					keys[j][5] = byte(j % 256)
				}
				_, err := jmt.UpdateWithSession(sess, keys, values, Version(i+1))
				if err != nil {
					b.Fatalf("Serial update failed: %v", err)
				}
				sess.Commit()
				sess.Close()
			}
		})

		b.Run(fmt.Sprintf("Parallel-%d", size), func(b *testing.B) {
			tmpDir, _ := os.MkdirTemp("", "jmt_parallel")
			defer os.RemoveAll(tmpDir)

			cfg_default := config.DefaultConfig()
			opts := badger.DefaultOptions(tmpDir).WithLogger(nil).WithSyncWrites(false)
			opts.MaxTableSize = cfg_default.Database.BaseTableSize
			db, _ := badger.Open(opts)
			defer db.Close()

			store := NewVersionedBadgerStore(db, []byte("jmt:"))
			jmt := NewJMT(store, sha256.New())

			keys := make([][]byte, size)
			values := make([][]byte, size)
			for i := 0; i < size; i++ {
				keys[i] = []byte(fmt.Sprintf("key-%08d", i))
				values[i] = []byte(fmt.Sprintf("value-%08d", i))
			}

			cfg := DefaultParallelConfig()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sess, _ := store.NewSession()
				for j := 0; j < size; j++ {
					keys[j][4] = byte(i % 256)
					keys[j][5] = byte(j % 256)
				}
				_, err := jmt.ParallelUpdate(sess, keys, values, Version(i+1), cfg)
				if err != nil {
					b.Fatalf("Parallel update failed: %v", err)
				}
				sess.Commit()
				sess.Close()
			}
		})
	}
}

// BenchmarkParallelVsSerialThroughput 测试吞吐量对比
func BenchmarkParallelVsSerialThroughput(b *testing.B) {
	const batchSize = 1000

	b.Run("Serial", func(b *testing.B) {
		tmpDir, _ := os.MkdirTemp("", "jmt_tp_serial")
		defer os.RemoveAll(tmpDir)

		opts := badger.DefaultOptions(tmpDir).WithLogger(nil).WithSyncWrites(false)
		db, _ := badger.Open(opts)
		defer db.Close()

		store := NewVersionedBadgerStore(db, []byte("jmt:"))
		jmt := NewJMT(store, sha256.New())

		keys := make([][]byte, batchSize)
		values := make([][]byte, batchSize)
		for i := 0; i < batchSize; i++ {
			keys[i] = []byte(fmt.Sprintf("key-%08d", i))
			values[i] = []byte(fmt.Sprintf("value-%08d", i))
		}

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			sess, _ := store.NewSession()
			for j := 0; j < batchSize; j++ {
				keys[j][4] = byte(i % 256)
			}
			jmt.UpdateWithSession(sess, keys, values, Version(i+1))
			sess.Commit()
			sess.Close()
		}

		// 每次迭代处理 batchSize 个更新
		b.ReportMetric(float64(batchSize), "updates/op")
	})

	b.Run("Parallel", func(b *testing.B) {
		tmpDir, _ := os.MkdirTemp("", "jmt_tp_parallel")
		defer os.RemoveAll(tmpDir)

		opts := badger.DefaultOptions(tmpDir).WithLogger(nil).WithSyncWrites(false)
		db, _ := badger.Open(opts)
		defer db.Close()

		store := NewVersionedBadgerStore(db, []byte("jmt:"))
		jmt := NewJMT(store, sha256.New())

		keys := make([][]byte, batchSize)
		values := make([][]byte, batchSize)
		for i := 0; i < batchSize; i++ {
			keys[i] = []byte(fmt.Sprintf("key-%08d", i))
			values[i] = []byte(fmt.Sprintf("value-%08d", i))
		}

		cfg := DefaultParallelConfig()

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			sess, _ := store.NewSession()
			for j := 0; j < batchSize; j++ {
				keys[j][4] = byte(i % 256)
			}
			jmt.ParallelUpdate(sess, keys, values, Version(i+1), cfg)
			sess.Commit()
			sess.Close()
		}

		b.ReportMetric(float64(batchSize), "updates/op")
	})
}

// TestParallelUpdateCorrectness 验证并行更新的正确性
func TestParallelUpdateCorrectness(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "jmt_correctness")
	defer os.RemoveAll(tmpDir)

	opts := badger.DefaultOptions(tmpDir).WithLogger(nil).WithSyncWrites(false)
	db, _ := badger.Open(opts)
	defer db.Close()

	const batchSize = 500

	// 创建两个独立的 JMT 实例
	store1 := NewVersionedBadgerStore(db, []byte("serial:"))
	jmt1 := NewJMT(store1, sha256.New())

	store2 := NewVersionedBadgerStore(db, []byte("parallel:"))
	jmt2 := NewJMT(store2, sha256.New())

	// 准备相同的数据
	keys := make([][]byte, batchSize)
	values := make([][]byte, batchSize)
	for i := 0; i < batchSize; i++ {
		keys[i] = []byte(fmt.Sprintf("key-%08d", i))
		values[i] = []byte(fmt.Sprintf("value-%08d", i))
	}

	// 串行更新
	sess1, _ := store1.NewSession()
	root1, err := jmt1.UpdateWithSession(sess1, keys, values, 1)
	if err != nil {
		t.Fatalf("Serial update failed: %v", err)
	}
	sess1.Commit()
	sess1.Close()

	// 并行更新
	sess2, _ := store2.NewSession()
	cfg := DefaultParallelConfig()
	cfg.MinBatchForParallel = 1 // 强制使用并行路径
	root2, err := jmt2.ParallelUpdate(sess2, keys, values, 1, cfg)
	if err != nil {
		t.Fatalf("Parallel update failed: %v", err)
	}
	sess2.Commit()
	sess2.Close()

	// 验证根哈希一致
	if !bytesEqual(root1, root2) {
		t.Errorf("Root hash mismatch!\nSerial:   %x\nParallel: %x", root1, root2)
	} else {
		t.Logf("Root hash matches: %x", root1)
	}

	// 验证所有 Key 的值一致
	for i := 0; i < batchSize; i++ {
		val1, err1 := jmt1.Get(keys[i], 0)
		val2, err2 := jmt2.Get(keys[i], 0)

		if err1 != nil || err2 != nil {
			t.Errorf("Get failed for key %d: serial=%v, parallel=%v", i, err1, err2)
			continue
		}

		if !bytesEqual(val1, val2) {
			t.Errorf("Value mismatch for key %d", i)
		}
	}
}
