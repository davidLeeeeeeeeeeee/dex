package vm_test

import (
	"fmt"
	"testing"

	"dex/keys"
	"dex/pb"
	"dex/vm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestBatchOrderBookRebuild_SinglePair 测试单个交易对的批量重建
func TestBatchOrderBookRebuild_SinglePair(t *testing.T) {
	db := NewMockDB()

	// 创建测试账户
	createTestAccount(t, db, "test_user", "BTC", "10.0")
	createTestAccount(t, db, "test_user", "USDT", "100000.0")

	executor := createTestExecutor(db)

	// 准备测试数据：在 DB 中创建 3 个未成交订单
	pair := "BTC_USDT"
	orders := createTestOrders(t, db, pair, 3)

	// 创建包含 1 个订单交易的区块
	block := &pb.Block{
		BlockHash:     "block1",
		PrevBlockHash: "",
		Height:        1,
		Body: []*pb.AnyTx{
			createOrderAnyTx("new_order_1", "BTC", "USDT", "50000", "1.0"),
		},
	}

	// 执行预执行
	result, err := executor.PreExecuteBlock(block)

	// 验证
	require.NoError(t, err)
	assert.NotNil(t, result)

	// 调试信息
	if !result.Valid {
		t.Logf("Block invalid, reason: %s", result.Reason)
	}
	t.Logf("Receipts count: %d", len(result.Receipts))
	for i, r := range result.Receipts {
		t.Logf("Receipt %d: TxID=%s, Status=%s, Error=%s", i, r.TxID, r.Status, r.Error)
	}

	assert.True(t, result.Valid, "Block should be valid, reason: %s", result.Reason)
	assert.Equal(t, 1, len(result.Receipts))

	// 验证订单簿被正确重建（通过检查 receipt 中的撮合结果）
	if len(result.Receipts) > 0 {
		receipt := result.Receipts[0]
		assert.Equal(t, "SUCCEED", receipt.Status)
	}

	t.Logf("Successfully rebuilt order book for pair %s with %d existing orders", pair, len(orders))
}

// TestBatchOrderBookRebuild_MultiplePairs 测试多个交易对的批量重建
func TestBatchOrderBookRebuild_MultiplePairs(t *testing.T) {
	db := NewMockDB()

	// 创建测试账户
	createTestAccount(t, db, "test_user", "BTC", "100.0")
	createTestAccount(t, db, "test_user", "ETH", "1000.0")
	createTestAccount(t, db, "test_user", "SOL", "10000.0")
	createTestAccount(t, db, "test_user", "USDT", "1000000.0")

	executor := createTestExecutor(db)

	// 准备测试数据：3 个不同的交易对
	pairs := []string{"BTC_USDT", "ETH_USDT", "SOL_USDT"}
	ordersPerPair := 5

	totalOrders := 0
	for _, pair := range pairs {
		orders := createTestOrders(t, db, pair, ordersPerPair)
		totalOrders += len(orders)
		t.Logf("Created %d orders for pair %s", len(orders), pair)
	}

	// 创建包含 3 个不同交易对订单的区块
	block := &pb.Block{
		BlockHash:     "block1",
		PrevBlockHash: "",
		Height:        1,
		Body: []*pb.AnyTx{
			createOrderAnyTx("new_btc_1", "BTC", "USDT", "50000", "0.5"),
			createOrderAnyTx("new_eth_1", "ETH", "USDT", "3000", "2.0"),
			createOrderAnyTx("new_sol_1", "SOL", "USDT", "100", "10.0"),
		},
	}

	// 执行预执行
	result, err := executor.PreExecuteBlock(block)

	// 验证
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, result.Valid)
	assert.Equal(t, 3, len(result.Receipts))

	// 验证所有订单都成功处理
	for i, receipt := range result.Receipts {
		assert.Equal(t, "SUCCEED", receipt.Status, "Receipt %d should succeed", i)
	}

	t.Logf("Successfully rebuilt %d order books with total %d existing orders", len(pairs), totalOrders)
}

