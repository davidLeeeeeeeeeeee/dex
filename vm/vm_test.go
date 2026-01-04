package vm_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"dex/keys"
	"dex/pb"
	"dex/vm"

	"github.com/stretchr/testify/assert"
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

func (db *MockDB) Scan(prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)

	db.mu.RLock()
	defer db.mu.RUnlock()
	for k, v := range db.data {
		if strings.HasPrefix(k, prefix) {
			valCopy := make([]byte, len(v))
			copy(valCopy, v)
			result[k] = valCopy
		}
	}
	return result, nil
}

func (db *MockDB) ScanOrdersByPairs(pairs []string) (map[string]map[string][]byte, error) {
	result := make(map[string]map[string][]byte)

	db.mu.RLock()
	defer db.mu.RUnlock()

	for _, pair := range pairs {
		prefix := keys.KeyOrderPriceIndexPrefix(pair, false)
		pairMap := make(map[string][]byte)

		for k, v := range db.data {
			if strings.HasPrefix(k, prefix) {
				valCopy := make([]byte, len(v))
				copy(valCopy, v)
				pairMap[k] = valCopy
			}
		}
		result[pair] = pairMap
	}

	return result, nil
}

// ========== 测试用例 ==========

func TestBasicExecution(t *testing.T) {
	// 创建数据库和组件
	db := NewMockDB()

	// 初始化账户数据（使用新的 key 格式）
	aliceAccount := &pb.Account{
		Address: "alice",
		Balances: map[string]*pb.TokenBalance{
			"token123": {
				Balance:                "1000",
				MinerLockedBalance:     "0",
				CandidateLockedBalance: "0",
			},
		},
	}
	aliceData, _ := json.Marshal(aliceAccount)
	db.data[keys.KeyAccount("alice")] = aliceData

	bobAccount := &pb.Account{
		Address: "bob",
		Balances: map[string]*pb.TokenBalance{
			"token123": {
				Balance:                "500",
				MinerLockedBalance:     "0",
				CandidateLockedBalance: "0",
			},
		},
	}
	bobData, _ := json.Marshal(bobAccount)
	db.data[keys.KeyAccount("bob")] = bobData

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
	aliceKey := keys.KeyAccount("alice")
	val, _ := db.Get(aliceKey)
	var checkAccount pb.Account
	json.Unmarshal(val, &checkAccount)
	if checkAccount.Balances["token123"].Balance != "1000" {
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

	// 初始化账户数据（使用新的 key 格式）
	aliceAccount := &pb.Account{
		Address: "alice",
		Balances: map[string]*pb.TokenBalance{
			"token123": {
				Balance:                "1000",
				MinerLockedBalance:     "0",
				CandidateLockedBalance: "0",
			},
		},
	}
	aliceData, _ := json.Marshal(aliceAccount)
	db.data[keys.KeyAccount("alice")] = aliceData

	bobAccount := &pb.Account{
		Address: "bob",
		Balances: map[string]*pb.TokenBalance{
			"token123": {
				Balance:                "0",
				MinerLockedBalance:     "0",
				CandidateLockedBalance: "0",
			},
		},
	}
	bobData, _ := json.Marshal(bobAccount)
	db.data[keys.KeyAccount("bob")] = bobData

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

	// 初始化多个账户（使用新的 key 格式）
	for i := 0; i < 10; i++ {
		userAddr := fmt.Sprintf("user%d", i)
		account := &pb.Account{
			Address: userAddr,
			Balances: map[string]*pb.TokenBalance{
				"token123": {
					Balance:                "1000",
					MinerLockedBalance:     "0",
					CandidateLockedBalance: "0",
				},
			},
		}
		accountData, _ := json.Marshal(account)
		db.data[keys.KeyAccount(userAddr)] = accountData
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

	scanFn := func(prefix string) (map[string][]byte, error) {
		return make(map[string][]byte), nil
	}

	sv := vm.NewStateView(readFn, scanFn)

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

	// 准备数据（使用新的 key 格式）
	for i := 0; i < 100; i++ {
		userAddr := fmt.Sprintf("user%d", i)
		account := &pb.Account{
			Address: userAddr,
			Balances: map[string]*pb.TokenBalance{
				"token123": {
					Balance:                "10000",
					MinerLockedBalance:     "0",
					CandidateLockedBalance: "0",
				},
			},
		}
		accountData, _ := json.Marshal(account)
		db.data[keys.KeyAccount(userAddr)] = accountData
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

// TestFrostKind 测试 Frost 交易类型的 kind 字符串识别
func TestFrostKind(t *testing.T) {
	// 测试 FrostWithdrawRequestTx
	frostWithdrawRequestTx := &pb.AnyTx{
		Content: &pb.AnyTx_FrostWithdrawRequestTx{
			FrostWithdrawRequestTx: &pb.FrostWithdrawRequestTx{
				Base: &pb.BaseMessage{
					TxId:        "test-frost-withdraw-request-1",
					FromAddress: "alice",
				},
				Chain:  "BTC",
				Asset:  "native",
				To:     "bc1qtest...",
				Amount: "100000",
			},
		},
	}

	kind, err := vm.DefaultKindFn(frostWithdrawRequestTx)
	assert.NoError(t, err)
	assert.Equal(t, "frost_withdraw_request", kind)

	// 测试 FrostWithdrawSignedTx
	frostWithdrawSignedTx := &pb.AnyTx{
		Content: &pb.AnyTx_FrostWithdrawSignedTx{
			FrostWithdrawSignedTx: &pb.FrostWithdrawSignedTx{
				Base: &pb.BaseMessage{
					TxId:        "test-frost-withdraw-signed-1",
					FromAddress: "runtime",
				},
				JobId:              "job-123",
				SignedPackageBytes: []byte("signed-package"),
			},
		},
	}

	kind, err = vm.DefaultKindFn(frostWithdrawSignedTx)
	assert.NoError(t, err)
	assert.Equal(t, "frost_withdraw_signed", kind)

	// 测试 handler 注册
	registry := vm.NewHandlerRegistry()
	err = vm.RegisterDefaultHandlers(registry)
	assert.NoError(t, err)

	// 验证 Frost handler 已注册
	h1, ok := registry.Get("frost_withdraw_request")
	assert.True(t, ok)
	assert.Equal(t, "frost_withdraw_request", h1.Kind())

	h2, ok := registry.Get("frost_withdraw_signed")
	assert.True(t, ok)
	assert.Equal(t, "frost_withdraw_signed", h2.Kind())
}

// TestFrostWithdrawRequest 测试 FrostWithdrawRequest handler
func TestFrostWithdrawRequest(t *testing.T) {
	// 创建 StateView
	store := make(map[string][]byte)
	readFn := func(key string) ([]byte, error) {
		if v, ok := store[key]; ok {
			return v, nil
		}
		return nil, nil
	}
	scanFn := func(prefix string) (map[string][]byte, error) {
		return make(map[string][]byte), nil
	}

	handler := &vm.FrostWithdrawRequestTxHandler{}

	// 测试1：第一笔 withdraw request，seq 应该是 1
	sv := vm.NewStateView(readFn, scanFn)
	tx1 := &pb.AnyTx{
		Content: &pb.AnyTx_FrostWithdrawRequestTx{
			FrostWithdrawRequestTx: &pb.FrostWithdrawRequestTx{
				Base: &pb.BaseMessage{
					TxId:           "tx_001",
					FromAddress:    "alice",
					ExecutedHeight: 100,
				},
				Chain:  "BTC",
				Asset:  "native",
				To:     "bc1qtest001",
				Amount: "100000",
			},
		},
	}

	ops1, receipt1, err1 := handler.DryRun(tx1, sv)
	assert.NoError(t, err1)
	assert.Equal(t, "SUCCEED", receipt1.Status)
	assert.True(t, len(ops1) >= 4) // withdraw + fifo_index + seq + tx_ref

	// 应用写入到 store
	for _, op := range ops1 {
		store[op.Key] = op.Value
	}

	// 测试2：第二笔不同 tx_id，seq 应该递增到 2
	sv2 := vm.NewStateView(readFn, scanFn)
	tx2 := &pb.AnyTx{
		Content: &pb.AnyTx_FrostWithdrawRequestTx{
			FrostWithdrawRequestTx: &pb.FrostWithdrawRequestTx{
				Base: &pb.BaseMessage{
					TxId:           "tx_002",
					FromAddress:    "bob",
					ExecutedHeight: 101,
				},
				Chain:  "BTC",
				Asset:  "native",
				To:     "bc1qtest002",
				Amount: "200000",
			},
		},
	}

	ops2, receipt2, err2 := handler.DryRun(tx2, sv2)
	assert.NoError(t, err2)
	assert.Equal(t, "SUCCEED", receipt2.Status)
	assert.True(t, len(ops2) >= 4)

	// 应用写入到 store
	for _, op := range ops2 {
		store[op.Key] = op.Value
	}

	// 验证 seq 已递增（通过检查 FIFO index 存在性）
	seqKey := keys.KeyFrostWithdrawFIFOSeq("BTC", "native")
	seqData := store[seqKey]
	assert.Equal(t, "2", string(seqData))

	// 测试3：重复 tx_id 不递增 seq（幂等）
	sv3 := vm.NewStateView(readFn, scanFn)
	ops3, receipt3, err3 := handler.DryRun(tx1, sv3) // 重复 tx_001
	assert.NoError(t, err3)
	assert.Equal(t, "SUCCEED", receipt3.Status)
	assert.Equal(t, 0, receipt3.WriteCount) // 没有新的写入

	// seq 仍然是 2
	assert.Nil(t, ops3)
	seqData2 := store[seqKey]
	assert.Equal(t, "2", string(seqData2))
}

// TestFrostWithdrawSigned 测试 FrostWithdrawSigned handler
func TestFrostWithdrawSigned(t *testing.T) {
	store := make(map[string][]byte)
	readFn := func(key string) ([]byte, error) {
		if v, ok := store[key]; ok {
			return v, nil
		}
		return nil, nil
	}
	scanFn := func(prefix string) (map[string][]byte, error) {
		return make(map[string][]byte), nil
	}

	requestHandler := &vm.FrostWithdrawRequestTxHandler{}
	signedHandler := &vm.FrostWithdrawSignedTxHandler{}

	// 先创建一个 withdraw request
	sv1 := vm.NewStateView(readFn, scanFn)
	tx1 := &pb.AnyTx{
		Content: &pb.AnyTx_FrostWithdrawRequestTx{
			FrostWithdrawRequestTx: &pb.FrostWithdrawRequestTx{
				Base: &pb.BaseMessage{
					TxId:           "tx_req_001",
					FromAddress:    "alice",
					ExecutedHeight: 100,
				},
				Chain:  "ETH",
				Asset:  "USDT",
				To:     "0xabc123",
				Amount: "1000000",
			},
		},
	}

	ops1, _, _ := requestHandler.DryRun(tx1, sv1)
	for _, op := range ops1 {
		store[op.Key] = op.Value
	}

	// 获取 withdraw_id（从 tx_ref 读取）
	txRefKey := keys.KeyFrostWithdrawTxRef("tx_req_001")
	withdrawID := string(store[txRefKey])
	assert.NotEmpty(t, withdrawID)

	// 测试签名完成
	sv2 := vm.NewStateView(readFn, scanFn)
	signedTx := &pb.AnyTx{
		Content: &pb.AnyTx_FrostWithdrawSignedTx{
			FrostWithdrawSignedTx: &pb.FrostWithdrawSignedTx{
				Base: &pb.BaseMessage{
					TxId:           "tx_signed_001",
					FromAddress:    "coordinator",
					ExecutedHeight: 110,
				},
				JobId:              "job_001",
				SignedPackageBytes: []byte("signed_tx_data"),
				WithdrawIds:        []string{withdrawID},
			},
		},
	}

	ops2, receipt2, err2 := signedHandler.DryRun(signedTx, sv2)
	assert.NoError(t, err2)
	assert.Equal(t, "SUCCEED", receipt2.Status)
	assert.True(t, len(ops2) >= 3) // withdraw update + signed_pkg + count + head

	// 应用写入
	for _, op := range ops2 {
		store[op.Key] = op.Value
	}

	// 验证 withdraw 状态变为 SIGNED
	withdrawKey := keys.KeyFrostWithdraw(withdrawID)
	withdrawData := store[withdrawKey]
	assert.NotEmpty(t, withdrawData)

	// 测试重复提交只追加 receipt
	sv3 := vm.NewStateView(readFn, scanFn)
	signedTx2 := &pb.AnyTx{
		Content: &pb.AnyTx_FrostWithdrawSignedTx{
			FrostWithdrawSignedTx: &pb.FrostWithdrawSignedTx{
				Base: &pb.BaseMessage{
					TxId:           "tx_signed_002",
					FromAddress:    "coordinator2",
					ExecutedHeight: 111,
				},
				JobId:              "job_001", // 同一个 job
				SignedPackageBytes: []byte("signed_tx_data_v2"),
				WithdrawIds:        []string{withdrawID},
			},
		},
	}

	ops3, receipt3, err3 := signedHandler.DryRun(signedTx2, sv3)
	assert.NoError(t, err3)
	assert.Equal(t, "SUCCEED", receipt3.Status)
	// 只有 signed_pkg 和 count 两个写入（不再更新 withdraw 状态）
	assert.Equal(t, 2, len(ops3))

	// 验证 count 更新为 2
	for _, op := range ops3 {
		store[op.Key] = op.Value
	}
	countKey := keys.KeyFrostSignedPackageCount("job_001")
	countData := store[countKey]
	assert.Equal(t, "2", string(countData))
}

// TestFrostFundsLedger 测试 FundsLedger helper
func TestFrostFundsLedger(t *testing.T) {
	store := make(map[string][]byte)
	readFn := func(key string) ([]byte, error) {
		if v, ok := store[key]; ok {
			return v, nil
		}
		return nil, nil
	}
	scanFn := func(prefix string) (map[string][]byte, error) {
		return make(map[string][]byte), nil
	}

	chain := "ETH"
	asset := "native"
	vaultID := uint32(0)

	// 初始状态：没有任何 lot
	sv := vm.NewStateView(readFn, scanFn)
	head := vm.GetFundsLotHead(sv, chain, asset, vaultID)
	assert.Equal(t, uint64(0), head.Height)
	assert.Equal(t, uint64(0), head.Seq)

	// 模拟添加 lot（通过直接设置 seq 和 index）
	// height=100, seq=1
	seqKey1 := keys.KeyFrostFundsLotSeq(chain, asset, vaultID, 100)
	store[seqKey1] = []byte("2") // 2 个 lot
	lotKey1 := keys.KeyFrostFundsLotIndex(chain, asset, vaultID, 100, 1)
	store[lotKey1] = []byte("request_001")
	lotKey2 := keys.KeyFrostFundsLotIndex(chain, asset, vaultID, 100, 2)
	store[lotKey2] = []byte("request_002")

	// height=102, seq=1
	seqKey2 := keys.KeyFrostFundsLotSeq(chain, asset, vaultID, 102)
	store[seqKey2] = []byte("1")
	lotKey3 := keys.KeyFrostFundsLotIndex(chain, asset, vaultID, 102, 1)
	store[lotKey3] = []byte("request_003")

	// 设置初始头指针
	vm.SetFundsLotHead(sv, &vm.FundsLotHead{
		Chain:   chain,
		Asset:   asset,
		VaultID: vaultID,
		Height:  100,
		Seq:     1,
	})
	// 保存头指针
	for _, op := range sv.Diff() {
		store[op.Key] = op.Value
	}

	// 测试获取头部 lot
	sv2 := vm.NewStateView(readFn, scanFn)
	requestID, ok := vm.GetFundsLotAtHead(sv2, chain, asset, vaultID)
	assert.True(t, ok)
	assert.Equal(t, "request_001", requestID)

	// 消费第一个 lot
	sv3 := vm.NewStateView(readFn, scanFn)
	consumedID, consumed := vm.ConsumeFundsLot(sv3, chain, asset, vaultID)
	assert.True(t, consumed)
	assert.Equal(t, "request_001", consumedID)

	// 保存头指针更新
	for _, op := range sv3.Diff() {
		store[op.Key] = op.Value
	}

	// 验证头指针已推进到 (100, 2)
	sv4 := vm.NewStateView(readFn, scanFn)
	head2 := vm.GetFundsLotHead(sv4, chain, asset, vaultID)
	assert.Equal(t, uint64(100), head2.Height)
	assert.Equal(t, uint64(2), head2.Seq)

	// 获取新头部 lot
	requestID2, ok2 := vm.GetFundsLotAtHead(sv4, chain, asset, vaultID)
	assert.True(t, ok2)
	assert.Equal(t, "request_002", requestID2)

	// 消费第二个 lot
	sv5 := vm.NewStateView(readFn, scanFn)
	consumedID2, consumed2 := vm.ConsumeFundsLot(sv5, chain, asset, vaultID)
	assert.True(t, consumed2)
	assert.Equal(t, "request_002", consumedID2)

	// 保存头指针更新
	for _, op := range sv5.Diff() {
		store[op.Key] = op.Value
	}

	// 验证头指针已推进到 (102, 1)
	sv6 := vm.NewStateView(readFn, scanFn)
	head3 := vm.GetFundsLotHead(sv6, chain, asset, vaultID)
	assert.Equal(t, uint64(102), head3.Height)
	assert.Equal(t, uint64(1), head3.Seq)

	// 获取新头部 lot
	requestID3, ok3 := vm.GetFundsLotAtHead(sv6, chain, asset, vaultID)
	assert.True(t, ok3)
	assert.Equal(t, "request_003", requestID3)
}
