package smt

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"

	"github.com/dgraph-io/badger/v4"
)

// BenchmarkTransactionOverhead 展示真实 BadgerDB 存储下的 JMT 表现
func BenchmarkBigDataBadgerOverhead(b *testing.B) {
	// 1. 准备真实的物理存储
	dir, err := os.MkdirTemp("", "badger_bench_real")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(dir)

	opts := badger.DefaultOptions(dir).WithLoggingLevel(badger.ERROR)
	db, err := badger.Open(opts)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	// 2. 初始化适配器和 JMT
	store := NewVersionedBadgerStore(db, []byte("jmt:"))
	jmt := NewJMT(store, sha256.New())

	// 3. 预填 1000 个 Key，使 JMT 产生 3-4 层的深度
	// 模拟你运行到高度 10 时，订单状态在树中已经处于较深的分叉位置
	count := 1000
	keys := make([][]byte, count)
	values := make([][]byte, count)
	for i := 0; i < count; i++ {
		keys[i] = []byte(fmt.Sprintf("order-id-%08d", i))
		values[i] = []byte(fmt.Sprintf("order-data-payload-%08d", i))
	}
	_, err = jmt.Update(keys, values, 1)
	if err != nil {
		b.Fatal(err)
	}

	// 测试 A: 内存 Map 表现 (作为对比参考线)
	b.Run("Memory_Map_Get_Baseline", func(b *testing.B) {
		memStore := NewSimpleVersionedMap()
		memJMT := NewJMT(memStore, sha256.New())
		_, _ = memJMT.Update(keys, values, 1)

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			key := keys[i%count]
			_, _ = memJMT.Get(key, 0)
		}
	})

	// 测试 B: 真实的 JMT Get 操作 (反映现有的问题)
	// 这个操作会：1. 开启 db.View 事务 2. 创建迭代器 3. 逐层寻找子节点
	b.Run("Badger_JMT_Get_Real_Path", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			key := keys[i%count]
			// 在你的 pprof 中，这里每层都会开个 db.View 
			// 由于 JMT 是递归 Get 的，这会爆炸
			_, _ = jmt.Get(key, 0)
		}
	})

	// 测试 C: 模拟高并发下的争抢 (卡死的真实原因)
	b.Run("Badger_JMT_Get_Concurrent_10", func(b *testing.B) {
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				key := keys[i%count]
				_, _ = jmt.Get(key, 0)
				i++
			}
		})
	})
}