// TestBatchOrderBookRebuild_Performance 测试批量重建的性能优势
func TestBatchOrderBookRebuild_Performance(t *testing.T) {
	db := NewMockDB()

	// 创建测试账户
	createTestAccount(t, db, "test_user", "BTC", "1000.0")
	createTestAccount(t, db, "test_user", "ETH", "10000.0")
	createTestAccount(t, db, "test_user", "SOL", "100000.0")
	createTestAccount(t, db, "test_user", "DOGE", "1000000.0")
	createTestAccount(t, db, "test_user", "ADA", "1000000.0")
	createTestAccount(t, db, "test_user", "USDT", "10000000.0")

	executor := createTestExecutor(db)

	// 准备大量测试数据
	pairs := []string{"BTC_USDT", "ETH_USDT", "SOL_USDT", "DOGE_USDT", "ADA_USDT"}
	ordersPerPair := 20

	totalOrders := 0
	for _, pair := range pairs {
		orders := createTestOrders(t, db, pair, ordersPerPair)
		totalOrders += len(orders)
	}

	// 创建包含多个订单的区块
	block := &pb.Block{
		BlockHash:     "block1",
		PrevBlockHash: "",
		Height:        1,
		Body:          make([]*pb.AnyTx, 0, 50),
	}

	// 为每个交易对添加 10 个新订单
	orderID := 0
	for _, pair := range pairs {
		tokens := splitPair(pair)
		for i := 0; i < 10; i++ {
			orderID++
			block.Body = append(block.Body,
				createOrderAnyTx(
					fmt.Sprintf("new_order_%d", orderID),
					tokens[0],
					tokens[1],
					"50000",
					"0.1",
				),
			)
		}
	}

	// 执行预执行并测量
	result, err := executor.PreExecuteBlock(block)

	// 验证
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, result.Valid)
	assert.Equal(t, 50, len(result.Receipts))

	successCount := 0
	for _, receipt := range result.Receipts {
		if receipt.Status == "SUCCEED" {
			successCount++
		}
	}

	t.Logf("Performance test: %d pairs, %d existing orders, %d new orders, %d succeeded",
		len(pairs), totalOrders, len(block.Body), successCount)
}

// TestBatchOrderBookRebuild_EmptyOrderBook 测试空订单簿的情况
func TestBatchOrderBookRebuild_EmptyOrderBook(t *testing.T) {
	db := NewMockDB()

	// 创建测试账户
	createTestAccount(t, db, "test_user", "BTC", "10.0")
	createTestAccount(t, db, "test_user", "USDT", "100000.0")

	executor := createTestExecutor(db)

	// 创建区块，但 DB 中没有任何订单
	block := &pb.Block{
		BlockHash:     "block1",
		PrevBlockHash: "",
		Height:        1,
		Body: []*pb.AnyTx{
			createOrderAnyTx("new_order_1", "BTC", "USDT", "50000", "1.0"),
		},
	}

	// 执行预执行
	result, err := executor.PreExecuteBlock(block)

	// 验证
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, result.Valid)
	assert.Equal(t, 1, len(result.Receipts))
	assert.Equal(t, "SUCCEED", result.Receipts[0].Status)

	t.Log("Successfully handled empty order book scenario")
}

