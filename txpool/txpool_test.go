package txpool

import (
	"awesomeProject1/db"
	"testing"
	"time"
)

// TestGetTxsByTargetHeight 测试从TxPool中根据目标高度获取交易
func TestGetTxsByTargetHeight(t *testing.T) {
	// 1. 获取或创建 TxPool 实例
	//    如果项目里只能用单例 GetInstance()，则直接调用：
	txPool := GetInstance()

	// 如果想在测试环境中使用一个空/临时DB来保证测试的纯净性，
	// 可以自行mock或者使用 t.TempDir() 做临时目录来初始化 DB。
	// 这里为简化，直接使用默认的 singleton：

	// 2. 构造若干交易，模拟不同的 target_height
	tx1 := &db.AnyTx{
		Content: &db.AnyTx_Transaction{
			Transaction: &db.Transaction{
				Base: &db.BaseMessage{
					TxId:         "tx_1001",
					FromAddress:  "addr_1",
					TargetHeight: 100,
				},
				To:           "addr_2",
				TokenAddress: "FB",
				Amount:       "10",
			},
		},
	}
	tx2 := &db.AnyTx{
		Content: &db.AnyTx_Transaction{
			Transaction: &db.Transaction{
				Base: &db.BaseMessage{
					TxId:         "tx_1002",
					FromAddress:  "addr_3",
					TargetHeight: 100,
				},
				To:           "addr_4",
				TokenAddress: "FB",
				Amount:       "50",
			},
		},
	}
	tx3 := &db.AnyTx{
		Content: &db.AnyTx_Transaction{
			Transaction: &db.Transaction{
				Base: &db.BaseMessage{
					TxId:         "tx_2001",
					FromAddress:  "addr_5",
					TargetHeight: 200,
				},
				To:           "addr_6",
				TokenAddress: "FB",
				Amount:       "80",
			},
		},
	}

	// 3. 将它们存入 TxPool
	err1 := txPool.StoreAnyTx(tx1)
	err2 := txPool.StoreAnyTx(tx2)
	err3 := txPool.StoreAnyTx(tx3)

	if err1 != nil || err2 != nil || err3 != nil {
		t.Fatalf("failed to store tx into txPool: err1=%v, err2=%v, err3=%v", err1, err2, err3)
	}

	// 4. 调用GetTxsByTargetHeight(100) 看看是否只返回 tx1 / tx2
	result100 := txPool.GetTxsByTargetHeight(100)
	if len(result100) != 2 {
		t.Errorf("expected 2 tx for height=100, got %d", len(result100))
	} else {
		// 再检查一下具体TxId是否符合
		foundTxIds := map[string]bool{}
		for _, tx := range result100 {
			foundTxIds[tx.GetTxId()] = true
		}
		if !foundTxIds["tx_1001"] || !foundTxIds["tx_1002"] {
			t.Errorf("result for height=100 missing expected txId, got: %v", foundTxIds)
		}
	}

	// 5. 调用GetTxsByTargetHeight(200) 看看是否只返回 tx3
	result200 := txPool.GetTxsByTargetHeight(200)
	if len(result200) != 1 {
		t.Errorf("expected 1 tx for height=200, got %d", len(result200))
	} else {
		if result200[0].GetTxId() != "tx_2001" {
			t.Errorf("result for height=200 is not tx_2001, got: %s", result200[0].GetTxId())
		}
	}

	// 6. 调用GetTxsByTargetHeight(999) 看是否返回空
	result999 := txPool.GetTxsByTargetHeight(999)
	if len(result999) != 0 {
		t.Errorf("expected 0 tx for height=999, got %d", len(result999))
	}
}

