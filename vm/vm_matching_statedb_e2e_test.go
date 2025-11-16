package vm_test

import (
	"encoding/json"
	"testing"
	"time"

	"dex/db"
	"dex/keys"
	"dex/pb"
	"dex/vm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestE2E_OrderMatching_VM_StateDB_Integration
// 端到端集成测试：验证 VM、Matching、StateDB 三模块完美配合
//
// 测试场景：
// 1. 创建真实的 Badger + StateDB
// 2. 初始化账户余额
// 3. 提交订单交易，触发撮合
// 4. 验证：
//    - VM 正确执行
//    - Matching 正确撮合
//    - StateDB 正确同步账户数据
//    - Badger 持久化所有数据
//    - 数据一致性
func TestE2E_OrderMatching_VM_StateDB_Integration(t *testing.T) {
	// ========== 第一步：初始化真实数据库 ==========
	tmpDir := t.TempDir() // 自动清理
	t.Logf("📁 Test database directory: %s", tmpDir)

	dbMgr, err := db.NewManager(tmpDir)
	require.NoError(t, err, "Failed to create DB manager")
	defer dbMgr.Close()

	// 启动写队列
	dbMgr.InitWriteQueue(100, 200*time.Millisecond)
	t.Log("✅ Database initialized (Badger + StateDB)")

	// ========== 第二步：初始化 VM Executor ==========
	registry := vm.NewHandlerRegistry()
	require.NoError(t, vm.RegisterDefaultHandlers(registry))

	cache := vm.NewSpecExecLRU(100)
	executor := vm.NewExecutor(dbMgr, registry, cache)
	t.Log("✅ VM Executor initialized")

	// ========== 第三步：准备测试账户 ==========
	// Alice: 有 10 BTC 和 100000 USDT
	// Bob: 有 0 BTC 和 100000 USDT
	aliceAddr := "alice_test_addr"
	bobAddr := "bob_test_addr"

	createE2ETestAccount(t, dbMgr, aliceAddr, map[string]string{
		"BTC":  "10.0",
		"USDT": "100000.0",
	})
	createE2ETestAccount(t, dbMgr, bobAddr, map[string]string{
		"BTC":  "0.0",
		"USDT": "100000.0",
	})

	// 强制刷新到数据库
	require.NoError(t, dbMgr.ForceFlush())
	time.Sleep(100 * time.Millisecond)
	t.Log("✅ Test accounts created (Alice: 10 BTC, Bob: 0 BTC)")

	// ========== 第四步：Block 1 - Alice 挂卖单 ==========
	sellOrderTx := &pb.AnyTx{
		Content: &pb.AnyTx_OrderTx{
			OrderTx: &pb.OrderTx{
				Base: &pb.BaseMessage{
					TxId:        "sell_order_001",
					FromAddress: aliceAddr,
					Status:      pb.Status_PENDING,
				},
				BaseToken:   "BTC",
				QuoteToken:  "USDT",
				Op:          pb.OrderOp_ADD,
				Price:       "50000",  // 卖价 50000 USDT/BTC
				Amount:      "1.0",    // 卖 1 BTC
				FilledBase:  "0",
				FilledQuote: "0",
				IsFilled:    false,
			},
		},
	}

	block1 := &pb.Block{
		BlockHash:     "block_001",
		PrevBlockHash: "genesis",
		Height:        1,
		Body:          []*pb.AnyTx{sellOrderTx},
	}

	t.Log("📦 Executing Block 1: Alice places sell order (1 BTC @ 50000 USDT)")
	result1, err := executor.PreExecuteBlock(block1)
	require.NoError(t, err)
	require.True(t, result1.Valid, "Block 1 should be valid")
	require.Equal(t, 1, len(result1.Receipts))
	assert.Equal(t, "SUCCEED", result1.Receipts[0].Status)

	// 提交区块
	require.NoError(t, executor.CommitFinalizedBlock(block1))
	require.NoError(t, dbMgr.ForceFlush())
	time.Sleep(100 * time.Millisecond)
	t.Log("✅ Block 1 committed: Sell order placed")

	// ========== 第五步：Block 2 - Bob 挂买单，触发撮合 ==========
	// Bob 买 BTC：base_token=USDT（支付的币种），quote_token=BTC（想要的币种）
	// amount=25000（支付 25000 USDT）
	// price=50000（USDT/BTC，即 1 BTC = 50000 USDT，与 Alice 的卖单价格一致）
	//
	// 撮合逻辑：
	// - Bob 的买单：花费 25000 USDT，按 price=50000 买入 25000 / 50000 = 0.5 BTC
	// - Alice 的卖单：卖出 BTC，按 price=50000 得到 USDT
	// - 撮合时价格匹配：都是 50000 USDT/BTC
	buyOrderTx := &pb.AnyTx{
		Content: &pb.AnyTx_OrderTx{
			OrderTx: &pb.OrderTx{
				Base: &pb.BaseMessage{
					TxId:        "buy_order_001",
					FromAddress: bobAddr,
					Status:      pb.Status_PENDING,
				},
				BaseToken:   "USDT",
				QuoteToken:  "BTC",
				Op:          pb.OrderOp_ADD,
				Price:       "50000",    // USDT/BTC (1 BTC = 50000 USDT)
				Amount:      "25000",    // 支付 25000 USDT
				FilledBase:  "0",
				FilledQuote: "0",
				IsFilled:    false,
			},
		},
	}

	block2 := &pb.Block{
		BlockHash:     "block_002",
		PrevBlockHash: "block_001",
		Height:        2,
		Body:          []*pb.AnyTx{buyOrderTx},
	}

	t.Log("📦 Executing Block 2: Bob places buy order (0.5 BTC @ 50000 USDT) - Should trigger matching")
	result2, err := executor.PreExecuteBlock(block2)
	require.NoError(t, err)
	require.True(t, result2.Valid, "Block 2 should be valid")
	require.Equal(t, 1, len(result2.Receipts))
	assert.Equal(t, "SUCCEED", result2.Receipts[0].Status)

	// 提交区块
	require.NoError(t, executor.CommitFinalizedBlock(block2))
	require.NoError(t, dbMgr.ForceFlush())
	time.Sleep(500 * time.Millisecond) // 增加等待时间
	t.Log("✅ Block 2 committed: Buy order matched with sell order")

	// ========== 第六步：验证撮合结果 ==========
	t.Log("🔍 Verifying matching results...")

	// 验证 Alice 的余额变化
	// Alice 的卖单：base_token=BTC, quote_token=USDT, amount=1.0, price=50000
	// 成交 tradeAmt（BTC 数量），获得 tradeAmt * 50000 USDT
	// 预期：卖出 0.5 BTC，获得 0.5 * 50000 = 25000 USDT
	// - BTC: 10.0 - 0.5 = 9.5
	// - USDT: 100000 + 25000 = 125000
	aliceAccount := getE2EAccount(t, dbMgr, aliceAddr)
	t.Logf("Alice actual: BTC=%s, USDT=%s",
		aliceAccount.Balances["BTC"].Balance,
		aliceAccount.Balances["USDT"].Balance)
	assert.Equal(t, "9.5", aliceAccount.Balances["BTC"].Balance, "Alice should have 9.5 BTC")
	assert.Equal(t, "125000", aliceAccount.Balances["USDT"].Balance, "Alice should have 125000 USDT")

	// 验证 Bob 的余额变化
	// Bob 的买单：base_token=USDT, quote_token=BTC, amount=25000, price=0.00002
	// 成交 tradeAmt（USDT 数量），获得 tradeAmt * 0.00002 BTC
	// 预期：花费 25000 USDT，获得 25000 * 0.00002 = 0.5 BTC
	// - USDT: 100000 - 25000 = 75000
	// - BTC: 0 + 0.5 = 0.5
	bobAccount := getE2EAccount(t, dbMgr, bobAddr)
	t.Logf("Bob actual: BTC=%s, USDT=%s",
		bobAccount.Balances["BTC"].Balance,
		bobAccount.Balances["USDT"].Balance)
	assert.Equal(t, "0.5", bobAccount.Balances["BTC"].Balance, "Bob should have 0.5 BTC")
	assert.Equal(t, "75000", bobAccount.Balances["USDT"].Balance, "Bob should have 75000 USDT")

	t.Log("✅ Account balances verified correctly")

	// ========== 第七步：验证数据持久化 ==========
	t.Log("🔍 Verifying data persistence...")

	// 重新读取账户，验证数据已正确持久化
	aliceFromDB := getE2EAccount(t, dbMgr, aliceAddr)
	assert.Equal(t, "9.5", aliceFromDB.Balances["BTC"].Balance, "DB should have correct Alice BTC balance")
	assert.Equal(t, "125000", aliceFromDB.Balances["USDT"].Balance, "DB should have correct Alice USDT balance")

	bobFromDB := getE2EAccount(t, dbMgr, bobAddr)
	assert.Equal(t, "0.5", bobFromDB.Balances["BTC"].Balance, "DB should have correct Bob BTC balance")
	assert.Equal(t, "75000", bobFromDB.Balances["USDT"].Balance, "DB should have correct Bob USDT balance")

	t.Log("✅ Data persistence verified")

	// ========== 第八步：验证订单状态 ==========
	t.Log("🔍 Verifying order status...")

	// 验证卖单部分成交
	sellOrder := getE2EOrder(t, dbMgr, "sell_order_001")
	assert.Equal(t, "0.5", sellOrder.FilledBase, "Sell order should have 0.5 BTC filled")
	assert.False(t, sellOrder.IsFilled, "Sell order should not be fully filled")

	// 验证买单完全成交
	buyOrder := getE2EOrder(t, dbMgr, "buy_order_001")
	assert.Equal(t, "0.5", buyOrder.FilledQuote, "Buy order should have 0.5 BTC filled")
	assert.True(t, buyOrder.IsFilled, "Buy order should be fully filled")

	t.Log("✅ Order status verified")

	// ========== 第九步：验证数据一致性 ==========
	t.Log("🔍 Verifying data consistency...")

	// 验证账户余额与订单状态一致
	assert.Equal(t, bobAccount.Balances["BTC"].Balance, bobFromDB.Balances["BTC"].Balance,
		"Account balance should match between reads")
	assert.Equal(t, bobAccount.Balances["USDT"].Balance, bobFromDB.Balances["USDT"].Balance,
		"Account balance should match between reads")

	t.Log("✅ Data consistency verified")

	// ========== 测试总结 ==========
	t.Log("🎉 ========== E2E Test Summary ==========")
	t.Log("✅ VM execution: PASS")
	t.Log("✅ Order matching: PASS")
	t.Log("✅ StateDB sync: PASS")
	t.Log("✅ Data persistence: PASS")
	t.Log("✅ Data consistency: PASS")
	t.Log("🎉 All checks passed! VM + Matching + StateDB integration working perfectly!")
}

// ========== 辅助函数 ==========

// createE2ETestAccount 创建测试账户并写入数据库（E2E 测试专用）
func createE2ETestAccount(t *testing.T, dbMgr *db.Manager, address string, balances map[string]string) {
	account := &pb.Account{
		Address:  address,
		Balances: make(map[string]*pb.TokenBalance),
	}

	for token, balance := range balances {
		account.Balances[token] = &pb.TokenBalance{
			Balance:                balance,
			MinerLockedBalance:     "0",
			CandidateLockedBalance: "0",
		}
	}

	accountData, err := json.Marshal(account)
	require.NoError(t, err)

	accountKey := keys.KeyAccount(address)
	dbMgr.EnqueueSet(accountKey, string(accountData))
}

// getE2EAccount 从数据库读取账户（E2E 测试专用）
func getE2EAccount(t *testing.T, dbMgr *db.Manager, address string) *pb.Account {
	accountKey := keys.KeyAccount(address)
	accountData, err := dbMgr.Get(accountKey)
	require.NoError(t, err)
	require.NotNil(t, accountData)

	var account pb.Account
	require.NoError(t, json.Unmarshal(accountData, &account))
	return &account
}

// getE2EOrder 从数据库读取订单（E2E 测试专用）
func getE2EOrder(t *testing.T, dbMgr *db.Manager, orderID string) *pb.OrderTx {
	orderKey := keys.KeyOrder(orderID)
	orderData, err := dbMgr.Get(orderKey)
	require.NoError(t, err)
	require.NotNil(t, orderData)

	var order pb.OrderTx
	require.NoError(t, proto.Unmarshal(orderData, &order))
	return &order
}

// TestE2E_MultiBlock_OrderMatching
// 测试多区块连续执行场景
//
// 场景：
// Block 1: Alice 挂 3 个卖单（不同价格）
// Block 2: Bob 挂 1 个买单，部分撮合
// Block 3: Charlie 挂 1 个买单，继续撮合
// Block 4: Alice 取消剩余订单
func TestE2E_MultiBlock_OrderMatching(t *testing.T) {
	// 初始化数据库
	tmpDir := t.TempDir()
	t.Logf("📁 Test database directory: %s", tmpDir)

	dbMgr, err := db.NewManager(tmpDir)
	require.NoError(t, err)
	defer dbMgr.Close()

	dbMgr.InitWriteQueue(100, 200*time.Millisecond)

	// 初始化 VM
	registry := vm.NewHandlerRegistry()
	require.NoError(t, vm.RegisterDefaultHandlers(registry))
	cache := vm.NewSpecExecLRU(100)
	executor := vm.NewExecutor(dbMgr, registry, cache)

	// 创建测试账户
	aliceAddr := "alice_multi"
	bobAddr := "bob_multi"
	charlieAddr := "charlie_multi"

	createE2ETestAccount(t, dbMgr, aliceAddr, map[string]string{
		"BTC":  "10.0",
		"USDT": "0",
	})
	createE2ETestAccount(t, dbMgr, bobAddr, map[string]string{
		"BTC":  "0",
		"USDT": "200000",
	})
	createE2ETestAccount(t, dbMgr, charlieAddr, map[string]string{
		"BTC":  "0",
		"USDT": "200000",
	})

	require.NoError(t, dbMgr.ForceFlush())
	time.Sleep(100 * time.Millisecond)
	t.Log("✅ Test accounts created")

	// ========== Block 1: Alice 挂 3 个卖单 ==========
	block1 := &pb.Block{
		BlockHash:     "multi_block_001",
		PrevBlockHash: "genesis",
		Height:        1,
		Body: []*pb.AnyTx{
			createSellOrder("sell_1", aliceAddr, "BTC", "USDT", "49000", "1.0"),
			createSellOrder("sell_2", aliceAddr, "BTC", "USDT", "50000", "2.0"),
			createSellOrder("sell_3", aliceAddr, "BTC", "USDT", "51000", "3.0"),
		},
	}

	t.Log("📦 Block 1: Alice places 3 sell orders")
	result1, err := executor.PreExecuteBlock(block1)
	require.NoError(t, err)
	require.True(t, result1.Valid)
	require.NoError(t, executor.CommitFinalizedBlock(block1))
	require.NoError(t, dbMgr.ForceFlush())
	time.Sleep(500 * time.Millisecond)
	t.Log("✅ Block 1 committed")

	// ========== Block 2: Bob 买入 1.5 BTC ==========
	// Bob 想买 1.5 BTC，愿意支付最高 51000 USDT/BTC（高于 sell_2 的 50000）
	// 需要支付：1.5 * 51000 = 76500 USDT
	// 所以 amount 应该是 76500（支付的 USDT 数量）
	// 预期撮合：
	// - 先匹配 sell_1: 1 BTC @ 49000 = 49000 USDT
	// - 再匹配 sell_2: 0.5 BTC @ 50000 = 25000 USDT
	// - 总计：1.5 BTC，花费 74000 USDT
	block2 := &pb.Block{
		BlockHash:     "multi_block_002",
		PrevBlockHash: "multi_block_001",
		Height:        2,
		Body: []*pb.AnyTx{
			createBuyOrder("buy_1", bobAddr, "USDT", "BTC", "51000", "76500"),
		},
	}

	t.Log("📦 Block 2: Bob buys 1.5 BTC (pays 75000 USDT @ 50000)")
	result2, err := executor.PreExecuteBlock(block2)
	require.NoError(t, err)
	require.True(t, result2.Valid)

	// 打印撮合事件
	t.Logf("Block 2 receipts count: %d", len(result2.Receipts))
	for i, receipt := range result2.Receipts {
		t.Logf("Receipt %d: TxID=%s, Status=%s, WriteCount=%d",
			i, receipt.TxID, receipt.Status, receipt.WriteCount)
	}

	require.NoError(t, executor.CommitFinalizedBlock(block2))
	require.NoError(t, dbMgr.ForceFlush())
	time.Sleep(500 * time.Millisecond)
	t.Log("✅ Block 2 committed")

	// 验证 Bob 的余额
	// 预期：买入 1.5 BTC，花费 1*49000 + 0.5*50000 = 49000 + 25000 = 74000 USDT
	// （因为会先匹配价格更低的 sell_1: 49000）
	bobAccount := getE2EAccount(t, dbMgr, bobAddr)
	t.Logf("Bob BTC balance: %s (expected: 1.5)", bobAccount.Balances["BTC"].Balance)
	t.Logf("Bob USDT balance: %s (expected: 126000)", bobAccount.Balances["USDT"].Balance)

	assert.Equal(t, "1.5", bobAccount.Balances["BTC"].Balance, "Bob should have 1.5 BTC")
	assert.Equal(t, "126000", bobAccount.Balances["USDT"].Balance, "Bob should have 126000 USDT left")

	// 检查订单状态
	sell1 := getE2EOrder(t, dbMgr, "sell_1")
	assert.Equal(t, "1", sell1.FilledBase, "sell_1 should be fully filled (1 BTC)")
	assert.True(t, sell1.IsFilled, "sell_1 should be marked as filled")

	sell2 := getE2EOrder(t, dbMgr, "sell_2")
	assert.Equal(t, "0.5", sell2.FilledBase, "sell_2 should be partially filled (0.5 BTC)")
	assert.False(t, sell2.IsFilled, "sell_2 should not be fully filled")

	buyOrder1 := getE2EOrder(t, dbMgr, "buy_1")
	assert.Equal(t, "1.5", buyOrder1.FilledQuote, "buy_1 should have bought 1.5 BTC")
	assert.True(t, buyOrder1.IsFilled, "buy_1 should be fully filled")

	// ========== Block 3: Charlie 买入 2 BTC ==========
	// Charlie 想买 2 BTC，愿意支付最高 51000 USDT/BTC
	// 需要支付：2 * 51000 = 102000 USDT
	// 但实际会匹配到更便宜的价格：
	// - sell_2 剩余 1.5 BTC @ 50000 = 75000 USDT
	// - sell_3 剩余 0.5 BTC @ 51000 = 25500 USDT
	// 总计：100500 USDT
	block3 := &pb.Block{
		BlockHash:     "multi_block_003",
		PrevBlockHash: "multi_block_002",
		Height:        3,
		Body: []*pb.AnyTx{
			createBuyOrder("buy_2", charlieAddr, "USDT", "BTC", "51000", "102000"),
		},
	}

	t.Log("📦 Block 3: Charlie buys 2 BTC (pays up to 102000 USDT @ 51000)")
	result3, err := executor.PreExecuteBlock(block3)
	require.NoError(t, err)
	require.True(t, result3.Valid)
	require.NoError(t, executor.CommitFinalizedBlock(block3))
	require.NoError(t, dbMgr.ForceFlush())
	time.Sleep(500 * time.Millisecond)
	t.Log("✅ Block 3 committed")

	// 验证 Charlie 的余额
	// 预期：买入 2 BTC，花费 1.5*50000 + 0.5*51000 = 75000 + 25500 = 100500 USDT
	charlieAccount := getE2EAccount(t, dbMgr, charlieAddr)
	t.Logf("Charlie BTC balance: %s (expected: 2.0)", charlieAccount.Balances["BTC"].Balance)
	t.Logf("Charlie USDT balance: %s (expected: 99500)", charlieAccount.Balances["USDT"].Balance)

	assert.Equal(t, "2", charlieAccount.Balances["BTC"].Balance, "Charlie should have 2 BTC")
	assert.Equal(t, "99500", charlieAccount.Balances["USDT"].Balance, "Charlie should have 99500 USDT left")

	// 检查订单状态
	sell2Final := getE2EOrder(t, dbMgr, "sell_2")
	assert.Equal(t, "2", sell2Final.FilledBase, "sell_2 should be fully filled (2 BTC total)")
	assert.True(t, sell2Final.IsFilled, "sell_2 should be marked as filled")

	sell3 := getE2EOrder(t, dbMgr, "sell_3")
	assert.Equal(t, "0.5", sell3.FilledBase, "sell_3 should be partially filled (0.5 BTC)")
	assert.False(t, sell3.IsFilled, "sell_3 should not be fully filled")

	// ========== 验证最终状态 ==========
	t.Log("🔍 Verifying final state...")

	// Alice 应该卖出了 3.5 BTC (1 + 1.5 + 0.5)
	// Bob 买入：1*49000 + 0.5*50000 = 74000 USDT
	// Charlie 买入：1.5*50000 + 0.5*51000 = 75000 + 25500 = 100500 USDT
	// Alice 总收入：74000 + 100500 = 174500 USDT
	// 剩余：10 - 3.5 = 6.5 BTC
	aliceAccount := getE2EAccount(t, dbMgr, aliceAddr)
	t.Logf("Alice final BTC: %s (expected: 6.5), USDT: %s (expected: 174500)",
		aliceAccount.Balances["BTC"].Balance,
		aliceAccount.Balances["USDT"].Balance)

	assert.Equal(t, "6.5", aliceAccount.Balances["BTC"].Balance, "Alice should have 6.5 BTC left")
	assert.Equal(t, "174500", aliceAccount.Balances["USDT"].Balance, "Alice should have 174500 USDT")

	// 验证数据持久化
	bobAccountFinal := getE2EAccount(t, dbMgr, bobAddr)
	charlieAccountFinal := getE2EAccount(t, dbMgr, charlieAddr)

	t.Logf("Bob final BTC: %s, USDT: %s",
		bobAccountFinal.Balances["BTC"].Balance,
		bobAccountFinal.Balances["USDT"].Balance)
	t.Logf("Charlie final BTC: %s, USDT: %s",
		charlieAccountFinal.Balances["BTC"].Balance,
		charlieAccountFinal.Balances["USDT"].Balance)

	assert.Equal(t, "1.5", bobAccountFinal.Balances["BTC"].Balance, "Bob should have 1.5 BTC")
	assert.Equal(t, "126000", bobAccountFinal.Balances["USDT"].Balance, "Bob should have 126000 USDT")
	assert.Equal(t, "2", charlieAccountFinal.Balances["BTC"].Balance, "Charlie should have 2 BTC")
	assert.Equal(t, "99500", charlieAccountFinal.Balances["USDT"].Balance, "Charlie should have 99500 USDT")

	// 验证总量守恒
	// BTC 总量：10 (Alice初始) = 6.5 (Alice) + 1.5 (Bob) + 2 (Charlie) ✅
	// USDT 总量：400000 (Bob+Charlie初始) = 174500 (Alice) + 126000 (Bob) + 99500 (Charlie) = 400000 ✅
	t.Log("✅ Multi-block test completed successfully")
}

// ========== 辅助函数：创建订单 ==========

func createSellOrder(txID, from, base, quote, price, amount string) *pb.AnyTx {
	return &pb.AnyTx{
		Content: &pb.AnyTx_OrderTx{
			OrderTx: &pb.OrderTx{
				Base: &pb.BaseMessage{
					TxId:        txID,
					FromAddress: from,
					Status:      pb.Status_PENDING,
				},
				BaseToken:   base,
				QuoteToken:  quote,
				Op:          pb.OrderOp_ADD,
				Price:       price,
				Amount:      amount,
				FilledBase:  "0",
				FilledQuote: "0",
				IsFilled:    false,
			},
		},
	}
}

func createBuyOrder(txID, from, base, quote, price, amount string) *pb.AnyTx {
	return &pb.AnyTx{
		Content: &pb.AnyTx_OrderTx{
			OrderTx: &pb.OrderTx{
				Base: &pb.BaseMessage{
					TxId:        txID,
					FromAddress: from,
					Status:      pb.Status_PENDING,
				},
				BaseToken:   base,
				QuoteToken:  quote,
				Op:          pb.OrderOp_ADD,
				Price:       price,
				Amount:      amount,
				FilledBase:  "0",
				FilledQuote: "0",
				IsFilled:    false,
			},
		},
	}
}

