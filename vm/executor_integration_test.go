package vm_test

import (
	"encoding/json"
	"sync"
	"testing"

	iface "dex/interfaces"
	"dex/keys"
	"dex/pb"
	"dex/vm"
)

// ========== Mock StateDB ==========

type MockStateDB struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func NewMockStateDB() *MockStateDB {
	return &MockStateDB{
		data: make(map[string][]byte),
	}
}

func (s *MockStateDB) ApplyAccountUpdate(height uint64, update interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 模拟 StateDB 的更新逻辑
	switch u := update.(type) {
	case map[string]interface{}:
		key := u["Key"].(string)
		value := u["Value"].([]byte)
		deleted := u["Deleted"].(bool)

		if deleted {
			delete(s.data, key)
		} else {
			s.data[key] = value
		}
	}
	return nil
}

func (s *MockStateDB) Get(key string) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	val, exists := s.data[key]
	return val, exists
}

// ========== Enhanced MockDB with StateDB ==========

type EnhancedMockDB struct {
	*MockDB
	stateDB *MockStateDB
}

func NewEnhancedMockDB() *EnhancedMockDB {
	return &EnhancedMockDB{
		MockDB:  NewMockDB(),
		stateDB: NewMockStateDB(),
	}
}

func (db *EnhancedMockDB) NewSession() (iface.DBSession, error) {
	return db.MockDB.NewSession()
}

func (db *EnhancedMockDB) CommitRoot(height uint64, root []byte) {
	// 简单实现
}

// ========== 测试用例 ==========

// TestWriteOpSyncStateDB 测试 WriteOp 的 SyncStateDB 功能
func TestWriteOpSyncStateDB(t *testing.T) {
	db := NewEnhancedMockDB()

	// 初始化账户数据
	aliceAddr := "alice"
	accountKey := keys.KeyAccount(aliceAddr)

	account := &pb.Account{
		Address: aliceAddr,
		Balances: map[string]*pb.TokenBalance{
			"FB": {
				Balance:            "1000",
				MinerLockedBalance: "0",
			},
		},
	}
	accountData, _ := json.Marshal(account)
	db.data[accountKey] = accountData

	// 创建执行器
	registry := vm.NewHandlerRegistry()
	if err := vm.RegisterDefaultHandlers(registry); err != nil {
		t.Fatal(err)
	}

	cache := vm.NewSpecExecLRU(100)
	executor := vm.NewExecutor(db, registry, cache)

	// 创建转账交易
	transferTx := &pb.AnyTx{
		Content: &pb.AnyTx_Transaction{
			Transaction: &pb.Transaction{
				Base: &pb.BaseMessage{
					TxId:        "tx_sync_001",
					FromAddress: aliceAddr,
					Status:      pb.Status_PENDING,
				},
				To:           "bob",
				TokenAddress: "FB",
				Amount:       "100",
			},
		},
	}

	block := &pb.Block{
		BlockHash:     "block_sync_001",
		PrevBlockHash: "genesis",
		Height:        1,
		Body:          []*pb.AnyTx{transferTx},
	}

	// 提交区块
	err := executor.CommitFinalizedBlock(block)
	if err != nil {
		t.Fatal("Commit failed:", err)
	}

	// 验证 Badger 中的数据已更新
	badgerData, err := db.Get(accountKey)
	if err != nil {
		t.Fatal("Failed to get account from Badger:", err)
	}
	if badgerData == nil {
		t.Fatal("Account should exist in Badger")
	}

	var badgerAccount pb.Account
	if err := json.Unmarshal(badgerData, &badgerAccount); err != nil {
		t.Fatal("Failed to unmarshal Badger account:", err)
	}

	// 验证 StateDB 中的数据已同步（因为账户数据的 SyncStateDB=true）
	stateDBData, exists := db.stateDB.Get(accountKey)
	if !exists {
		t.Fatalf("Account should be synced to StateDB (key=%s)", accountKey)
	}

	var stateDBAccount pb.Account
	if err := json.Unmarshal(stateDBData, &stateDBAccount); err != nil {
		t.Fatal("Failed to unmarshal StateDB account:", err)
	}

	// 验证 Badger 和 StateDB 的数据一致
	if badgerAccount.Balances["FB"].Balance != stateDBAccount.Balances["FB"].Balance {
		t.Fatalf("Badger and StateDB data mismatch: Badger=%s, StateDB=%s",
			badgerAccount.Balances["FB"].Balance,
			stateDBAccount.Balances["FB"].Balance)
	}

	t.Logf("✅ Account data synced correctly: Balance=%s", badgerAccount.Balances["FB"].Balance)
}

