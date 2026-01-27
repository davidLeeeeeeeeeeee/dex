package smt

import (
	"os"
	"testing"

	"github.com/dgraph-io/badger/v4"
)

// ============================================
// JMTStateDB 单元测试
// ============================================

func TestJMTStateDB_BasicOperations(t *testing.T) {
	// 创建临时目录
	tmpDir, err := os.MkdirTemp("", "jmt_statedb_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// 创建 JMTStateDB
	cfg := JMTConfig{
		DataDir: tmpDir,
		Prefix:  []byte("test:"),
	}
	stateDB, err := NewJMTStateDB(cfg)
	if err != nil {
		t.Fatalf("Failed to create JMTStateDB: %v", err)
	}
	defer stateDB.Close()

	// 测试基本的 Set/Get
	t.Run("BasicSetGet", func(t *testing.T) {
		updates := []KVUpdate{
			{Key: "v1_account_alice", Value: []byte(`{"balance":1000}`), Deleted: false},
			{Key: "v1_account_bob", Value: []byte(`{"balance":500}`), Deleted: false},
		}

		// 在高度 1 更新
		err := stateDB.ApplyAccountUpdate(1, updates...)
		if err != nil {
			t.Fatalf("ApplyAccountUpdate failed: %v", err)
		}

		// 验证 Get
		val, exists, err := stateDB.Get("v1_account_alice")
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if !exists {
			t.Fatal("Expected key to exist")
		}
		if string(val) != `{"balance":1000}` {
			t.Errorf("Unexpected value: %s", val)
		}

		// 验证 Version
		if stateDB.Version() != 1 {
			t.Errorf("Expected version 1, got %d", stateDB.Version())
		}

		// 验证 Root 不为空
		root := stateDB.Root()
		if len(root) == 0 {
			t.Error("Expected non-empty root")
		}
		t.Logf("Root after height 1: %x", root)
	})

	// 测试更新
	t.Run("Update", func(t *testing.T) {
		oldRoot := stateDB.Root()

		updates := []KVUpdate{
			{Key: "v1_account_alice", Value: []byte(`{"balance":800}`), Deleted: false},
		}

		err := stateDB.ApplyAccountUpdate(2, updates...)
		if err != nil {
			t.Fatalf("ApplyAccountUpdate failed: %v", err)
		}

		// 验证新值
		val, exists, err := stateDB.Get("v1_account_alice")
		if err != nil || !exists {
			t.Fatalf("Get failed: %v, exists: %v", err, exists)
		}
		if string(val) != `{"balance":800}` {
			t.Errorf("Unexpected value: %s", val)
		}

		// 验证 Root 变化
		newRoot := stateDB.Root()
		if string(oldRoot) == string(newRoot) {
			t.Error("Root should change after update")
		}
		t.Logf("Root after height 2: %x", newRoot)
	})

	// 测试删除
	t.Run("Delete", func(t *testing.T) {
		updates := []KVUpdate{
			{Key: "v1_account_bob", Value: nil, Deleted: true},
		}

		err := stateDB.ApplyAccountUpdate(3, updates...)
		if err != nil {
			t.Fatalf("ApplyAccountUpdate failed: %v", err)
		}

		// 验证删除
		_, exists, err := stateDB.Get("v1_account_bob")
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if exists {
			t.Error("Expected key to be deleted")
		}
	})

	// 测试历史版本查询
	t.Run("HistoricalQuery", func(t *testing.T) {
		// 查询高度 1 的 alice 余额
		val, err := stateDB.GetAtVersion("v1_account_alice", 1)
		if err != nil {
			t.Fatalf("GetAtVersion failed: %v", err)
		}
		if string(val) != `{"balance":1000}` {
			t.Errorf("Expected 1000, got %s", val)
		}

		// 查询高度 2 的 alice 余额
		val, err = stateDB.GetAtVersion("v1_account_alice", 2)
		if err != nil {
			t.Fatalf("GetAtVersion failed: %v", err)
		}
		if string(val) != `{"balance":800}` {
			t.Errorf("Expected 800, got %s", val)
		}
	})
}

func TestJMTStateDB_Proof(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "jmt_statedb_proof_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := JMTConfig{
		DataDir: tmpDir,
		Prefix:  []byte("test:"),
	}
	stateDB, err := NewJMTStateDB(cfg)
	if err != nil {
		t.Fatalf("Failed to create JMTStateDB: %v", err)
	}
	defer stateDB.Close()

	// 插入一些数据
	updates := []KVUpdate{
		{Key: "v1_account_alice", Value: []byte(`{"balance":1000}`), Deleted: false},
		{Key: "v1_account_bob", Value: []byte(`{"balance":500}`), Deleted: false},
		{Key: "v1_token_BTC", Value: []byte(`{"symbol":"BTC"}`), Deleted: false},
	}
	err = stateDB.ApplyAccountUpdate(1, updates...)
	if err != nil {
		t.Fatalf("ApplyAccountUpdate failed: %v", err)
	}

	// 生成 Proof
	t.Run("GenerateAndVerifyProof", func(t *testing.T) {
		proof, err := stateDB.Prove("v1_account_alice")
		if err != nil {
			t.Fatalf("Prove failed: %v", err)
		}

		// 验证 Proof
		root := stateDB.Root()
		if !stateDB.VerifyProof(proof, root) {
			t.Error("Proof verification failed")
		}

		t.Logf("Proof for alice: siblings=%d", len(proof.Siblings))
	})

	// 测试不存在的 key 的 Proof
	t.Run("NonExistenceProof", func(t *testing.T) {
		proof, err := stateDB.Prove("v1_account_nonexistent")
		if err != nil {
			// 不存在的 key 也应该能生成 proof（证明不存在）
			t.Logf("Prove for nonexistent key returned: %v", err)
		} else {
			root := stateDB.Root()
			if proof != nil && !stateDB.VerifyProof(proof, root) {
				t.Error("Non-existence proof verification failed")
			}
		}
	})
}

