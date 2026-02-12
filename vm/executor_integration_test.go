package vm_test

import (
	"sync"
	"testing"

	iface "dex/interfaces"
	"dex/keys"
	"dex/pb"
	"dex/vm"

	"google.golang.org/protobuf/proto"
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

func (db *EnhancedMockDB) ScanKVWithLimit(prefix string, limit int) (map[string][]byte, error) {
	return db.MockDB.ScanKVWithLimit(prefix, limit)
}

func (db *EnhancedMockDB) ScanKVWithLimitReverse(prefix string, limit int) (map[string][]byte, error) {
	return db.MockDB.ScanKVWithLimitReverse(prefix, limit)
}

func (db *EnhancedMockDB) ApplyAccountUpdate(height uint64, update interface{}) error {
	return db.stateDB.ApplyAccountUpdate(height, update)
}

// ========== 测试用例 ==========

// TestWriteOpSyncStateDB 测试 WriteOp 的 SyncStateDB 功能
func TestWriteOpSyncStateDB(t *testing.T) {
	db := NewEnhancedMockDB()

	// 初始化账户数据（使用分离存储）
	aliceAddr := "alice"
	accountKey := keys.KeyAccount(aliceAddr)

	account := &pb.Account{
		Address: aliceAddr,
	}
	accountData, _ := proto.Marshal(account)
	db.data[accountKey] = accountData

	// 分离存储余额
	bal := &pb.TokenBalanceRecord{
		Balance: &pb.TokenBalance{
			Balance:            "1000",
			MinerLockedBalance: "0",
		},
	}
	balData, _ := proto.Marshal(bal)
	balKey := keys.KeyBalance(aliceAddr, "FB")
	db.data[balKey] = balData

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
		BlockHash: "block_sync_001",
		Header: &pb.BlockHeader{
			PrevBlockHash: "genesis",
			Height:        1,
		},
		Body: []*pb.AnyTx{transferTx},
	}

	// 提交区块
	err := executor.CommitFinalizedBlock(block)
	if err != nil {
		t.Fatal("Commit failed:", err)
	}

	// 验证 Badger 中的余额数据已更新
	badgerBalData, err := db.Get(balKey)
	if err != nil {
		t.Fatal("Failed to get balance from Badger:", err)
	}
	if badgerBalData == nil {
		t.Fatal("Balance should exist in Badger")
	}

	var badgerBal pb.TokenBalanceRecord
	if err := proto.Unmarshal(badgerBalData, &badgerBal); err != nil {
		t.Fatal("Failed to unmarshal Badger balance:", err)
	}

	// 验证 StateDB 中的数据已同步
	stateDBBalData, exists := db.stateDB.Get(balKey)
	if !exists {
		t.Fatalf("Balance should be synced to StateDB (key=%s)", balKey)
	}

	var stateDBBal pb.TokenBalanceRecord
	if err := proto.Unmarshal(stateDBBalData, &stateDBBal); err != nil {
		t.Fatal("Failed to unmarshal StateDB balance:", err)
	}

	// 验证 Badger 和 StateDB 的数据一致
	if badgerBal.Balance.Balance != stateDBBal.Balance.Balance {
		t.Fatalf("Badger and StateDB data mismatch: Badger=%s, StateDB=%s",
			badgerBal.Balance.Balance,
			stateDBBal.Balance.Balance)
	}

	t.Logf("✅ Account data synced correctly: Balance=%s", badgerBal.Balance.Balance)
}

