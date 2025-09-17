package db

import (
	"fmt"
	"os"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
)

// 测试 SaveTransaction 和 GetTransaction
func TestSaveAndGetTransaction(t *testing.T) {
	mgr, err := NewManager("./data")
	if err != nil {
		t.Fatalf("Failed to get DB instance: %v", err)
	}
	defer mgr.Close()

	tx := &Transaction{
		Base: &BaseMessage{
			TxId:         "tx12345",
			FromAddress:  "addr1",
			TargetHeight: 100,
			PublicKey:    "pubkey1",
			Signature:    "sig1",
			Nonce:        1,
		},
		To:     "addr2",
		Amount: "1000",
	}

	// Save transaction
	if err := SaveTransaction(mgr, tx); err != nil {
		t.Fatalf("SaveTransaction failed: %v", err)
	}

	// Retrieve transaction
	loadedTx, err := GetTransaction(mgr, "tx12345")
	if err != nil {
		t.Fatalf("GetTransaction failed: %v", err)
	}

	// Compare the saved and loaded transactions
	if !proto.Equal(tx, loadedTx) {
		t.Errorf("Loaded transaction does not match saved transaction. Got: %+v, Expected: %+v", loadedTx, tx)
	}
}

// 测试 SaveAccount 和 GetAccount
func TestSaveAndGetAccount(t *testing.T) {
	mgr, err := NewManager("./data")
	if err != nil {
		t.Fatalf("Failed to get DB instance: %v", err)
	}
	defer mgr.Close()

	account := &Account{
		Address: "addr1",
		Nonce:   1,
		Orders:  []string{"order1", "order2"},
	}

	// Save account
	if err := SaveAccount(mgr, account); err != nil {
		t.Fatalf("SaveAccount failed: %v", err)
	}

	// Retrieve account
	loadedAccount, err := GetAccount(mgr, "addr1")
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}

	// Compare the saved and loaded accounts
	if !proto.Equal(account, loadedAccount) {
		t.Errorf("Loaded account does not match saved account. Got: %+v, Expected: %+v", loadedAccount, account)
	}
}
func TestWriteBatch65000(t *testing.T) {
	testDBPath := "./test_data_for_writebatch"
	os.RemoveAll(testDBPath)
	defer os.RemoveAll(testDBPath)

	mgr, err := NewManager(testDBPath)
	if err != nil {
		t.Fatalf("Failed to get DB manager: %v", err)
	}
	// 不要再 defer mgr.Close() 了，因为我们想手动控制关闭时机
	// defer mgr.Close()

	// 初始化队列：假设一次最多一万条，200ms 超时强制写
	mgr.InitWriteQueue(10000, 200*time.Millisecond)

	// 1.开始计时
	start := time.Now()

	// 2. 依次放入 65000 个写请求
	const N = 65000
	for i := 0; i < N; i++ {
		key := fmt.Sprintf("batch_key_%06d", i)
		val := fmt.Sprintf("batch_val_%06d", i)
		mgr.EnqueueSet(key, val)
	}

	// 3. 调用 Close()，它会：
	//    - 通知队列 goroutine 停止
	//    - 等待 goroutine flush 掉所有待写数据
	//    - 关闭数据库
	mgr.Close()

	// 4. 此时所有 65000 笔已经写入完毕，停止计时
	elapsed := time.Since(start)
	t.Logf("WriteBatch(65000 entries) took: %s", elapsed)
}