func TestJMTStateDB_WithExistingDB(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "jmt_statedb_existing_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// 先打开一个 BadgerDB
	opts := badger.DefaultOptions(tmpDir).WithLogger(nil)
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatalf("Failed to open badger: %v", err)
	}

	// 使用已有的 DB 创建 JMTStateDB
	cfg := JMTConfig{
		Prefix: []byte("state:"),
	}
	stateDB, err := NewJMTStateDBWithDB(db, cfg)
	if err != nil {
		db.Close()
		t.Fatalf("Failed to create JMTStateDB: %v", err)
	}

	// 写入数据
	updates := []KVUpdate{
		{Key: "v1_account_test", Value: []byte("value1"), Deleted: false},
	}
	err = stateDB.ApplyAccountUpdate(1, updates...)
	if err != nil {
		t.Fatalf("ApplyAccountUpdate failed: %v", err)
	}

	// 记录根哈希
	root1 := stateDB.Root()
	t.Logf("Root: %x", root1)

	// 关闭 JMTStateDB
	stateDB.Close()

	// 关闭 DB
	db.Close()

	// 重新打开
	db, err = badger.Open(opts)
	if err != nil {
		t.Fatalf("Failed to reopen badger: %v", err)
	}
	defer db.Close()

	stateDB2, err := NewJMTStateDBWithDB(db, cfg)
	if err != nil {
		t.Fatalf("Failed to recreate JMTStateDB: %v", err)
	}
	defer stateDB2.Close()

	// 验证恢复的版本和根哈希
	if stateDB2.Version() != 1 {
		t.Errorf("Expected version 1 after recovery, got %d", stateDB2.Version())
	}

	root2 := stateDB2.Root()
	if string(root1) != string(root2) {
		t.Errorf("Root mismatch after recovery: %x vs %x", root1, root2)
	}

	// 验证数据仍然存在
	val, exists, err := stateDB2.Get("v1_account_test")
	if err != nil || !exists {
		t.Fatalf("Data not recovered: err=%v, exists=%v", err, exists)
	}
	if string(val) != "value1" {
		t.Errorf("Recovered value mismatch: %s", val)
	}
}

func TestJMTStateDB_BatchUpdate(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "jmt_statedb_batch_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := JMTConfig{
		DataDir: tmpDir,
		Prefix:  []byte("test:"),
	}
	stateDB, err := NewJMTStateDB(cfg)
	if err != nil {
		t.Fatalf("Failed to create JMTStateDB: %v", err)
	}
	defer stateDB.Close()

	// 批量插入 100 个账户
	updates := make([]KVUpdate, 100)
	for i := 0; i < 100; i++ {
		updates[i] = KVUpdate{
			Key:     "v1_account_user_" + string(rune('A'+i%26)) + string(rune('0'+i/26)),
			Value:   []byte(`{"balance":` + string(rune('0'+i%10)) + `000}`),
			Deleted: false,
		}
	}

	err = stateDB.ApplyAccountUpdate(1, updates...)
	if err != nil {
		t.Fatalf("Batch ApplyAccountUpdate failed: %v", err)
	}

	// 验证所有数据都存在
	for i := 0; i < 10; i++ { // 抽查 10 个
		key := updates[i*10].Key
		_, exists, err := stateDB.Get(key)
		if err != nil || !exists {
			t.Errorf("Key %s not found: err=%v", key, err)
		}
	}

	t.Logf("Successfully inserted 100 accounts, version=%d, root=%x",
		stateDB.Version(), stateDB.Root())
}

func TestJMTStateDB_Exists(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "jmt_statedb_exists_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := JMTConfig{
		DataDir: tmpDir,
	}
	stateDB, err := NewJMTStateDB(cfg)
	if err != nil {
		t.Fatalf("Failed to create JMTStateDB: %v", err)
	}
	defer stateDB.Close()

	// 插入数据
	updates := []KVUpdate{
		{Key: "key1", Value: []byte("value1"), Deleted: false},
	}
	stateDB.ApplyAccountUpdate(1, updates...)

	// 测试存在的 key
	exists, err := stateDB.Exists("key1")
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Error("Expected key1 to exist")
	}

	// 测试不存在的 key
	exists, err = stateDB.Exists("key_nonexistent")
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if exists {
		t.Error("Expected key_nonexistent to not exist")
	}
}

func TestJMTStateDB_FlushAndRotate(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "jmt_statedb_flush_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := JMTConfig{
		DataDir: tmpDir,
	}
	stateDB, err := NewJMTStateDB(cfg)
	if err != nil {
		t.Fatalf("Failed to create JMTStateDB: %v", err)
	}
	defer stateDB.Close()

	// FlushAndRotate 应该是空操作（兼容接口）
	err = stateDB.FlushAndRotate(40000)
	if err != nil {
		t.Errorf("FlushAndRotate failed: %v", err)
	}
}
