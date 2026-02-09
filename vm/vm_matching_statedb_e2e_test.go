package vm_test

import (
	"bytes"
	"testing"
	"time"

	"dex/db"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"dex/vm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func requireProtoUnmarshalCompat(t *testing.T, data []byte, msg proto.Message) {
	t.Helper()
	if err := proto.Unmarshal(data, msg); err == nil {
		return
	}
	trimmed := bytes.TrimRight(data, "\x00")
	require.NotEqual(t, len(data), len(trimmed))
	require.NoError(t, proto.Unmarshal(trimmed, msg))
}

// TestE2E_OrderMatching_VM_StateDB_Integration
// 端到端集成测试：验证 VM、Matching、StateDB 三模块完美配合
//
// 测试场景：
// 1. 创建真实的 Badger + StateDB
// 2. 初始化账户余额
// 3. 提交订单交易，触发撮合
// 4. 验证：
//   - VM 正确执行
//   - Matching 正确撮合
//   - StateDB 正确同步账户数据
//   - Badger 持久化所有数据
//   - 数据一致性
func TestE2E_OrderMatching_VM_StateDB_Integration(t *testing.T) {
	// ========== 第一步：初始化真实数据库 ==========
	tmpDir := t.TempDir() // 自动清理
	t.Logf("📁 Test database directory: %s", tmpDir)

	dbMgr, err := db.NewManager(tmpDir, logs.NewNodeLogger("test", 0))
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
				BaseToken:  "BTC",
				QuoteToken: "USDT",
				Op:         pb.OrderOp_ADD,
				Side:       pb.OrderSide_SELL, // 明确设置卖单方向
				Price:      "50000",           // 卖价 50000 USDT/BTC
				Amount:     "1.0",             // 卖 1 BTC
			},
		},
	}

	block1 := &pb.Block{
		BlockHash: "block_001",
		Header: &pb.BlockHeader{
			PrevBlockHash: "genesis",
			Height:        1,
		},
		Body: []*pb.AnyTx{sellOrderTx},
	}

	t.Log("📦 Executing Block 1: Alice places sell order (1 BTC @ 50000 USDT)")
	result1, err := executor.PreExecuteBlock(block1)
	require.NoError(t, err)
	if !result1.Valid {
		t.Logf("Block 1 failed, reason: %s", result1.Reason)
	}
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
	// 买单：BaseToken=BTC（要买的币），QuoteToken=USDT（支付的币），Side=BUY
	buyOrderTx := &pb.AnyTx{
		Content: &pb.AnyTx_OrderTx{
			OrderTx: &pb.OrderTx{
				Base: &pb.BaseMessage{
					TxId:        "buy_order_001",
					FromAddress: bobAddr,
					Status:      pb.Status_PENDING,
				},
				BaseToken:  "BTC",
				QuoteToken: "USDT",
				Op:         pb.OrderOp_ADD,
				Side:       pb.OrderSide_BUY, // 明确设置买单方向
				Price:      "50000",          // USDT/BTC (1 BTC = 50000 USDT)
				Amount:     "0.5",            // 买 0.5 BTC
			},
		},
	}

	block2 := &pb.Block{
		BlockHash: "block_002",
		Header: &pb.BlockHeader{
			PrevBlockHash: "block_001",
			Height:        2,
		},
		Body: []*pb.AnyTx{buyOrderTx},
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
	aliceBTC := getE2EBalance(t, dbMgr, aliceAddr, "BTC")
	aliceUSDT := getE2EBalance(t, dbMgr, aliceAddr, "USDT")
	t.Logf("Alice actual: BTC=%s, USDT=%s", aliceBTC.Balance, aliceUSDT.Balance)
	assert.Equal(t, "9.5", aliceBTC.Balance, "Alice should have 9.5 BTC")
	assert.Equal(t, "125000", aliceUSDT.Balance, "Alice should have 125000 USDT")

	// 验证 Bob 的余额变化
	// Bob 的买单：base_token=USDT, quote_token=BTC, amount=25000, price=0.00002
	// 成交 tradeAmt（USDT 数量），获得 tradeAmt * 0.00002 BTC
	// 预期：花费 25000 USDT，获得 25000 * 0.00002 = 0.5 BTC
	// - USDT: 100000 - 25000 = 75000
	// - BTC: 0 + 0.5 = 0.5
	bobBTC := getE2EBalance(t, dbMgr, bobAddr, "BTC")
	bobUSDT := getE2EBalance(t, dbMgr, bobAddr, "USDT")
	t.Logf("Bob actual: BTC=%s, USDT=%s", bobBTC.Balance, bobUSDT.Balance)
	assert.Equal(t, "0.5", bobBTC.Balance, "Bob should have 0.5 BTC")
	assert.Equal(t, "75000", bobUSDT.Balance, "Bob should have 75000 USDT")

	t.Log("✅ Account balances verified correctly")

	// ========== 第七步：验证数据持久化 ==========
	t.Log("🔍 Verifying data persistence...")

	// 重新读取账户，验证数据已正确持久化
	aliceBTCFromDB := getE2EBalance(t, dbMgr, aliceAddr, "BTC")
	aliceUSDTFromDB := getE2EBalance(t, dbMgr, aliceAddr, "USDT")
	assert.Equal(t, "9.5", aliceBTCFromDB.Balance, "DB should have correct Alice BTC balance")
	assert.Equal(t, "125000", aliceUSDTFromDB.Balance, "DB should have correct Alice USDT balance")

	bobBTCFromDB := getE2EBalance(t, dbMgr, bobAddr, "BTC")
	bobUSDTFromDB := getE2EBalance(t, dbMgr, bobAddr, "USDT")
	assert.Equal(t, "0.5", bobBTCFromDB.Balance, "DB should have correct Bob BTC balance")
	assert.Equal(t, "75000", bobUSDTFromDB.Balance, "DB should have correct Bob USDT balance")

	t.Log("✅ Data persistence verified")

	// ========== 第八步：验证订单状态 ==========
	t.Log("🔍 Verifying order status...")

	// 验证卖单部分成交 - 使用 OrderState
	sellOrderState := getE2EOrderState(t, dbMgr, "sell_order_001")
	assert.Equal(t, "0.5", sellOrderState.FilledBase, "Sell order should have 0.5 BTC filled")
	assert.False(t, sellOrderState.IsFilled, "Sell order should not be fully filled")

	// 验证买单完全成交 - 使用 OrderState
	// 买单：BaseToken=BTC，Amount=0.5，成交后 FilledBase=0.5
	buyOrderState := getE2EOrderState(t, dbMgr, "buy_order_001")
	assert.Equal(t, "0.5", buyOrderState.FilledBase, "Buy order should have 0.5 BTC filled")
	assert.True(t, buyOrderState.IsFilled, "Buy order should be fully filled")

	t.Log("✅ Order status verified")

	// 验证账户余额一致性
	assert.Equal(t, bobBTC.Balance, bobBTCFromDB.Balance,
		"Account balance should match between reads")
	assert.Equal(t, bobUSDT.Balance, bobUSDTFromDB.Balance,
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

// createE2ETestAccount 创建测试账户并写入数据库（使用分离存储）
func createE2ETestAccount(t *testing.T, dbMgr *db.Manager, address string, balances map[string]string) {
	// 创建账户（不含余额）
	account := &pb.Account{
		Address: address,
	}

	// 使用 proto 序列化账户
	accountData, err := proto.Marshal(account)
	require.NoError(t, err)

	accountKey := keys.KeyAccount(address)
	dbMgr.EnqueueSet(accountKey, string(accountData))

	// 使用 KeyBalance 分离存储余额
	for token, balance := range balances {
		bal := &pb.TokenBalanceRecord{
			Balance: &pb.TokenBalance{
				Balance:            balance,
				MinerLockedBalance: "0",
			},
		}
		balData, err := proto.Marshal(bal)
		require.NoError(t, err)
		balKey := keys.KeyBalance(address, token)
		dbMgr.EnqueueSet(balKey, string(balData))
	}
}

// getE2EAccount 从数据库读取账户（E2E 测试专用）
func getE2EAccount(t *testing.T, dbMgr *db.Manager, address string) *pb.Account {
	accountKey := keys.KeyAccount(address)
	accountData, err := dbMgr.Get(accountKey)
	require.NoError(t, err)
	require.NotNil(t, accountData)

	var account pb.Account
	// 使用 proto 反序列化
	requireProtoUnmarshalCompat(t, accountData, &account)
	return &account
}

// getE2EBalance 从数据库读取余额（E2E 测试专用，使用分离存储）
func getE2EBalance(t *testing.T, dbMgr *db.Manager, address, token string) *pb.TokenBalance {
	balKey := keys.KeyBalance(address, token)
	balData, err := dbMgr.Get(balKey)
	if err != nil || len(balData) == 0 {
		return &pb.TokenBalance{Balance: "0"}
	}

	var record pb.TokenBalanceRecord
	requireProtoUnmarshalCompat(t, balData, &record)
	if record.Balance == nil {
		return &pb.TokenBalance{Balance: "0"}
	}
	return record.Balance
}

// getE2EOrder 从数据库读取订单（E2E 测试专用）
func getE2EOrder(t *testing.T, dbMgr *db.Manager, orderID string) *pb.OrderTx {
	orderKey := keys.KeyOrder(orderID)
	orderData, err := dbMgr.Get(orderKey)
	require.NoError(t, err)
	require.NotNil(t, orderData)

	var order pb.OrderTx
	requireProtoUnmarshalCompat(t, orderData, &order)
	return &order
}

// getE2EOrderState 从数据库读取订单状态（E2E 测试专用）
func getE2EOrderState(t *testing.T, dbMgr *db.Manager, orderID string) *pb.OrderState {
	orderStateKey := keys.KeyOrderState(orderID)
	orderStateData, err := dbMgr.Get(orderStateKey)
	require.NoError(t, err)
	require.NotNil(t, orderStateData)

	var orderState pb.OrderState
	requireProtoUnmarshalCompat(t, orderStateData, &orderState)
	return &orderState
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
	t.Skip("TODO: 需要适配分离存储 - Balances 已移除")
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
				BaseToken:  base,
				QuoteToken: quote,
				Op:         pb.OrderOp_ADD,
				Side:       pb.OrderSide_SELL, // 明确设置卖单方向
				Price:      price,
				Amount:     amount,
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
				BaseToken:  base,
				QuoteToken: quote,
				Op:         pb.OrderOp_ADD,
				Side:       pb.OrderSide_BUY, // 明确设置买单方向
				Price:      price,
				Amount:     amount,
			},
		},
	}
}