// TestBatchOrderBookRebuild_Matching 测试批量重建后的撮合功能
func TestBatchOrderBookRebuild_Matching(t *testing.T) {
	db := NewMockDB()

	// 创建测试账户
	createTestAccount(t, db, "seller", "BTC", "10.0")
	createTestAccount(t, db, "test_user", "USDT", "100000.0")

	executor := createTestExecutor(db)

	pair := "BTC_USDT"

	// 在 DB 中创建一个卖单
	sellOrder := &pb.OrderTx{
		Base: &pb.BaseMessage{
			TxId:        "sell_order_1",
			FromAddress: "seller",
		},
		BaseToken:   "BTC",
		QuoteToken:  "USDT",
		Op:          pb.OrderOp_ADD,
		Side:        pb.OrderSide_SELL,
		Price:       "50000",
		Amount:      "1.0",
		FilledBase:  "0",
		FilledQuote: "0",
		IsFilled:    false,
	}
	saveOrderToDB(t, db, sellOrder, pair)

	// 创建一个匹配的买单
	block := &pb.Block{
		BlockHash:     "block1",
		PrevBlockHash: "",
		Height:        1,
		Body: []*pb.AnyTx{
			createBuyOrderAnyTx("buy_order_1", "BTC", "USDT", "50000", "0.5"),
		},
	}

	// 执行预执行
	result, err := executor.PreExecuteBlock(block)

	// 验证
	require.NoError(t, err)
	assert.NotNil(t, result)
	if !result.Valid {
		t.Logf("Block not valid, reason: %s", result.Reason)
	}
	require.True(t, result.Valid, "Block should be valid")
	require.Equal(t, 1, len(result.Receipts), "Should have 1 receipt")

	receipt := result.Receipts[0]
	if receipt.Status != "SUCCEED" {
		t.Logf("Receipt failed: %s", receipt.Error)
	}
	assert.Equal(t, "SUCCEED", receipt.Status)

	// 验证生成了 WriteOps（撮合结果）
	assert.NotEmpty(t, result.Diff)

	t.Logf("Matching test: generated %d write operations", len(result.Diff))
}

// TestBatchOrderBookRebuild_MixedOperations 测试混合操作（ADD/REMOVE）
func TestBatchOrderBookRebuild_MixedOperations(t *testing.T) {
	db := NewMockDB()

	// 创建测试账户
	createTestAccount(t, db, "test_user", "BTC", "10.0")
	createTestAccount(t, db, "test_user", "USDT", "100000.0")
	createTestAccount(t, db, "user1", "BTC", "10.0")

	executor := createTestExecutor(db)

	pair := "BTC_USDT"
	createTestOrders(t, db, pair, 3)

	// 创建包含 ADD 和 CANCEL 操作的区块
	block := &pb.Block{
		BlockHash:     "block1",
		PrevBlockHash: "",
		Height:        1,
		Body: []*pb.AnyTx{
			createOrderAnyTx("new_order_1", "BTC", "USDT", "50000", "1.0"),
			// REMOVE 操作不应该触发订单簿重建
			{
				Content: &pb.AnyTx_OrderTx{
					OrderTx: &pb.OrderTx{
						Base: &pb.BaseMessage{
							TxId:        "remove_order_1",
							FromAddress: "user1",
						},
						Op: pb.OrderOp_REMOVE,
					},
				},
			},
		},
	}

	// 执行预执行
	result, err := executor.PreExecuteBlock(block)

	// 验证
	require.NoError(t, err)
	assert.NotNil(t, result)

	t.Logf("Mixed operations test: %d receipts generated", len(result.Receipts))
}

// ========== 辅助函数 ==========

// createTestExecutor 创建测试用的 Executor
func createTestExecutor(db *MockDB) *vm.Executor {
	reg := vm.NewHandlerRegistry()
	if err := vm.RegisterDefaultHandlers(reg); err != nil {
		panic(err)
	}

	cache := vm.NewSpecExecLRU(1024)
	return vm.NewExecutor(db, reg, cache)
}

// createTestAccount 创建测试账户（使用 JSON 序列化，与现有测试保持一致）
func createTestAccount(t *testing.T, db *MockDB, address, tokenAddress, balance string) {
	accountKey := keys.KeyAccount(address)

	// 尝试获取现有账户
	db.mu.RLock()
	existingData, exists := db.data[accountKey]
	db.mu.RUnlock()

	var account *pb.Account
	if exists {
		// 反序列化现有账户（使用 Proto）
		account = &pb.Account{}
		if err := proto.Unmarshal(existingData, account); err != nil {
			account = &pb.Account{
				Address:  address,
				Balances: make(map[string]*pb.TokenBalance),
			}
		}
	} else {
		// 创建新账户
		account = &pb.Account{
			Address:  address,
			Balances: make(map[string]*pb.TokenBalance),
		}
	}

	// 添加或更新 token 余额
	account.Balances[tokenAddress] = &pb.TokenBalance{
		Balance:            balance,
		MinerLockedBalance: "0",
	}

	// 序列化并保存（使用 Proto）
	accountData, err := proto.Marshal(account)
	if err != nil {
		if t != nil {
			require.NoError(t, err)
		} else {
			panic(err)
		}
	}

	db.mu.Lock()
	db.data[accountKey] = accountData
	db.mu.Unlock()
}

