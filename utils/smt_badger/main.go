package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/celestiaorg/smt"
	badger "github.com/dgraph-io/badger/v4"
)

// BadgerMapStore 实现 SMT 的 MapStore 接口，使用 Badger v4 作为底层存储
type BadgerMapStore struct {
	db    *badger.DB
	cache map[string][]byte // 内存缓存层
	mu    sync.RWMutex      // 缓存读写锁
}

// NewBadgerMapStore 创建新的 Badger 存储实例
func NewBadgerMapStore(path string) (*BadgerMapStore, error) {
	// Badger v4 配置优化
	opts := badger.DefaultOptions(path)
	opts.Logger = nil // 关闭日志以提高性能

	// 性能优化配置
	opts.MemTableSize = 128 << 20 // 128 MB
	opts.BaseTableSize = 2 << 20  // 2 MB
	opts.BaseLevelSize = 10 << 20 // 10 MB
	opts.NumLevelZeroTables = 5
	opts.NumLevelZeroTablesStall = 10
	opts.ValueLogFileSize = 256 << 20 // 256 MB
	opts.ValueLogMaxEntries = 1000000
	opts.NumCompactors = 4 // 并发压缩
	opts.NumMemtables = 5
	opts.BloomFalsePositive = 0.01
	opts.BlockCacheSize = 256 << 20 // 256 MB block cache
	opts.IndexCacheSize = 100 << 20 // 100 MB index cache

	// 打开数据库
	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}

	return &BadgerMapStore{
		db:    db,
		cache: make(map[string][]byte, 10000), // 预分配缓存
	}, nil
}

// Get 实现 MapStore 的 Get 方法
func (b *BadgerMapStore) Get(key []byte) ([]byte, error) {
	// 先检查缓存
	b.mu.RLock()
	if val, ok := b.cache[string(key)]; ok {
		b.mu.RUnlock()
		return val, nil
	}
	b.mu.RUnlock()

	// 从 Badger 读取
	var result []byte
	err := b.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			if err == badger.ErrKeyNotFound {
				return nil // 返回 nil 表示不存在
			}
			return err
		}

		result, err = item.ValueCopy(nil)
		return err
	})

	if err != nil {
		return nil, err
	}

	// 更新缓存
	if result != nil && len(b.cache) < 50000 { // 限制缓存大小
		b.mu.Lock()
		b.cache[string(key)] = result
		b.mu.Unlock()
	}

	return result, nil
}

// Set 实现 MapStore 的 Set 方法
func (b *BadgerMapStore) Set(key []byte, value []byte) error {
	// 更新缓存
	b.mu.Lock()
	b.cache[string(key)] = value
	b.mu.Unlock()

	// 异步写入 Badger（提高性能）
	return b.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, value)
	})
}

// Delete 实现 MapStore 的 Delete 方法
func (b *BadgerMapStore) Delete(key []byte) error {
	// 从缓存删除
	b.mu.Lock()
	delete(b.cache, string(key))
	b.mu.Unlock()

	// 从 Badger 删除
	return b.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(key)
	})
}

// BatchSet 批量设置，提高性能
func (b *BadgerMapStore) BatchSet(kvPairs map[string][]byte) error {
	// 批量更新缓存
	b.mu.Lock()
	for k, v := range kvPairs {
		b.cache[k] = v
	}
	b.mu.Unlock()

	// 批量写入 Badger
	batch := b.db.NewWriteBatch()
	defer batch.Cancel()

	for k, v := range kvPairs {
		err := batch.Set([]byte(k), v)
		if err != nil {
			return err
		}
	}

	return batch.Flush()
}

// Close 关闭数据库
func (b *BadgerMapStore) Close() error {
	return b.db.Close()
}

// 生成测试数据
func generateTestData(count int) ([][]byte, [][]byte) {
	keys := make([][]byte, count)
	values := make([][]byte, count)

	for i := 0; i < count; i++ {
		// 生成 32 字节的 key
		key := make([]byte, 32)
		rand.Read(key)
		keys[i] = key

		// 生成 100 字节的 value
		value := make([]byte, 100)
		rand.Read(value)
		values[i] = value
	}

	return keys, values
}

