package vm_test

import (
	"fmt"
	"sync"
	"testing"

	"dex/pb"
	"dex/vm"
)

// ========== Mock数据库实现 ==========

type MockDB struct {
	mu      sync.RWMutex
	data    map[string][]byte
	pending []func()
}

func NewMockDB() *MockDB {
	return &MockDB{
		data:    make(map[string][]byte),
		pending: make([]func(), 0),
	}
}

func (db *MockDB) Get(key string) ([]byte, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	val, exists := db.data[key]
	if !exists {
		return nil, nil
	}
	return val, nil
}

func (db *MockDB) EnqueueSet(key, value string) {
	db.pending = append(db.pending, func() {
		db.mu.Lock()
		defer db.mu.Unlock()
		db.data[key] = []byte(value)
	})
}

func (db *MockDB) EnqueueDel(key string) {
	db.pending = append(db.pending, func() {
		db.mu.Lock()
		defer db.mu.Unlock()
		delete(db.data, key)
	})
}

func (db *MockDB) ForceFlush() error {
	for _, op := range db.pending {
		op()
	}
	db.pending = db.pending[:0]
	return nil
}

// ========== 测试用例 ==========

func TestBasicExecution(t *testing.T) {
	// 创建数据库和组件
	db := NewMockDB()
	db.data["balance_alice_token123"] = []byte("1000")
	db.data["balance_bob_token123"] = []byte("500")

	// 创建执行器
	registry := vm.NewHandlerRegistry()
	if err := vm.RegisterDefaultHandlers(registry); err != nil {
		t.Fatal(err)
	}

	cache := vm.NewSpecExecLRU(100)
	executor := vm.NewExecutor(db, registry, cache)

	// 创建pb.AnyTx交易
	transferTx := &pb.AnyTx{
		Content: &pb.AnyTx_Transaction{
			Transaction: &pb.Transaction{
				Base: &pb.BaseMessage{
					TxId:        "tx_001",
					FromAddress: "alice",
					Status:      pb.Status_PENDING,
				},
				To:           "bob",
				TokenAddress: "token123",
				Amount:       "100",
			},
		},
	}

	// 创建pb.Block区块
	block := &pb.Block{
		BlockHash:     "block_001",
		PrevBlockHash: "genesis",
		Height:        1,
		Body:          []*pb.AnyTx{transferTx},
	}

	// 预执行区块
	result, err := executor.PreExecuteBlock(block)
	if err != nil {
		t.Fatal("PreExecute failed:", err)
	}

	if !result.Valid {
		t.Fatal("Block should be valid")
	}

	if len(result.Receipts) != 1 {
		t.Fatal("Should have 1 receipt")
	}

	// 检查数据库没有变化（预执行不应改变数据库）
	if val, _ := db.Get("balance_alice_token123"); string(val) != "1000" {
		t.Fatal("Database should not change during PreExecute")
	}

	// 最终提交
	err = executor.CommitFinalizedBlock(block)
	if err != nil {
		t.Fatal("Commit failed:", err)
	}

	// 检查交易状态
	status, _ := executor.GetTransactionStatus("tx_001")
	if status != "SUCCEED" {
		t.Fatal("Transaction should succeed")
	}

	// 检查区块提交状态
	committed, blockID := executor.IsBlockCommitted(1)
	if !committed || blockID != "block_001" {
		t.Fatal("Block should be committed")
	}
}

func TestCacheEffectiveness(t *testing.T) {
	db := NewMockDB()
	db.data["balance_alice_token123"] = []byte("1000")

	registry := vm.NewHandlerRegistry()
	vm.RegisterDefaultHandlers(registry)

	cache := vm.NewSpecExecLRU(10)
	executor := vm.NewExecutor(db, registry, cache)

	// 创建pb.Block区块
	transferTx := &pb.AnyTx{
		Content: &pb.AnyTx_Transaction{
			Transaction: &pb.Transaction{
				Base: &pb.BaseMessage{
					TxId:        "tx_cache_001",
					FromAddress: "alice",
					Status:      pb.Status_PENDING,
				},
				To:           "bob",
				TokenAddress: "token123",
				Amount:       "100",
			},
		},
	}

	block := &pb.Block{
		BlockHash:     "block_cache_test",
		PrevBlockHash: "genesis",
		Height:        1,
		Body:          []*pb.AnyTx{transferTx},
	}

	// 第一次执行
	result1, err := executor.PreExecuteBlock(block)
	if err != nil {
		t.Fatal(err)
	}

	// 第二次执行（应该从缓存获取）
	result2, err := executor.PreExecuteBlock(block)
	if err != nil {
		t.Fatal(err)
	}

	// 验证结果一致
	if result1.BlockID != result2.BlockID {
		t.Fatal("Cache returned different result")
	}
}

func TestInvalidTransaction(t *testing.T) {
	db := NewMockDB()
	// 没有设置账户余额，交易应该失败

	registry := vm.NewHandlerRegistry()
	vm.RegisterDefaultHandlers(registry)

	cache := vm.NewSpecExecLRU(10)
	executor := vm.NewExecutor(db, registry, cache)

	// 创建包含无效交易的pb.Block区块
	transferTx := &pb.AnyTx{
		Content: &pb.AnyTx_Transaction{
			Transaction: &pb.Transaction{
				Base: &pb.BaseMessage{
					TxId:        "tx_invalid_001",
					FromAddress: "alice",
					Status:      pb.Status_PENDING,
				},
				To:           "bob",
				TokenAddress: "token123",
				Amount:       "200",
			},
		},
	}

	block := &pb.Block{
		BlockHash:     "block_invalid",
		PrevBlockHash: "genesis",
		Height:        2,
		Body:          []*pb.AnyTx{transferTx},
	}

	// 预执行
	result, _ := executor.PreExecuteBlock(block)

	if result.Valid {
		t.Fatal("Block with invalid transaction should be invalid")
	}
}

