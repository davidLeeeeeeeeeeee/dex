package vm_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	iface "dex/interfaces"
	"dex/keys"
	"dex/pb"
	"dex/vm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// ========== Mock数据库实现 ==========

type MockDB struct {
	mu              sync.RWMutex
	data            map[string][]byte
	pending         []func()
	stateSyncCalls  int
	stateSyncHeight uint64
	stateSyncOps    int
}

// MockSession 模拟数据库会话
type MockSession struct {
	db *MockDB
}

func (s *MockSession) Get(key string) ([]byte, error) {
	return s.db.Get(key)
}

func (s *MockSession) ApplyStateUpdate(height uint64, updates []interface{}) ([]byte, error) {
	for _, u := range updates {
		type writeOpInterface interface {
			GetKey() string
			GetValue() []byte
			IsDel() bool
		}
		if op, ok := u.(writeOpInterface); ok {
			s.db.mu.Lock()
			if op.IsDel() {
				delete(s.db.data, op.GetKey())
			} else {
				s.db.data[op.GetKey()] = op.GetValue()
			}
			s.db.mu.Unlock()
		}
	}
	return []byte("mock_root"), nil
}

func (s *MockSession) Commit() error   { return nil }
func (s *MockSession) Rollback() error { return nil }
func (s *MockSession) Close() error    { return nil }

func (db *MockDB) NewSession() (iface.DBSession, error) {
	return &MockSession{db: db}, nil
}