// 性能测试函数
func performanceTest(store *BadgerMapStore, testSize int) {
	fmt.Printf("\n=== SMT + Badger v4 性能测试 ===\n")
	fmt.Printf("测试节点数量: %d\n", testSize)
	fmt.Printf("CPU 核心数: %d\n", runtime.NumCPU())

	// 创建 SMT
	tree := smt.NewSparseMerkleTree(store, smt.NewSimpleMap(), sha256.New())

	// 生成测试数据
	fmt.Printf("\n生成测试数据...\n")
	startGen := time.Now()
	keys, values := generateTestData(testSize)
	fmt.Printf("数据生成耗时: %v\n", time.Since(startGen))

	// 测试单个更新
	fmt.Printf("\n--- 单个更新测试 ---\n")
	singleStart := time.Now()
	for i := 0; i < min(1000, testSize); i++ {
		_, err := tree.Update(keys[i], values[i])
		if err != nil {
			log.Printf("更新错误: %v", err)
		}
	}
	singleDuration := time.Since(singleStart)
	fmt.Printf("前 1000 个单独更新耗时: %v\n", singleDuration)
	fmt.Printf("平均每个更新: %v\n", singleDuration/1000)

	// 测试批量更新（使用剩余的数据）
	fmt.Printf("\n--- 批量更新测试 ---\n")
	batchSize := 1000
	totalBatches := (testSize - 1000) / batchSize

	batchStart := time.Now()
	for batch := 0; batch < totalBatches && (batch+1)*batchSize+1000 < testSize; batch++ {
		startIdx := batch*batchSize + 1000
		endIdx := min(startIdx+batchSize, testSize)

		// 批量准备数据
		kvPairs := make(map[string][]byte)
		for i := startIdx; i < endIdx; i++ {
			kvPairs[string(keys[i])] = values[i]
		}

		// 批量写入存储
		err := store.BatchSet(kvPairs)
		if err != nil {
			log.Printf("批量写入错误: %v", err)
		}

		// 更新 SMT
		for i := startIdx; i < endIdx; i++ {
			_, err := tree.Update(keys[i], values[i])
			if err != nil {
				log.Printf("SMT 更新错误: %v", err)
			}
		}

		if batch%10 == 0 {
			fmt.Printf("已完成 %d/%d 批次\n", batch, totalBatches)
		}
	}
	batchDuration := time.Since(batchStart)
	fmt.Printf("批量更新耗时: %v\n", batchDuration)
	fmt.Printf("平均每批次 (%d 个节点): %v\n", batchSize, batchDuration/time.Duration(totalBatches))

	// 总计
	fmt.Printf("\n--- 总体统计 ---\n")
	totalDuration := singleDuration + batchDuration
	fmt.Printf("总更新耗时: %v\n", totalDuration)
	fmt.Printf("平均每个节点: %v\n", totalDuration/time.Duration(testSize))
	fmt.Printf("TPS (每秒更新数): %.2f\n", float64(testSize)/totalDuration.Seconds())

	// 测试读取性能
	fmt.Printf("\n--- 读取性能测试 ---\n")
	readStart := time.Now()
	readCount := min(10000, testSize)
	for i := 0; i < readCount; i++ {
		val, err := tree.Get(keys[i])
		if err != nil {
			log.Printf("读取错误: %v", err)
		}
		if val == nil {
			log.Printf("未找到 key: %x", keys[i])
		}
	}
	readDuration := time.Since(readStart)
	fmt.Printf("读取 %d 个节点耗时: %v\n", readCount, readDuration)
	fmt.Printf("平均每次读取: %v\n", readDuration/time.Duration(readCount))
	fmt.Printf("读取 QPS: %.2f\n", float64(readCount)/readDuration.Seconds())

	// 获取和验证根哈希
	fmt.Printf("\n--- 根哈希计算 ---\n")
	rootStart := time.Now()
	root := tree.Root()
	fmt.Printf("根哈希计算耗时: %v\n", time.Since(rootStart))
	fmt.Printf("根哈希: %x\n", root)

	// 测试证明生成
	fmt.Printf("\n--- Merkle 证明测试 ---\n")
	proofStart := time.Now()
	proof, err := tree.Prove(keys[0])
	if err != nil {
		log.Printf("生成证明错误: %v", err)
	}
	fmt.Printf("生成证明耗时: %v\n", time.Since(proofStart))

	// 验证证明
	verifyStart := time.Now()
	verified := smt.VerifyProof(proof, root, keys[0], values[0], sha256.New())
	fmt.Printf("验证证明耗时: %v\n", time.Since(verifyStart))
	fmt.Printf("证明验证结果: %v\n", verified)

	// 打印存储统计
	fmt.Printf("\n--- 存储统计 ---\n")
	lsm, vlog := store.db.Size()
	fmt.Printf("LSM 大小: %.2f MB\n", float64(lsm)/1024/1024)
	fmt.Printf("ValueLog 大小: %.2f MB\n", float64(vlog)/1024/1024)
	fmt.Printf("总存储大小: %.2f MB\n", float64(lsm+vlog)/1024/1024)
	fmt.Printf("缓存条目数: %d\n", len(store.cache))

	// 内存统计
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Printf("\n--- 内存统计 ---\n")
	fmt.Printf("分配的内存: %.2f MB\n", float64(m.Alloc)/1024/1024)
	fmt.Printf("累计分配: %.2f MB\n", float64(m.TotalAlloc)/1024/1024)
	fmt.Printf("系统内存: %.2f MB\n", float64(m.Sys)/1024/1024)
	fmt.Printf("GC 次数: %d\n", m.NumGC)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func main() {
	// 创建测试目录
	dbPath := "./badger-smt-test"
	os.RemoveAll(dbPath)       // 清理旧数据
	defer os.RemoveAll(dbPath) // 测试后清理

	// 创建 Badger 存储
	store, err := NewBadgerMapStore(dbPath)
	if err != nil {
		log.Fatalf("创建 Badger 存储失败: %v", err)
	}
	defer store.Close()

	// 运行性能测试
	testSize := 100000 // 10万节点
	performanceTest(store, testSize)

	// 额外测试：并发更新
	fmt.Printf("\n=== 并发更新测试 ===\n")
	concurrentTest(store)
}

// 并发更新测试
func concurrentTest(store *BadgerMapStore) {
	tree := smt.NewSparseMerkleTree(store, smt.NewSimpleMap(), sha256.New())

	concurrency := runtime.NumCPU()
	updatesPerGoroutine := 1000
	totalUpdates := concurrency * updatesPerGoroutine

	var wg sync.WaitGroup
	var tmu sync.Mutex // 关键：保护同一棵树的并发访问

	start := time.Now()

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < updatesPerGoroutine; j++ {
				key := make([]byte, 32)
				value := make([]byte, 100)
				binary.BigEndian.PutUint64(key, uint64(id))
				binary.BigEndian.PutUint64(key[8:], uint64(j))
				rand.Read(key[16:])
				rand.Read(value)

				tmu.Lock()
				_, err := tree.Update(key, value)
				tmu.Unlock()
				if err != nil {
					log.Printf("Goroutine %d 更新错误: %v", id, err)
				}
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)
	fmt.Printf("并发更新总耗时: %v\n", duration)
	fmt.Printf("并发 TPS: %.2f\n", float64(totalUpdates)/duration.Seconds())
}
