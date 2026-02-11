package smt

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"

	"github.com/dgraph-io/badger/v2"
)

// BenchmarkJMTWithBadgerHyperBatch 测试大批次下的性能表现
func BenchmarkJMTWithBadgerHyperBatch(b *testing.B) {
	// 创建临时目录
	tmpDir, _ := os.MkdirTemp("", "jmt_badger_hyper")
	defer os.RemoveAll(tmpDir)

	opts := badger.DefaultOptions(tmpDir).
		WithLogger(nil).
		WithSyncWrites(false)

	db, _ := badger.Open(opts)
	defer db.Close()

	prefix := []byte("jmt:")
	store := NewVersionedBadgerStore(db, prefix)
	jmt := NewJMT(store, sha256.New())

	// 测试不同的 batchSize
	batchSizes := []int{100, 500, 1000, 2000}

	for _, size := range batchSizes {
		b.Run(fmt.Sprintf("Batch-%d", size), func(b *testing.B) {
			keys := make([][]byte, size)
			values := make([][]byte, size)
			for i := 0; i < size; i++ {
				keys[i] = []byte(fmt.Sprintf("key-%08d", i))
				values[i] = []byte(fmt.Sprintf("value-%08d", i))
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// 对于较大的 Batch，我们可以手动拆分事务，或者只依赖 Session
				// 这里我们重点看单位 update 的耗时变化
				sess, _ := store.NewSession()
				for j := 0; j < size; j++ {
					// 这里的随机化很重要，否则 JMT 会变成简单的链式结构
					keys[j][4] = byte(i % 256)
					keys[j][5] = byte(j % 256)
				}
				_, err := jmt.UpdateWithSession(sess, keys, values, Version(i+1))
				if err != nil {
					b.Fatalf("Update size=%d failed: %v", size, err)
				}
				err = sess.Commit()
				// 如果 Badger 限制了事务大小，这里会报错，说明我们需要更细粒度的提交
				if err != nil {
					b.Fatalf("Commit size=%d failed: %v", size, err)
				}
				sess.Close()
			}
		})
	}
}