func (db *MockDB) CommitRoot(height uint64, root []byte) {
	// Mock实现：简单记录或忽略
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

func (db *MockDB) GetKV(key string) ([]byte, error) {
	return db.Get(key)
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

func (db *MockDB) SyncStateUpdates(height uint64, updates []interface{}) (int, error) {
	type writeOpInterface interface {
		GetKey() string
		GetValue() []byte
		IsDel() bool
	}

	count := 0
	for _, u := range updates {
		op, ok := u.(writeOpInterface)
		if !ok || op == nil || op.GetKey() == "" {
			continue
		}
		count++
	}

	db.mu.Lock()
	db.stateSyncCalls++
	db.stateSyncHeight = height
	db.stateSyncOps = count
	db.mu.Unlock()
	return count, nil
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
func (db *MockDB) ScanKVWithLimit(prefix string, limit int) (map[string][]byte, error) {
	result := make(map[string][]byte)
	db.mu.RLock()
	defer db.mu.RUnlock()
	count := 0
	for k, v := range db.data {
		if strings.HasPrefix(k, prefix) {
			if limit > 0 && count >= limit {
				break
			}
			valCopy := make([]byte, len(v))
			copy(valCopy, v)
			result[k] = valCopy
			count++
		}
	}
	return result, nil
}

func (db *MockDB) ScanKVWithLimitReverse(prefix string, limit int) (map[string][]byte, error) {
	return db.ScanKVWithLimit(prefix, limit)
}

func (db *MockDB) ScanOrdersByPairs(pairs []string) (map[string]map[string][]byte, error) {
	result := make(map[string]map[string][]byte)

	db.mu.RLock()
	defer db.mu.RUnlock()

	for _, pair := range pairs {
		prefix := keys.KeyOrderPriceIndexGeneralPrefix(pair, false)
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

// ========== Frost 相关方法（满足 DBManager 接口）==========

func (db *MockDB) GetFrostVaultTransition(key string) (*pb.VaultTransitionState, error) {
	data, err := db.Get(key)
	if err != nil || data == nil {
		return nil, err
	}
	var state pb.VaultTransitionState
	if err := proto.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (db *MockDB) SetFrostVaultTransition(key string, state *pb.VaultTransitionState) error {
	data, _ := proto.Marshal(state)
	db.mu.Lock()
	db.data[key] = data
	db.mu.Unlock()
	return nil
}

func (db *MockDB) GetFrostDkgCommitment(key string) (*pb.FrostVaultDkgCommitment, error) {
	data, err := db.Get(key)
	if err != nil || data == nil {
		return nil, err
	}
	var commitment pb.FrostVaultDkgCommitment
	if err := proto.Unmarshal(data, &commitment); err != nil {
		return nil, err
	}
	return &commitment, nil
}

func (db *MockDB) SetFrostDkgCommitment(key string, commitment *pb.FrostVaultDkgCommitment) error {
	data, _ := proto.Marshal(commitment)
	db.mu.Lock()
	db.data[key] = data
	db.mu.Unlock()
	return nil
}

func (db *MockDB) GetFrostDkgShare(key string) (*pb.FrostVaultDkgShare, error) {
	data, err := db.Get(key)
	if err != nil || data == nil {
		return nil, err
	}
	var share pb.FrostVaultDkgShare
	if err := proto.Unmarshal(data, &share); err != nil {
		return nil, err
	}
	return &share, nil
}

func (db *MockDB) SetFrostDkgShare(key string, share *pb.FrostVaultDkgShare) error {
	data, _ := proto.Marshal(share)
	db.mu.Lock()
	db.data[key] = data
	db.mu.Unlock()
	return nil
}

func (db *MockDB) GetFrostVaultState(key string) (*pb.FrostVaultState, error) {
	data, err := db.Get(key)
	if err != nil || data == nil {
		return nil, err
	}
	var state pb.FrostVaultState
	if err := proto.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (db *MockDB) SetFrostVaultState(key string, state *pb.FrostVaultState) error {
	data, _ := proto.Marshal(state)
	db.mu.Lock()
	db.data[key] = data
	db.mu.Unlock()
	return nil
}

func (db *MockDB) GetFrostDkgComplaint(key string) (*pb.FrostVaultDkgComplaint, error) {
	data, err := db.Get(key)
	if err != nil || data == nil {
		return nil, err
	}
	var complaint pb.FrostVaultDkgComplaint
	if err := proto.Unmarshal(data, &complaint); err != nil {
		return nil, err
	}
	return &complaint, nil
}

func (db *MockDB) SetFrostDkgComplaint(key string, complaint *pb.FrostVaultDkgComplaint) error {
	data, _ := proto.Marshal(complaint)
	db.mu.Lock()
	db.data[key] = data
	db.mu.Unlock()
	return nil
}

// ========== 测试用例 ==========

func TestBasicExecution(t *testing.T) {
	// 创建数据库和组件
	db := NewMockDB()

	// 初始化账户数据（使用分离存储）
	aliceAccount := &pb.Account{Address: "alice"}
	aliceData, _ := proto.Marshal(aliceAccount)
	db.data[keys.KeyAccount("alice")] = aliceData

	// 分离存储余额
	aliceBal := &pb.TokenBalanceRecord{
		Balance: &pb.TokenBalance{
			Balance:            "1000",
			MinerLockedBalance: "0",
		},
	}
	aliceBalData, _ := proto.Marshal(aliceBal)
	db.data[keys.KeyBalance("alice", "token123")] = aliceBalData

	bobAccount := &pb.Account{Address: "bob"}
	bobData, _ := proto.Marshal(bobAccount)
	db.data[keys.KeyAccount("bob")] = bobData

	bobBal := &pb.TokenBalanceRecord{
		Balance: &pb.TokenBalance{
			Balance:            "500",
			MinerLockedBalance: "0",
		},
	}
	bobBalData, _ := proto.Marshal(bobBal)
	db.data[keys.KeyBalance("bob", "token123")] = bobBalData

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
		BlockHash: "block_001",
		Header: &pb.BlockHeader{
			PrevBlockHash: "genesis",
			Height:        1,
		},
		Body: []*pb.AnyTx{transferTx},
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

	// 检查余额数据没有变化（预执行不应改变数据库）
	aliceBalKey := keys.KeyBalance("alice", "token123")
	balVal, _ := db.Get(aliceBalKey)
	var checkBal pb.TokenBalanceRecord
	proto.Unmarshal(balVal, &checkBal)
	if checkBal.Balance.Balance != "1000" {
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

	// 初始化账户数据（使用分离存储）
	aliceAccount := &pb.Account{Address: "alice"}
	aliceData, _ := proto.Marshal(aliceAccount)
	db.data[keys.KeyAccount("alice")] = aliceData

	aliceBal := &pb.TokenBalanceRecord{
		Balance: &pb.TokenBalance{
			Balance:            "1000",
			MinerLockedBalance: "0",
		},
	}
	aliceBalData, _ := proto.Marshal(aliceBal)
	db.data[keys.KeyBalance("alice", "token123")] = aliceBalData

	bobAccount := &pb.Account{Address: "bob"}
	bobData, _ := proto.Marshal(bobAccount)
	db.data[keys.KeyAccount("bob")] = bobData

	bobBal := &pb.TokenBalanceRecord{
		Balance: &pb.TokenBalance{
			Balance:            "0",
			MinerLockedBalance: "0",
		},
	}
	bobBalData, _ := proto.Marshal(bobBal)
	db.data[keys.KeyBalance("bob", "token123")] = bobBalData

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
		BlockHash: "block_cache_test",
		Header: &pb.BlockHeader{
			PrevBlockHash: "genesis",
			Height:        1,
		},
		Body: []*pb.AnyTx{transferTx},
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
					FromAddress: "alice_addr",
					Nonce:       1,
					Status:      pb.Status_PENDING,
				},
				To:           "bob",
				TokenAddress: "token123",
				Amount:       "200",
			},
		},
	}

	block := &pb.Block{
		BlockHash: "block_invalid",
		Header: &pb.BlockHeader{
			PrevBlockHash: "genesis",
			Height:        2,
		},
		Body: []*pb.AnyTx{transferTx},
	}

	// 预执行
	result, _ := executor.PreExecuteBlock(block)

	// 交易执行失败，但区块结构有效，PreExecuteBlock 应返回验证通过
	// 失败的交易会记录在 Receipts 中
	assert.True(t, result.Valid, "Block structure should be valid despite invalid tx execution")
	require.Equal(t, 1, len(result.Receipts))
	assert.Equal(t, "FAILED", result.Receipts[0].Status)
}

func TestConcurrentExecution(t *testing.T) {
	db := NewMockDB()
	registry := vm.NewHandlerRegistry()
	vm.RegisterDefaultHandlers(registry)

	cache := vm.NewSpecExecLRU(100)
	executor := vm.NewExecutor(db, registry, cache)

	// 初始化多个账户（使用分离存储）
	for i := 0; i < 10; i++ {
		userAddr := fmt.Sprintf("user%d", i)
		account := &pb.Account{Address: userAddr}
		accountData, _ := proto.Marshal(account)
		db.data[keys.KeyAccount(userAddr)] = accountData

		bal := &pb.TokenBalanceRecord{
			Balance: &pb.TokenBalance{
				Balance:            "1000",
				MinerLockedBalance: "0",
			},
		}
		balData, _ := proto.Marshal(bal)
		db.data[keys.KeyBalance(userAddr, "token123")] = balData
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
				BlockHash: fmt.Sprintf("block_%d", blockNum),
				Header: &pb.BlockHeader{
					PrevBlockHash: "genesis",
					Height:        uint64(blockNum + 1),
				},
				Body: txs,
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

	// 准备数据（使用分离存储）
	for i := 0; i < 100; i++ {
		userAddr := fmt.Sprintf("user%d", i)
		account := &pb.Account{Address: userAddr}
		accountData, _ := proto.Marshal(account)
		db.data[keys.KeyAccount(userAddr)] = accountData

		bal := &pb.TokenBalanceRecord{
			Balance: &pb.TokenBalance{
				Balance:            "10000",
				MinerLockedBalance: "0",
			},
		}
		balData, _ := proto.Marshal(bal)
		db.data[keys.KeyBalance(userAddr, "token123")] = balData
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
		BlockHash: "bench_block",
		Header: &pb.BlockHeader{
			PrevBlockHash: "genesis",
			Height:        1,
		},
		Body: txs,
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
	seqKey := keys.KeyFrostWithdrawFIFOSeq("btc", "NATIVE")
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

// ========== BTC 验签 + UTXO Lock 测试 ==========

// createTestBTCTemplateData 创建测试用的 BTC 模板数据
func createTestBTCTemplateData(inputs []struct {
	TxID   string
	Vout   uint32
	Amount uint64
}, outputAmount uint64, withdrawID string) []byte {
	// 简化的 BTC 模板 JSON
	template := map[string]interface{}{
		"version":   2,
		"lock_time": 0,
		"vault_id":  1,
		"key_epoch": 1,
		"inputs":    make([]map[string]interface{}, len(inputs)),
		"outputs": []map[string]interface{}{
			{"address": "bc1qtest", "amount": outputAmount},
		},
		"withdraw_ids": []string{withdrawID},
	}
	for i, in := range inputs {
		template["inputs"].([]map[string]interface{})[i] = map[string]interface{}{
			"txid":     in.TxID,
			"vout":     in.Vout,
			"amount":   in.Amount,
			"sequence": 0xffffffff,
		}
	}
	data, _ := json.Marshal(template)
	return data
}

// TestFrostWithdrawSignedBTC_UTXOLock 测试 BTC UTXO 锁定防双花
func TestFrostWithdrawSignedBTC_UTXOLock(t *testing.T) {
	db := NewMockDB()

	readFn := func(key string) ([]byte, error) {
		return db.Get(key)
	}
	scanFn := func(prefix string) (map[string][]byte, error) {
		return db.Scan(prefix)
	}

	// 创建 Registry 和注册 handler
	reg := vm.NewHandlerRegistry()
	vm.RegisterDefaultHandlers(reg)

	// 准备测试数据：创建一个 QUEUED withdraw
	withdrawID := "test_withdraw_btc_001"
	chain := "btc"
	asset := "native"

	// 写入 withdraw
	withdrawKey := keys.KeyFrostWithdraw(withdrawID)
	withdrawState := &pb.FrostWithdrawState{
		WithdrawId:    withdrawID,
		TxId:          "orig_tx_001",
		Seq:           1,
		Chain:         chain,
		Asset:         asset,
		To:            "bc1qtest",
		Amount:        "10000",
		RequestHeight: 100,
		Status:        "QUEUED",
	}
	withdrawData, _ := proto.Marshal(withdrawState)
	db.mu.Lock()
	db.data[withdrawKey] = withdrawData
	db.mu.Unlock()

	// 创建 BTC 模板数据
	inputs := []struct {
		TxID   string
		Vout   uint32
		Amount uint64
	}{
		{"prev_tx_001", 0, 50000},
		{"prev_tx_002", 1, 30000},
	}
	templateData := createTestBTCTemplateData(inputs, 10000, withdrawID)

	// 创建虚拟签名（每个 input 64 字节）
	inputSigs := [][]byte{
		make([]byte, 64),
		make([]byte, 64),
	}
	scriptPubkeys := [][]byte{
		make([]byte, 34), // P2TR scriptPubKey
		make([]byte, 34),
	}

	// 创建 FrostWithdrawSignedTx
	jobID := "job_btc_001"

	signedTx := &pb.AnyTx{
		Content: &pb.AnyTx_FrostWithdrawSignedTx{
			FrostWithdrawSignedTx: &pb.FrostWithdrawSignedTx{
				Base: &pb.BaseMessage{
					TxId:           "signed_tx_001",
					ExecutedHeight: 101,
					FromAddress:    "signer_001",
				},
				JobId:              jobID,
				SignedPackageBytes: []byte("dummy_signature"),
				WithdrawIds:        []string{withdrawID},
				VaultId:            1,
				KeyEpoch:           1,
				Chain:              chain,
				Asset:              asset,
				TemplateData:       templateData,
				InputSigs:          inputSigs,
				ScriptPubkeys:      scriptPubkeys,
			},
		},
	}

	// 执行 DryRun（由于没有 Vault 状态，验签会被跳过）
	sv := vm.NewStateView(readFn, scanFn)
	handler, _ := reg.Get("frost_withdraw_signed")
	ops, receipt, err := handler.DryRun(signedTx, sv)

	assert.NoError(t, err)
	assert.Equal(t, "SUCCEED", receipt.Status)
	assert.True(t, len(ops) > 0)

	// 验证 UTXO lock 已写入
	hasUtxoLock := false
	for _, op := range ops {
		if strings.Contains(op.Key, "frost_btc_locked_utxo") {
			hasUtxoLock = true
			assert.Equal(t, jobID, string(op.Value))
		}
	}
	assert.True(t, hasUtxoLock, "should have UTXO lock ops")

	// 模拟第二次提交（不同 job）尝试使用同一 UTXO - 应该失败
	// 先应用第一次的结果
	for _, op := range ops {
		db.mu.Lock()
		db.data[op.Key] = op.Value
		db.mu.Unlock()
	}

	// 创建第二个 withdraw（新的 QUEUED 状态）
	withdrawID2 := "test_withdraw_btc_002"
	withdrawKey2 := keys.KeyFrostWithdraw(withdrawID2)
	withdrawState2 := &pb.FrostWithdrawState{
		WithdrawId:    withdrawID2,
		TxId:          "orig_tx_002",
		Seq:           2, // 下一个序号
		Chain:         chain,
		Asset:         asset,
		To:            "bc1qtest2",
		Amount:        "20000",
		RequestHeight: 100,
		Status:        "QUEUED",
	}
	withdrawData2, _ := proto.Marshal(withdrawState2)
	db.mu.Lock()
	db.data[withdrawKey2] = withdrawData2
	db.mu.Unlock()

	// 设置 FIFO head 为 2（因为第一个 withdraw 已被处理）
	fifoHeadKey := keys.KeyFrostWithdrawFIFOHead(chain, asset)
	db.mu.Lock()
	db.data[fifoHeadKey] = []byte("2")
	db.mu.Unlock()

	// 创建第二个 job 尝试使用同一 UTXO（只用第一个 input）
	inputs2 := []struct {
		TxID   string
		Vout   uint32
		Amount uint64
	}{
		{"prev_tx_001", 0, 50000}, // 同一 UTXO
	}
	templateData2 := createTestBTCTemplateData(inputs2, 10000, withdrawID2)
	inputSigs2 := [][]byte{make([]byte, 64)}
	scriptPubkeys2 := [][]byte{make([]byte, 34)}

	signedTx2 := &pb.AnyTx{
		Content: &pb.AnyTx_FrostWithdrawSignedTx{
			FrostWithdrawSignedTx: &pb.FrostWithdrawSignedTx{
				Base: &pb.BaseMessage{
					TxId:           "signed_tx_002",
					ExecutedHeight: 102,
					FromAddress:    "signer_002",
				},
				JobId:              "job_btc_002", // 不同的 job
				SignedPackageBytes: []byte("dummy_signature2"),
				WithdrawIds:        []string{withdrawID2},
				VaultId:            1,
				KeyEpoch:           1,
				Chain:              chain,
				Asset:              asset,
				TemplateData:       templateData2,
				InputSigs:          inputSigs2,
				ScriptPubkeys:      scriptPubkeys2,
			},
		},
	}

	sv2 := vm.NewStateView(readFn, scanFn)
	_, receipt2, err2 := handler.DryRun(signedTx2, sv2)

	// 应该失败因为 UTXO 已被锁定
	assert.Error(t, err2)
	assert.Equal(t, "FAILED", receipt2.Status)
	assert.Contains(t, receipt2.Error, "UTXO already locked")
}

// TestFrostWithdrawSignedBTC_SameJobResubmit 测试同一 job 重复提交（幂等）
func TestFrostWithdrawSignedBTC_SameJobResubmit(t *testing.T) {
	db := NewMockDB()

	readFn := func(key string) ([]byte, error) {
		return db.Get(key)
	}
	scanFn := func(prefix string) (map[string][]byte, error) {
		return db.Scan(prefix)
	}

	reg := vm.NewHandlerRegistry()
	vm.RegisterDefaultHandlers(reg)

	// 准备 QUEUED withdraw
	withdrawID := "test_withdraw_btc_002"
	chain := "btc"
	asset := "native"
	jobID := "job_btc_same_001"

	withdrawKey := keys.KeyFrostWithdraw(withdrawID)
	withdrawState := &pb.FrostWithdrawState{
		WithdrawId: withdrawID,
		Seq:        1,
		Chain:      chain,
		Asset:      asset,
		Status:     "QUEUED",
	}
	withdrawData, _ := proto.Marshal(withdrawState)
	db.mu.Lock()
	db.data[withdrawKey] = withdrawData
	db.mu.Unlock()

	// 创建 BTC 模板数据
	inputs := []struct {
		TxID   string
		Vout   uint32
		Amount uint64
	}{
		{"prev_tx_same", 0, 10000},
	}
	templateData := createTestBTCTemplateData(inputs, 9000, withdrawID)
	inputSigs := [][]byte{make([]byte, 64)}
	scriptPubkeys := [][]byte{make([]byte, 34)}

	signedTx := &pb.AnyTx{
		Content: &pb.AnyTx_FrostWithdrawSignedTx{
			FrostWithdrawSignedTx: &pb.FrostWithdrawSignedTx{
				Base: &pb.BaseMessage{
					TxId:           "signed_tx_same_001",
					ExecutedHeight: 101,
					FromAddress:    "signer",
				},
				JobId:              jobID,
				SignedPackageBytes: []byte("sig1"),
				WithdrawIds:        []string{withdrawID},
				VaultId:            1,
				Chain:              chain,
				Asset:              asset,
				TemplateData:       templateData,
				InputSigs:          inputSigs,
				ScriptPubkeys:      scriptPubkeys,
			},
		},
	}

	// 第一次提交
	sv1 := vm.NewStateView(readFn, scanFn)
	handler, _ := reg.Get("frost_withdraw_signed")
	ops1, receipt1, err1 := handler.DryRun(signedTx, sv1)

	assert.NoError(t, err1)
	assert.Equal(t, "SUCCEED", receipt1.Status)

	// 应用结果
	for _, op := range ops1 {
		db.mu.Lock()
		db.data[op.Key] = op.Value
		db.mu.Unlock()
	}

	// 同一 job 再次提交（幂等，应该只追加 receipt）
	signedTx2 := &pb.AnyTx{
		Content: &pb.AnyTx_FrostWithdrawSignedTx{
			FrostWithdrawSignedTx: &pb.FrostWithdrawSignedTx{
				Base: &pb.BaseMessage{
					TxId:           "signed_tx_same_002",
					ExecutedHeight: 102,
					FromAddress:    "signer2",
				},
				JobId:              jobID, // 同一 job
				SignedPackageBytes: []byte("sig2"),
				WithdrawIds:        []string{withdrawID},
				VaultId:            1,
				Chain:              chain,
				Asset:              asset,
				TemplateData:       templateData,  // 同一模板
				InputSigs:          inputSigs,     // 同一签名
				ScriptPubkeys:      scriptPubkeys, // 同一 scriptPubkeys
			},
		},
	}

	sv2 := vm.NewStateView(readFn, scanFn)
	ops2, receipt2, err2 := handler.DryRun(signedTx2, sv2)

	// 应该成功（幂等追加）
	assert.NoError(t, err2)
	assert.Equal(t, "SUCCEED", receipt2.Status)
	// 只应该有 pkg + count 更新，没有新的 UTXO lock
	assert.True(t, len(ops2) <= 2)
}