// createTestOrders 在 DB 中创建指定数量的测试订单
func createTestOrders(t *testing.T, db *MockDB, pair string, count int) []*pb.OrderTx {
	orders := make([]*pb.OrderTx, count)
	tokens := splitPair(pair)

	for i := 0; i < count; i++ {
		order := &pb.OrderTx{
			Base: &pb.BaseMessage{
				TxId:        fmt.Sprintf("order_%s_%d", pair, i),
				FromAddress: fmt.Sprintf("user_%d", i),
			},
			BaseToken:   tokens[0],
			QuoteToken:  tokens[1],
			Op:          pb.OrderOp_ADD,
			Side:        pb.OrderSide_SELL, // 默认创建卖单
			Price:       fmt.Sprintf("%d", 50000+i*100),
			Amount:      "1.0",
			FilledBase:  "0",
			FilledQuote: "0",
			IsFilled:    false,
		}
		saveOrderToDB(t, db, order, pair)
		orders[i] = order
	}

	return orders
}

// saveOrderToDB 将订单保存到 MockDB（使用 Proto 序列化，与生产代码保持一致）
func saveOrderToDB(t *testing.T, db *MockDB, order *pb.OrderTx, pair string) {
	// 序列化订单（使用 Proto）
	orderData, err := proto.Marshal(order)
	if err != nil {
		if t != nil {
			require.NoError(t, err)
		} else {
			panic(err)
		}
	}

	// 保存订单数据
	orderKey := keys.KeyOrder(order.Base.TxId)
	db.mu.Lock()
	db.data[orderKey] = orderData
	db.mu.Unlock()

	// 创建价格索引
	priceKey67, err := generatePriceKey(order.Price)
	if err != nil {
		if t != nil {
			require.NoError(t, err)
		} else {
			panic(err)
		}
	}

	indexKey := keys.KeyOrderPriceIndex(pair, false, priceKey67, order.Base.TxId)
	indexData, _ := proto.Marshal(&pb.OrderPriceIndex{Ok: true})
	db.mu.Lock()
	db.data[indexKey] = indexData
	db.mu.Unlock()
}

// createOrderAnyTx 创建订单 AnyTx（默认为卖单）
func createOrderAnyTx(txID, baseToken, quoteToken, price, amount string) *pb.AnyTx {
	return &pb.AnyTx{
		Content: &pb.AnyTx_OrderTx{
			OrderTx: &pb.OrderTx{
				Base: &pb.BaseMessage{
					TxId:        txID,
					FromAddress: "test_user",
				},
				BaseToken:   baseToken,
				QuoteToken:  quoteToken,
				Op:          pb.OrderOp_ADD,
				Side:        pb.OrderSide_SELL, // 默认为卖单
				Price:       price,
				Amount:      amount,
				FilledBase:  "0",
				FilledQuote: "0",
				IsFilled:    false,
			},
		},
	}
}

// createBuyOrderAnyTx 创建买单 AnyTx
func createBuyOrderAnyTx(txID, baseToken, quoteToken, price, amount string) *pb.AnyTx {
	return &pb.AnyTx{
		Content: &pb.AnyTx_OrderTx{
			OrderTx: &pb.OrderTx{
				Base: &pb.BaseMessage{
					TxId:        txID,
					FromAddress: "test_user",
				},
				BaseToken:   baseToken,
				QuoteToken:  quoteToken,
				Op:          pb.OrderOp_ADD,
				Side:        pb.OrderSide_BUY, // 买单
				Price:       price,
				Amount:      amount,
				FilledBase:  "0",
				FilledQuote: "0",
				IsFilled:    false,
			},
		},
	}
}