func TestConcurrentExecution(t *testing.T) {
	db := NewMockDB()
	registry := vm.NewHandlerRegistry()
	vm.RegisterDefaultHandlers(registry)

	cache := vm.NewSpecExecLRU(100)
	executor := vm.NewExecutor(db, registry, cache)

	// 初始化多个账户
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("balance_user%d_token123", i)
		db.data[key] = []byte("1000")
	}

	// 并发执行多个区块
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(blockNum int) {
			defer wg.Done()

			// 创建pb.AnyTx交易列表
			txs := make([]*pb.AnyTx, 0)
			for j := 0; j < 5; j++ {
				from := fmt.Sprintf("user%d", j)
				to := fmt.Sprintf("user%d", (j+1)%10)

				tx := &pb.AnyTx{
					Content: &pb.AnyTx_Transaction{
						Transaction: &pb.Transaction{
							Base: &pb.BaseMessage{
								TxId:        fmt.Sprintf("tx_%d_%d", blockNum, j),
								FromAddress: from,
								Status:      pb.Status_PENDING,
							},
							To:           to,
							TokenAddress: "token123",
							Amount:       "10",
						},
					},
				}
				txs = append(txs, tx)
			}

			block := &pb.Block{
				BlockHash:     fmt.Sprintf("block_%d", blockNum),
				PrevBlockHash: "genesis",
				Height:        uint64(blockNum + 1),
				Body:          txs,
			}

			// 预执行
			result, err := executor.PreExecuteBlock(block)
			if err != nil {
				t.Errorf("Block %d execution error: %v", blockNum, err)
				return
			}

			if !result.Valid {
				t.Errorf("Block %d should be valid", blockNum)
			}
		}(i)
	}

	wg.Wait()
}

func TestStateViewSnapshot(t *testing.T) {
	// 创建StateView
	readFn := func(key string) ([]byte, error) {
		if key == "test_key" {
			return []byte("initial_value"), nil
		}
		return nil, nil
	}

	sv := vm.NewStateView(readFn)

	// 初始状态
	val, exists, _ := sv.Get("test_key")
	if !exists || string(val) != "initial_value" {
		t.Fatal("Initial value incorrect")
	}

	// 创建快照
	snap1 := sv.Snapshot()

	// 修改状态
	sv.Set("test_key", []byte("modified_value"))
	sv.Set("new_key", []byte("new_value"))

	// 验证修改
	val, exists, _ = sv.Get("test_key")
	if string(val) != "modified_value" {
		t.Fatal("Modified value incorrect")
	}

	// 回滚到快照
	err := sv.Revert(snap1)
	if err != nil {
		t.Fatal("Revert failed:", err)
	}

	// 验证回滚
	val, exists, _ = sv.Get("test_key")
	if !exists || string(val) != "initial_value" {
		t.Fatal("Reverted value incorrect")
	}

	val, exists, _ = sv.Get("new_key")
	if exists {
		t.Fatal("New key should not exist after revert")
	}
}

func TestLRUCache(t *testing.T) {
	cache := vm.NewSpecExecLRU(3)

	// 添加项目
	for i := 0; i < 5; i++ {
		cache.Put(&vm.SpecResult{
			BlockID: fmt.Sprintf("block_%d", i),
			Height:  uint64(i),
			Valid:   true,
		})
	}

	// 检查最早的项目被驱逐
	if _, ok := cache.Get("block_0"); ok {
		t.Fatal("block_0 should be evicted")
	}

	if _, ok := cache.Get("block_1"); ok {
		t.Fatal("block_1 should be evicted")
	}

	// 最近的项目应该还在
	if _, ok := cache.Get("block_4"); !ok {
		t.Fatal("block_4 should be in cache")
	}

	// 测试EvictBelow
	cache.EvictBelow(4)

	if _, ok := cache.Get("block_2"); ok {
		t.Fatal("block_2 should be evicted")
	}

	if _, ok := cache.Get("block_3"); ok {
		t.Fatal("block_3 should be evicted")
	}

	if _, ok := cache.Get("block_4"); !ok {
		t.Fatal("block_4 should remain in cache")
	}
}

func BenchmarkPreExecute(b *testing.B) {
	db := NewMockDB()
	registry := vm.NewHandlerRegistry()
	vm.RegisterDefaultHandlers(registry)
	cache := vm.NewSpecExecLRU(1000)
	executor := vm.NewExecutor(db, registry, cache)

	// 准备数据
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("balance_user%d_token123", i)
		db.data[key] = []byte("10000")
	}

	// 创建测试区块
	txs := make([]*pb.AnyTx, 100)
	for i := 0; i < 100; i++ {
		txs[i] = &pb.AnyTx{
			Content: &pb.AnyTx_Transaction{
				Transaction: &pb.Transaction{
					Base: &pb.BaseMessage{
						TxId:        fmt.Sprintf("tx_%d", i),
						FromAddress: fmt.Sprintf("user%d", i%100),
						Status:      pb.Status_PENDING,
					},
					To:           fmt.Sprintf("user%d", (i+1)%100),
					TokenAddress: "token123",
					Amount:       "1",
				},
			},
		}
	}

	block := &pb.Block{
		BlockHash:     "bench_block",
		PrevBlockHash: "genesis",
		Height:        1,
		Body:          txs,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		executor.PreExecuteBlock(block)
	}
}