// TestSortTxsByFBBalanceAndComputeHash 测试按发起人FB余额排序并计算txs_hash
func TestSortTxsByFBBalanceAndComputeHash(t *testing.T) {

	// 2. 初始化 DB
	dbMgr, err := db.GetInstance("D:\\awesome\\data")
	if err != nil {
		t.Fatalf("failed to init db Manager: %v", err)
	}
	// 记得在测试结束时 close
	defer dbMgr.Close()
	dbMgr.InitWriteQueue(65000, 100*time.Millisecond)
	// 3. 创建一些账号，并设置它们的FB余额
	//    例如：addr_1 => FB=100, addr_2 => FB=50, addr_3 => FB=5
	mustSaveAccountWithFBBalance(t, dbMgr, "addr_1", "100")
	mustSaveAccountWithFBBalance(t, dbMgr, "addr_2", "50")
	mustSaveAccountWithFBBalance(t, dbMgr, "addr_3", "5")
	mustSaveAccountWithFBBalance(t, dbMgr, "addr_4", "1000") // 用于测试极大余额
	time.Sleep(1 * time.Second)
	// 4. 构造几笔 AnyTx (from=这些地址)
	txs := []*db.AnyTx{
		makeTestAnyTx("txid_1", "addr_3"), // FB=5
		makeTestAnyTx("txid_2", "addr_2"), // FB=50
		makeTestAnyTx("txid_3", "addr_1"), // FB=100
		makeTestAnyTx("txid_4", "addr_4"), // FB=1000
		makeTestAnyTx("txid_5", "addr_2"), // FB=50
	}

	// 5. 调用要测试的函数
	sortedTxs, txsHash, err := SortTxsByFBBalanceAndComputeHash(dbMgr, txs)
	if err != nil {
		t.Fatalf("SortTxsByFBBalanceAndComputeHash error: %v", err)
	}

	// 6. 检查排序结果: 期望顺序 (addr_4 => 1000), (addr_1 => 100), (addr_2 => 50), (addr_2 => 50), (addr_3 => 5)
	//    实际中如果两笔交易from相同余额，就看你是否需要稳定排序，但一般只要顺序在正确的大区间即可。
	if len(sortedTxs) != len(txs) {
		t.Errorf("Expected %d txs after sort, got %d", len(txs), len(sortedTxs))
	}

	wantOrder := []string{"addr_4", "addr_1", "addr_2", "addr_2", "addr_3"}
	for i, tx := range sortedTxs {
		gotFrom := tx.GetBase().FromAddress
		if gotFrom != wantOrder[i] {
			t.Errorf("index %d want from=%s, got %s", i, wantOrder[i], gotFrom)
		}
	}

	// 7. 检查 txs_hash 是否非空
	if txsHash == "" {
		t.Error("expected non-empty txs_hash, got empty string")
	} else {
		t.Logf("txs_hash = %s", txsHash)
	}
}

// ------------------------------------------------------------------
// 辅助函数
// ------------------------------------------------------------------

// mustSaveAccountWithFBBalance 用于简化在测试中保存账户
func mustSaveAccountWithFBBalance(t *testing.T, dbMgr *db.Manager, addr string, fbBalStr string) {
	t.Helper()

	acc := &db.Account{
		Address:  addr,
		Balances: map[string]*db.TokenBalance{},
	}
	if acc.Balances["FB"] == nil {
		acc.Balances["FB"] = &db.TokenBalance{}
	}
	acc.Balances["FB"].Balance = fbBalStr

	// 保存
	if err := db.SaveAccount(dbMgr, acc); err != nil {
		t.Fatalf("SaveAccount for %s error: %v", addr, err)
	}
	t.Logf("txs_hash ")
}

// makeTestAnyTx 辅助函数：简化构造AnyTx
func makeTestAnyTx(txID, fromAddr string) *db.AnyTx {
	return &db.AnyTx{
		Content: &db.AnyTx_Transaction{
			Transaction: &db.Transaction{
				Base: &db.BaseMessage{
					TxId:        txID,
					FromAddress: fromAddr,
				},
				// 其它字段可以省略或随意
				TokenAddress: "FB",
				Amount:       "10",
			},
		},
	}
}
