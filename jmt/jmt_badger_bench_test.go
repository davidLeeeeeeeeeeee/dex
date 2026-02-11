package smt

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"

	"github.com/dgraph-io/badger/v2"
)

// BenchmarkJMTWithBadger 对比真实 BadgerDB 环境下使用 Session 和不使用 Session 的性能
func BenchmarkJMTWithBadger(b *testing.B) {
	// 创建临时目录
	tmpDir, _ := os.MkdirTemp("", "jmt_badger_bench")
	defer os.RemoveAll(tmpDir)

	opts := badger.DefaultOptions(tmpDir).WithLogger(nil).WithSyncWrites(false)
	db, _ := badger.Open(opts)
	defer db.Close()

	prefix := []byte("jmt:")
	store := NewVersionedBadgerStore(db, prefix)
	jmt := NewJMT(store, sha256.New())

	const batchSize = 100
	keys := make([][]byte, batchSize)
	values := make([][]byte, batchSize)
	for i := 0; i < batchSize; i++ {
		keys[i] = []byte(fmt.Sprintf("key-%08d", i))
		values[i] = []byte(fmt.Sprintf("value-%08d", i))
	}

	b.Run("WithoutSession", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			// 模拟优化前：对于每一个 key，可能都要触发一次 JMT 更新和存储写入
			// 这里我们简化为每 batchSize 个 key 更新一次树，但使用非 Session 的 store.Set
			for j := 0; j < batchSize; j++ {
				store.Set(keys[j], values[j], Version(i+1))
			}
			jmt.Update(keys, values, Version(i+1))
		}
	})

	b.Run("WithSession", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			// 模拟优化后：使用 Session 合并 Badger 事务
			sess, err := store.NewSession()
			if err != nil {
				b.Fatalf("NewSession failed: %v", err)
			}
			// 在同一个事务中更新多个节点
			_, err = jmt.UpdateWithSession(sess, keys, values, Version(i+1))
			if err != nil {
				b.Fatalf("UpdateWithSession failed: %v", err)
			}
			sess.Commit()
			sess.Close()
		}
	})
}

// BenchmarkJMTStateDB_ApplyUpdate 测试集成后的 JMTStateDB 性能
func BenchmarkJMTStateDB_ApplyUpdate(b *testing.B) {
	tmpDir, _ := os.MkdirTemp("", "jmt_statedb_bench")
	defer os.RemoveAll(tmpDir)

	stateDB, _ := NewJMTStateDB(JMTConfig{DataDir: tmpDir})
	defer stateDB.Close()

	const batchSize = 100
	updates := make([]KVUpdate, batchSize)
	for i := 0; i < batchSize; i++ {
		updates[i] = KVUpdate{
			Key:   fmt.Sprintf("v1_account_%d", i),
			Value: []byte(fmt.Sprintf(`{"balance":%d}`, i*1000)),
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stateDB.ApplyAccountUpdate(uint64(i+1), updates...)
	}
}