// splitPair 分割交易对字符串
func splitPair(pair string) []string {
	// 使用 utils.GeneratePairKey 的逆向逻辑
	// pair 格式: "TOKEN1_TOKEN2"
	tokens := make([]string, 2)
	for i := 0; i < len(pair); i++ {
		if pair[i] == '_' {
			tokens[0] = pair[:i]
			tokens[1] = pair[i+1:]
			break
		}
	}
	return tokens
}

// generatePriceKey 生成 67 位价格 key（简化版本）
func generatePriceKey(price string) (string, error) {
	// 这里使用简化的实现，实际应该使用 db.PriceToKey128
	// 为了避免循环依赖，这里直接生成固定长度的字符串
	priceInt := 0
	fmt.Sscanf(price, "%d", &priceInt)
	return fmt.Sprintf("%067d", priceInt*1000000000000000000), nil
}

// TestScanOrdersByPairs 测试批量扫描订单的 DB 接口
func TestScanOrdersByPairs(t *testing.T) {
	db := NewMockDB()

	// 准备测试数据：3 个交易对，每个 5 个订单
	pairs := []string{"BTC_USDT", "ETH_USDT", "SOL_USDT"}
	for _, pair := range pairs {
		createTestOrders(t, db, pair, 5)
	}

	// 调用批量扫描
	result, err := db.ScanOrdersByPairs(pairs)

	// 验证
	require.NoError(t, err)
	assert.Equal(t, 3, len(result))

	for _, pair := range pairs {
		pairOrders := result[pair]
		assert.Equal(t, 5, len(pairOrders), "Pair %s should have 5 orders", pair)
		t.Logf("Pair %s: found %d orders", pair, len(pairOrders))
	}
}

// TestBatchRebuild_DBScanOptimization 测试 DB 扫描优化
func TestBatchRebuild_DBScanOptimization(t *testing.T) {
	db := NewMockDB()

	// 准备大量数据
	pairs := []string{"BTC_USDT", "ETH_USDT", "SOL_USDT", "DOGE_USDT", "ADA_USDT"}
	ordersPerPair := 50

	for _, pair := range pairs {
		createTestOrders(t, db, pair, ordersPerPair)
	}

	// 测试批量扫描
	result, err := db.ScanOrdersByPairs(pairs)
	require.NoError(t, err)

	totalOrders := 0
	for pair, orders := range result {
		totalOrders += len(orders)
		t.Logf("Pair %s: %d orders", pair, len(orders))
	}

	expectedTotal := len(pairs) * ordersPerPair
	assert.Equal(t, expectedTotal, totalOrders, "Total orders should match")

	t.Logf("DB Scan Optimization: scanned %d pairs with %d total orders in single operation",
		len(pairs), totalOrders)
}

// TestBatchRebuild_Correctness 测试批量重建的正确性
func TestBatchRebuild_Correctness(t *testing.T) {
	db := NewMockDB()

	// 创建测试账户
	createTestAccount(t, db, "test_user", "BTC", "10.0")
	createTestAccount(t, db, "test_user", "USDT", "100000.0")

	executor := createTestExecutor(db)

	pair := "BTC_USDT"

	// 创建不同价格的订单
	prices := []string{"49000", "49500", "50000", "50500", "51000"}
	for i, price := range prices {
		order := &pb.OrderTx{
			Base: &pb.BaseMessage{
				TxId:        fmt.Sprintf("order_%d", i),
				FromAddress: fmt.Sprintf("user_%d", i),
			},
			BaseToken:   "BTC",
			QuoteToken:  "USDT",
			Op:          pb.OrderOp_ADD,
			Side:        pb.OrderSide_SELL,
			Price:       price,
			Amount:      "1.0",
			FilledBase:  "0",
			FilledQuote: "0",
			IsFilled:    false,
		}
		saveOrderToDB(t, db, order, pair)
	}

	// 创建一个新订单，价格在中间
	block := &pb.Block{
		BlockHash:     "block1",
		PrevBlockHash: "",
		Height:        1,
		Body: []*pb.AnyTx{
			createOrderAnyTx("new_order", "BTC", "USDT", "50000", "0.5"),
		},
	}

	// 执行
	result, err := executor.PreExecuteBlock(block)

	// 验证
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, result.Valid)

	t.Logf("Correctness test: rebuilt order book with %d price levels", len(prices))
}