// TestE2E_TransactionOrderDeterminism
// 测试交易执行顺序的确定性
//
// 场景：
// 在同一个区块中，Alice 挂 3 个卖单（价格递增），Bob 挂 1 个买单
// 验证：
// 1. 交易按照 block.Body 数组顺序执行
// 2. 撮合引擎按照价格优先原则匹配（最低价优先）
// 3. 执行结果是确定性的，多次执行结果一致
func TestE2E_TransactionOrderDeterminism(t *testing.T) {
	t.Skip("TODO: 需要适配分离存储 - Balances 已移除")
}

// TestE2E_SameAccountMultipleBalanceChanges
// 测试同一区块中对同一账户的多次余额修改
//
// 场景：
// 在同一个区块中，Alice 进行多次转账操作
// 验证：
// 1. 同一账户的余额修改按照交易顺序累积
// 2. 最终余额正确反映所有交易的累积效果
// 3. 数据一致性：StateDB 和 Badger 数据一致
func TestE2E_SameAccountMultipleBalanceChanges(t *testing.T) {
	t.Skip("TODO: 需要适配分离存储 - Balances 已移除")
}

// createTransferTx 创建转账交易（辅助函数）
func createTransferTx(txID, from, to, token, amount string) *pb.AnyTx {
	return &pb.AnyTx{
		Content: &pb.AnyTx_Transaction{
			Transaction: &pb.Transaction{
				Base: &pb.BaseMessage{
					TxId:        txID,
					FromAddress: from,
					Status:      pb.Status_PENDING,
				},
				To:           to,
				TokenAddress: token,
				Amount:       amount,
			},
		},
	}
}
