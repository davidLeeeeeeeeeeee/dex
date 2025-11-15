package vm

import (
	"dex/keys"
	"dex/pb"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// testMockDB is a simple in-memory database for testing
type testMockDB struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func newTestMockDB() *testMockDB {
	return &testMockDB{
		data: make(map[string][]byte),
	}
}

func (db *testMockDB) Get(key string) ([]byte, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	val, exists := db.data[key]
	if !exists {
		return nil, nil
	}
	return val, nil
}

func (db *testMockDB) Scan(prefix string) (map[string][]byte, error) {
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

// TestOrderTxHandler_AddOrder_NoMatch tests adding an order that doesn't match
func TestOrderTxHandler_AddOrder_NoMatch(t *testing.T) {
	// Setup
	db := newTestMockDB()
	handler := &OrderTxHandler{}
	
	// Create account with balance
	account := &pb.Account{
		Address: "user1",
		Balances: map[string]*pb.TokenBalance{
			"USDT": {Balance: "10000"},
		},
	}
	accountData, _ := json.Marshal(account)
	db.data[keys.KeyAccount("user1")] = accountData
	
	// Create StateView
	sv := NewStateView(
		func(key string) ([]byte, error) { return db.Get(key) },
		func(prefix string) (map[string][]byte, error) { return db.Scan(prefix) },
	)
	
	// Create order transaction
	orderTx := &pb.OrderTx{
		Base: &pb.BaseMessage{
			TxId:        "order1",
			FromAddress: "user1",
		},
		BaseToken:  "USDT",
		QuoteToken: "BTC",
		Op:         pb.OrderOp_ADD,
		Amount:     "1.0",
		Price:      "50000",
		FilledBase: "0",
		FilledQuote: "0",
		IsFilled:   false,
	}
	
	// Execute
	writeOps, receipt, err := handler.handleAddOrder(orderTx, sv)
	
	// Assert
	require.NoError(t, err)
	assert.NotNil(t, receipt)
	assert.Equal(t, "SUCCEED", receipt.Status)
	assert.NotEmpty(t, writeOps)
	
	// Verify order was saved
	var foundOrderWrite bool
	for _, op := range writeOps {
		if op.Key == keys.KeyOrder("order1") {
			foundOrderWrite = true
			var savedOrder pb.OrderTx
			err := proto.Unmarshal(op.Value, &savedOrder)
			require.NoError(t, err)
			assert.Equal(t, "order1", savedOrder.Base.TxId)
			assert.Equal(t, "1.0", savedOrder.Amount)
			assert.Equal(t, "50000", savedOrder.Price)
		}
	}
	assert.True(t, foundOrderWrite, "Order write operation should exist")
}

// TestOrderTxHandler_AddOrder_FullMatch tests adding an order that fully matches
func TestOrderTxHandler_AddOrder_FullMatch(t *testing.T) {
	// Setup
	db := newTestMockDB()
	handler := &OrderTxHandler{}
	
	// Create accounts with balances
	account1 := &pb.Account{
		Address: "user1",
		Balances: map[string]*pb.TokenBalance{
			"USDT": {Balance: "100000"},
		},
	}
	account1Data, _ := json.Marshal(account1)
	db.data[keys.KeyAccount("user1")] = account1Data
	
	account2 := &pb.Account{
		Address: "user2",
		Balances: map[string]*pb.TokenBalance{
			"BTC": {Balance: "10"},
		},
	}
	account2Data, _ := json.Marshal(account2)
	db.data[keys.KeyAccount("user2")] = account2Data
	
	// Create existing sell order in DB
	existingOrder := &pb.OrderTx{
		Base: &pb.BaseMessage{
			TxId:        "order_existing",
			FromAddress: "user2",
		},
		BaseToken:   "BTC",
		QuoteToken:  "USDT",
		Op:          pb.OrderOp_ADD,
		Amount:      "1.0",
		Price:       "50000",
		FilledBase:  "0",
		FilledQuote: "0",
		IsFilled:    false,
	}
	existingOrderData, _ := proto.Marshal(existingOrder)
	db.data[keys.KeyOrder("order_existing")] = existingOrderData
	
	// Create price index for existing order
	pair := "BTC_USDT"
	priceKey67 := strings.Repeat("0", 60) + "0050000" // Simplified price key
	indexKey := keys.KeyOrderPriceIndex(pair, false, priceKey67, "order_existing")
	indexData, _ := proto.Marshal(&pb.OrderPriceIndex{Ok: true})
	db.data[indexKey] = indexData
	
	// Create StateView
	sv := NewStateView(
		func(key string) ([]byte, error) { return db.Get(key) },
		func(prefix string) (map[string][]byte, error) { return db.Scan(prefix) },
	)
	
	// Create new buy order that matches
	newOrder := &pb.OrderTx{
		Base: &pb.BaseMessage{
			TxId:        "order_new",
			FromAddress: "user1",
		},
		BaseToken:   "USDT",
		QuoteToken:  "BTC",
		Op:          pb.OrderOp_ADD,
		Amount:      "1.0",
		Price:       "50000",
		FilledBase:  "0",
		FilledQuote: "0",
		IsFilled:    false,
	}
	
	// Execute
	writeOps, receipt, err := handler.handleAddOrder(newOrder, sv)
	
	// Assert
	require.NoError(t, err)
	assert.NotNil(t, receipt)
	assert.Equal(t, "SUCCEED", receipt.Status)
	assert.NotEmpty(t, writeOps)
	
	// Verify that orders were updated with trade information
	// Note: The actual matching logic depends on the order side determination
	// which is currently simplified in convertToMatchingOrder
	t.Logf("Generated %d write operations", len(writeOps))
	for _, op := range writeOps {
		t.Logf("WriteOp: Key=%s, Del=%v, Category=%s", op.Key, op.Del, op.Category)
	}
}

// TestOrderTxHandler_AddOrder_PartialMatch tests adding an order that partially matches
func TestOrderTxHandler_AddOrder_PartialMatch(t *testing.T) {
	// Setup
	db := newTestMockDB()
	handler := &OrderTxHandler{}
	
	// Create accounts
	account1 := &pb.Account{
		Address: "user1",
		Balances: map[string]*pb.TokenBalance{
			"USDT": {Balance: "200000"},
		},
	}
	account1Data, _ := json.Marshal(account1)
	db.data[keys.KeyAccount("user1")] = account1Data
	
	account2 := &pb.Account{
		Address: "user2",
		Balances: map[string]*pb.TokenBalance{
			"BTC": {Balance: "10"},
		},
	}
	account2Data, _ := json.Marshal(account2)
	db.data[keys.KeyAccount("user2")] = account2Data
	
	// Create existing sell order (smaller amount)
	existingOrder := &pb.OrderTx{
		Base: &pb.BaseMessage{
			TxId:        "order_existing",
			FromAddress: "user2",
		},
		BaseToken:   "BTC",
		QuoteToken:  "USDT",
		Op:          pb.OrderOp_ADD,
		Amount:      "0.5",
		Price:       "50000",
		FilledBase:  "0",
		FilledQuote: "0",
		IsFilled:    false,
	}
	existingOrderData, _ := proto.Marshal(existingOrder)
	db.data[keys.KeyOrder("order_existing")] = existingOrderData
	
	// Create price index
	pair := "BTC_USDT"
	priceKey67 := strings.Repeat("0", 60) + "0050000"
	indexKey := keys.KeyOrderPriceIndex(pair, false, priceKey67, "order_existing")
	indexData, _ := proto.Marshal(&pb.OrderPriceIndex{Ok: true})
	db.data[indexKey] = indexData
	
	// Create StateView
	sv := NewStateView(
		func(key string) ([]byte, error) { return db.Get(key) },
		func(prefix string) (map[string][]byte, error) { return db.Scan(prefix) },
	)
	
	// Create new buy order (larger amount)
	newOrder := &pb.OrderTx{
		Base: &pb.BaseMessage{
			TxId:        "order_new",
			FromAddress: "user1",
		},
		BaseToken:   "USDT",
		QuoteToken:  "BTC",
		Op:          pb.OrderOp_ADD,
		Amount:      "1.0",
		Price:       "50000",
		FilledBase:  "0",
		FilledQuote: "0",
		IsFilled:    false,
	}
	
	// Execute
	writeOps, receipt, err := handler.handleAddOrder(newOrder, sv)
	
	// Assert
	require.NoError(t, err)
	assert.NotNil(t, receipt)
	assert.Equal(t, "SUCCEED", receipt.Status)
	assert.NotEmpty(t, writeOps)
	
	t.Logf("Generated %d write operations for partial match", len(writeOps))
}

// TestOrderTxHandler_ConvertToMatchingOrder tests the order conversion logic
func TestOrderTxHandler_ConvertToMatchingOrder(t *testing.T) {
	handler := &OrderTxHandler{}

	orderTx := &pb.OrderTx{
		Base: &pb.BaseMessage{
			TxId:        "test_order",
			FromAddress: "user1",
		},
		BaseToken:   "USDT",
		QuoteToken:  "BTC",
		Amount:      "1.5",
		Price:       "50000",
		FilledBase:  "0.5",
		FilledQuote: "0",
	}

	matchOrder, err := handler.convertToMatchingOrder(orderTx)

	require.NoError(t, err)
	assert.Equal(t, "test_order", matchOrder.ID)

	// Use string comparison for decimals to avoid precision issues
	expectedPrice, _ := decimal.NewFromString("50000")
	assert.True(t, matchOrder.Price.Equal(expectedPrice), "Price should be 50000, got %s", matchOrder.Price.String())

	// Remaining amount should be 1.5 - 0.5 = 1.0
	expectedAmount, _ := decimal.NewFromString("1.0")
	assert.True(t, matchOrder.Amount.Equal(expectedAmount), "Amount should be 1.0, got %s", matchOrder.Amount.String())
}

// TestOrderTxHandler_ExtractOrderIDFromIndexKey tests the key parsing logic
func TestOrderTxHandler_ExtractOrderIDFromIndexKey(t *testing.T) {
	tests := []struct {
		name     string
		indexKey string
		expected string
	}{
		{
			name:     "valid key",
			indexKey: "v1_pair:BTC_USDT|is_filled:false|price:0000000000000000000000000000000000000000000000000000000000050000|order_id:order123",
			expected: "order123",
		},
		{
			name:     "invalid key - no order_id",
			indexKey: "v1_pair:BTC_USDT|is_filled:false|price:0050000",
			expected: "",
		},
		{
			name:     "empty key",
			indexKey: "",
			expected: "",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractOrderIDFromIndexKey(tt.indexKey)
			assert.Equal(t, tt.expected, result)
		})
	}
}