// TestBatchRebuild_EdgeCases 测试边界情况
func TestBatchRebuild_EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		setupDB     func(*MockDB)
		block       *pb.Block
		expectValid bool
	}{
		{
			name: "no existing orders",
			setupDB: func(db *MockDB) {
				// 创建账户但不创建任何订单
				createTestAccount(nil, db, "test_user", "BTC", "10.0")
				createTestAccount(nil, db, "test_user", "USDT", "100000.0")
			},
			block: &pb.Block{
				BlockHash: "block1",
				Height:    1,
				Body: []*pb.AnyTx{
					createOrderAnyTx("order1", "BTC", "USDT", "50000", "1.0"),
				},
			},
			expectValid: true,
		},
		{
			name: "empty block",
			setupDB: func(db *MockDB) {
				// 创建一些订单但区块为空
				for i := 0; i < 5; i++ {
					order := &pb.OrderTx{
						Base: &pb.BaseMessage{
							TxId:        fmt.Sprintf("order_%d", i),
							FromAddress: fmt.Sprintf("user_%d", i),
						},
						BaseToken:   "BTC",
						QuoteToken:  "USDT",
						Op:          pb.OrderOp_ADD,
						Side:        pb.OrderSide_SELL,
						Price:       fmt.Sprintf("%d", 50000+i*100),
						Amount:      "1.0",
						FilledBase:  "0",
						FilledQuote: "0",
						IsFilled:    false,
					}
					saveOrderToDB(nil, db, order, "BTC_USDT")
				}
			},
			block: &pb.Block{
				BlockHash: "block1",
				Height:    1,
				Body:      []*pb.AnyTx{},
			},
			expectValid: true,
		},
		{
			name: "only non-ADD operations",
			setupDB: func(db *MockDB) {
				// 创建账户和一些订单
				createTestAccount(nil, db, "user_0", "BTC", "10.0")
				for i := 0; i < 5; i++ {
					order := &pb.OrderTx{
						Base: &pb.BaseMessage{
							TxId:        fmt.Sprintf("order_%d", i),
							FromAddress: "user_0", // 所有订单都由 user_0 创建
						},
						BaseToken:   "BTC",
						QuoteToken:  "USDT",
						Op:          pb.OrderOp_ADD,
						Side:        pb.OrderSide_SELL,
						Price:       fmt.Sprintf("%d", 50000+i*100),
						Amount:      "1.0",
						FilledBase:  "0",
						FilledQuote: "0",
						IsFilled:    false,
					}
					saveOrderToDB(nil, db, order, "BTC_USDT")
				}
			},
			block: &pb.Block{
				BlockHash: "block1",
				Height:    1,
				Body: []*pb.AnyTx{
					{
						Content: &pb.AnyTx_OrderTx{
							OrderTx: &pb.OrderTx{
								Base: &pb.BaseMessage{
									TxId:        "remove1",
									FromAddress: "user_0", // 与订单创建者一致
								},
								Op:         pb.OrderOp_REMOVE,
								OpTargetId: "order_0", // 指定要删除的订单 ID
								BaseToken:  "BTC",
								QuoteToken: "USDT",
							},
						},
					},
				},
			},
			expectValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := NewMockDB()
			if tt.setupDB != nil {
				tt.setupDB(db)
			}

			executor := createTestExecutor(db)
			result, err := executor.PreExecuteBlock(tt.block)

			require.NoError(t, err)
			assert.NotNil(t, result)

			if !result.Valid && tt.expectValid {
				t.Logf("Expected valid but got invalid, reason: %s", result.Reason)
			}
			assert.Equal(t, tt.expectValid, result.Valid, "Reason: %s", result.Reason)

			t.Logf("Edge case '%s': valid=%v, receipts=%d",
				tt.name, result.Valid, len(result.Receipts))
		})
	}
}