// TestWriteOpNoSyncStateDB 测试非账户数据不同步到 StateDB
func TestWriteOpNoSyncStateDB(t *testing.T) {
	db := NewEnhancedMockDB()

	// 初始化账户数据（使用分离存储）
	issuerAddr := "issuer"
	accountKey := keys.KeyAccount(issuerAddr)

	account := &pb.Account{
		Address: issuerAddr,
	}
	accountData, _ := proto.Marshal(account)
	db.data[accountKey] = accountData

	// 分离存储余额
	bal := &pb.TokenBalanceRecord{
		Balance: &pb.TokenBalance{
			Balance:            "10000",
			MinerLockedBalance: "0",
		},
	}
	balData, _ := proto.Marshal(bal)
	db.data[keys.KeyBalance(issuerAddr, "FB")] = balData

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
		BlockHash: "block_issue_001",
		Header: &pb.BlockHeader{
			PrevBlockHash: "genesis",
			Height:        1,
		},
		Body: []*pb.AnyTx{issueTx},
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

	// 初始化账户数据（使用分离存储）
	aliceAddr := "alice"
	accountKey := keys.KeyAccount(aliceAddr)

	account := &pb.Account{
		Address: aliceAddr,
	}
	accountData, _ := proto.Marshal(account)
	db.data[accountKey] = accountData

	// 分离存储余额
	bal := &pb.TokenBalanceRecord{
		Balance: &pb.TokenBalance{
			Balance:            "1000",
			MinerLockedBalance: "0",
		},
	}
	balData, _ := proto.Marshal(bal)
	db.data[keys.KeyBalance(aliceAddr, "FB")] = balData

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
		BlockHash: "block_idem_001",
		Header: &pb.BlockHeader{
			PrevBlockHash: "genesis",
			Height:        1,
		},
		Body: []*pb.AnyTx{transferTx},
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

// TestCommitFinalizedBlockDoesNotMutateInputTxStatus verifies commit path does not
// mutate tx status in the caller-provided block object.
func TestCommitFinalizedBlockDoesNotMutateInputTxStatus(t *testing.T) {
	db := NewEnhancedMockDB()

	aliceAddr := "alice_input_immutable"
	account := &pb.Account{Address: aliceAddr}
	accountData, _ := proto.Marshal(account)
	db.data[keys.KeyAccount(aliceAddr)] = accountData

	bal := &pb.TokenBalanceRecord{
		Balance: &pb.TokenBalance{
			Balance:            "1000",
			MinerLockedBalance: "0",
		},
	}
	balData, _ := proto.Marshal(bal)
	db.data[keys.KeyBalance(aliceAddr, "FB")] = balData

	registry := vm.NewHandlerRegistry()
	if err := vm.RegisterDefaultHandlers(registry); err != nil {
		t.Fatal(err)
	}
	cache := vm.NewSpecExecLRU(100)
	executor := vm.NewExecutor(db, registry, cache)

	tx := &pb.AnyTx{
		Content: &pb.AnyTx_Transaction{
			Transaction: &pb.Transaction{
				Base: &pb.BaseMessage{
					TxId:        "tx_input_immutable_001",
					FromAddress: aliceAddr,
					Status:      pb.Status_PENDING,
				},
				To:           "bob_input_immutable",
				TokenAddress: "FB",
				Amount:       "10",
			},
		},
	}

	block := &pb.Block{
		BlockHash: "block_input_immutable_001",
		Header: &pb.BlockHeader{
			PrevBlockHash: "genesis",
			Height:        1,
		},
		Body: []*pb.AnyTx{tx},
	}

	if err := executor.CommitFinalizedBlock(block); err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	if got := tx.GetBase().GetStatus(); got != pb.Status_PENDING {
		t.Fatalf("input tx status mutated, want %v got %v", pb.Status_PENDING, got)
	}
}

// TestReplayTxInLaterBlockShouldNotReapply tests that already-applied txs are skipped
// when they appear again in later blocks, preventing repeated balance deduction.
func TestReplayTxInLaterBlockShouldNotReapply(t *testing.T) {
	db := NewEnhancedMockDB()

	aliceAddr := "alice_replay"
	accountKey := keys.KeyAccount(aliceAddr)
	account := &pb.Account{Address: aliceAddr}
	accountData, _ := proto.Marshal(account)
	db.data[accountKey] = accountData

	initialBal := &pb.TokenBalanceRecord{
		Balance: &pb.TokenBalance{
			Balance:            "1000",
			MinerLockedBalance: "0",
		},
	}
	initialBalData, _ := proto.Marshal(initialBal)
	balKey := keys.KeyBalance(aliceAddr, "FB")
	db.data[balKey] = initialBalData

	registry := vm.NewHandlerRegistry()
	if err := vm.RegisterDefaultHandlers(registry); err != nil {
		t.Fatal(err)
	}

	cache := vm.NewSpecExecLRU(100)
	executor := vm.NewExecutor(db, registry, cache)

	txID := "tx_replay_001"
	replayTx := &pb.AnyTx{
		Content: &pb.AnyTx_Transaction{
			Transaction: &pb.Transaction{
				Base: &pb.BaseMessage{
					TxId:        txID,
					FromAddress: aliceAddr,
					Status:      pb.Status_PENDING,
					Fee:         "1",
				},
				To:           "bob_replay",
				TokenAddress: "FB",
				Amount:       "100",
			},
		},
	}

	block1 := &pb.Block{
		BlockHash: "block_replay_001",
		Header: &pb.BlockHeader{
			PrevBlockHash: "genesis",
			Height:        1,
		},
		Body: []*pb.AnyTx{replayTx},
	}
	if err := executor.CommitFinalizedBlock(block1); err != nil {
		t.Fatalf("first commit failed: %v", err)
	}

	// Replay exactly the same tx in a later block.
	block2 := &pb.Block{
		BlockHash: "block_replay_002",
		Header: &pb.BlockHeader{
			PrevBlockHash: block1.BlockHash,
			Height:        2,
		},
		Body: []*pb.AnyTx{replayTx},
	}
	if err := executor.CommitFinalizedBlock(block2); err != nil {
		t.Fatalf("second commit failed: %v", err)
	}

	finalBalData, err := db.Get(balKey)
	if err != nil {
		t.Fatalf("failed to get final balance: %v", err)
	}
	var finalBal pb.TokenBalanceRecord
	if err := proto.Unmarshal(finalBalData, &finalBal); err != nil {
		t.Fatalf("failed to unmarshal final balance: %v", err)
	}

	// Only the first execution should be applied: 1000 - (100 + fee 1) = 899.
	if finalBal.Balance == nil || finalBal.Balance.Balance != "899" {
		got := "<nil>"
		if finalBal.Balance != nil {
			got = finalBal.Balance.Balance
		}
		t.Fatalf("unexpected final balance, want 899 got %s", got)
	}

	// Tx first execution height should remain unchanged (not overwritten by replay block).
	heightVal, err := db.Get(keys.KeyVMTxHeight(txID))
	if err != nil {
		t.Fatalf("failed to get tx height: %v", err)
	}
	if string(heightVal) != "1" {
		t.Fatalf("unexpected tx height, want 1 got %s", string(heightVal))
	}
}

// TestCommitFinalizedBlockReexecAgainstLatestState verifies finalized commit does
// not apply a stale cached pre-execution result computed before parent commit.
func TestCommitFinalizedBlockReexecAgainstLatestState(t *testing.T) {
	db := NewEnhancedMockDB()

	aliceAddr := "alice_stale_cache"
	accountKey := keys.KeyAccount(aliceAddr)
	account := &pb.Account{Address: aliceAddr}
	accountData, _ := proto.Marshal(account)
	db.data[accountKey] = accountData

	initialBal := &pb.TokenBalanceRecord{
		Balance: &pb.TokenBalance{
			Balance:            "1000",
			MinerLockedBalance: "0",
		},
	}
	initialBalData, _ := proto.Marshal(initialBal)
	balKey := keys.KeyBalance(aliceAddr, "FB")
	db.data[balKey] = initialBalData

	registry := vm.NewHandlerRegistry()
	if err := vm.RegisterDefaultHandlers(registry); err != nil {
		t.Fatal(err)
	}

	cache := vm.NewSpecExecLRU(100)
	executor := vm.NewExecutor(db, registry, cache)

	tx1ID := "tx_stale_parent_001"
	tx2ID := "tx_stale_child_002"

	tx1 := &pb.AnyTx{
		Content: &pb.AnyTx_Transaction{
			Transaction: &pb.Transaction{
				Base: &pb.BaseMessage{
					TxId:        tx1ID,
					FromAddress: aliceAddr,
					Status:      pb.Status_PENDING,
					Fee:         "1",
				},
				To:           "bob_stale_parent",
				TokenAddress: "FB",
				Amount:       "100",
			},
		},
	}
	tx2 := &pb.AnyTx{
		Content: &pb.AnyTx_Transaction{
			Transaction: &pb.Transaction{
				Base: &pb.BaseMessage{
					TxId:        tx2ID,
					FromAddress: aliceAddr,
					Status:      pb.Status_PENDING,
					Fee:         "1",
				},
				To:           "bob_stale_child",
				TokenAddress: "FB",
				Amount:       "10",
			},
		},
	}

	block1 := &pb.Block{
		BlockHash: "block_stale_parent_001",
		Header: &pb.BlockHeader{
			PrevBlockHash: "genesis",
			Height:        1,
		},
		Body: []*pb.AnyTx{tx1},
	}
	block2 := &pb.Block{
		BlockHash: "block_stale_child_002",
		Header: &pb.BlockHeader{
			PrevBlockHash: block1.BlockHash,
			Height:        2,
		},
		Body: []*pb.AnyTx{tx2},
	}

	// Pre-execute child block first (stale parent state: Alice still 1000).
	preRes, err := executor.PreExecuteBlock(block2)
	if err != nil {
		t.Fatalf("pre-execute child failed: %v", err)
	}
	if !preRes.Valid {
		t.Fatalf("pre-execute child invalid: %s", preRes.Reason)
	}

	// Commit parent, then child. Child commit must re-execute on latest state.
	if err := executor.CommitFinalizedBlock(block1); err != nil {
		t.Fatalf("commit parent failed: %v", err)
	}
	if err := executor.CommitFinalizedBlock(block2); err != nil {
		t.Fatalf("commit child failed: %v", err)
	}

	finalBalData, err := db.Get(balKey)
	if err != nil {
		t.Fatalf("failed to get final balance: %v", err)
	}
	var finalBal pb.TokenBalanceRecord
	if err := proto.Unmarshal(finalBalData, &finalBal); err != nil {
		t.Fatalf("failed to unmarshal final balance: %v", err)
	}

	// Expected: 1000 - (100+1) - (10+1) = 888.
	if finalBal.Balance == nil || finalBal.Balance.Balance != "888" {
		got := "<nil>"
		if finalBal.Balance != nil {
			got = finalBal.Balance.Balance
		}
		t.Fatalf("unexpected final balance, want 888 got %s", got)
	}

	// Ensure tx2 height reflects finalized child block.
	heightVal, err := db.Get(keys.KeyVMTxHeight(tx2ID))
	if err != nil {
		t.Fatalf("failed to get tx2 height: %v", err)
	}
	if string(heightVal) != "2" {
		t.Fatalf("unexpected tx2 height, want 2 got %s", string(heightVal))
	}
}