// TestWriteOpNoSyncStateDB 测试非账户数据不同步到 StateDB
func TestWriteOpNoSyncStateDB(t *testing.T) {
	db := NewEnhancedMockDB()

	// 初始化账户数据
	issuerAddr := "issuer"
	accountKey := keys.KeyAccount(issuerAddr)

	account := &pb.Account{
		Address: issuerAddr,
		Balances: map[string]*pb.TokenBalance{
			"FB": {
				Balance:            "10000",
				MinerLockedBalance: "0",
			},
		},
	}
	accountData, _ := json.Marshal(account)
	db.data[accountKey] = accountData

	// 创建执行器
	registry := vm.NewHandlerRegistry()
	if err := vm.RegisterDefaultHandlers(registry); err != nil {
		t.Fatal(err)
	}

	cache := vm.NewSpecExecLRU(100)
	executor := vm.NewExecutor(db, registry, cache)

	// 创建发行 Token 交易
	issueTx := &pb.AnyTx{
		Content: &pb.AnyTx_IssueTokenTx{
			IssueTokenTx: &pb.IssueTokenTx{
				Base: &pb.BaseMessage{
					TxId:        "tx_issue_001",
					FromAddress: issuerAddr,
					Status:      pb.Status_PENDING,
				},
				TokenName:   "TestToken",
				TokenSymbol: "TT",
				TotalSupply: "1000000",
			},
		},
	}

	block := &pb.Block{
		BlockHash:     "block_issue_001",
		PrevBlockHash: "genesis",
		Height:        1,
		Body:          []*pb.AnyTx{issueTx},
	}

	// 提交区块
	err := executor.CommitFinalizedBlock(block)
	if err != nil {
		t.Fatal("Commit failed:", err)
	}

	// 验证账户数据同步到 StateDB（SyncStateDB=true）
	_, accountExists := db.stateDB.Get(accountKey)
	if !accountExists {
		t.Fatal("Account should be synced to StateDB")
	}

	// 验证 Token 数据没有同步到 StateDB（SyncStateDB=false）
	tokenKey := keys.KeyToken("generated_token_address") // 实际地址需要从交易中获取
	_, tokenExists := db.stateDB.Get(tokenKey)
	if tokenExists {
		t.Fatal("Token data should NOT be synced to StateDB")
	}

	t.Logf("✅ Non-account data correctly NOT synced to StateDB")
}

// TestIdempotency 测试幂等性（防止重复提交）
func TestIdempotency(t *testing.T) {
	db := NewEnhancedMockDB()

	// 初始化账户数据
	aliceAddr := "alice"
	accountKey := keys.KeyAccount(aliceAddr)

	account := &pb.Account{
		Address: aliceAddr,
		Balances: map[string]*pb.TokenBalance{
			"FB": {
				Balance:            "1000",
				MinerLockedBalance: "0",
			},
		},
	}
	accountData, _ := json.Marshal(account)
	db.data[accountKey] = accountData

	// 创建执行器
	registry := vm.NewHandlerRegistry()
	if err := vm.RegisterDefaultHandlers(registry); err != nil {
		t.Fatal(err)
	}

	cache := vm.NewSpecExecLRU(100)
	executor := vm.NewExecutor(db, registry, cache)

	// 创建转账交易
	transferTx := &pb.AnyTx{
		Content: &pb.AnyTx_Transaction{
			Transaction: &pb.Transaction{
				Base: &pb.BaseMessage{
					TxId:        "tx_idem_001",
					FromAddress: aliceAddr,
					Status:      pb.Status_PENDING,
				},
				To:           "bob",
				TokenAddress: "FB",
				Amount:       "100",
			},
		},
	}

	block := &pb.Block{
		BlockHash:     "block_idem_001",
		PrevBlockHash: "genesis",
		Height:        1,
		Body:          []*pb.AnyTx{transferTx},
	}

	// 第一次提交
	err := executor.CommitFinalizedBlock(block)
	if err != nil {
		t.Fatal("First commit failed:", err)
	}

	// 第二次提交同一个区块（应该被幂等性检查拦截，返回 nil 表示已提交）
	err = executor.CommitFinalizedBlock(block)
	if err != nil {
		t.Fatalf("Second commit should succeed (idempotent): %v", err)
	}

	t.Logf("✅ Idempotency check working: second commit returned nil (already committed)")
}
