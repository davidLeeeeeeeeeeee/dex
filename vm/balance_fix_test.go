package vm_test

import (
	"dex/db"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"dex/vm"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestOrderBalance_TakerAtomic 验证 Taker 账户在一次交易内的两次余额变更（冻结+扣减）是否原子
func TestOrderBalance_TakerAtomic(t *testing.T) {
	tmpDir := t.TempDir()
	dbMgr, err := db.NewManager(tmpDir, logs.NewNodeLogger("test", 0))
	require.NoError(t, err)
	defer dbMgr.Close()
	dbMgr.InitWriteQueue(10, 50*time.Millisecond)

	registry := vm.NewHandlerRegistry()
	require.NoError(t, vm.RegisterDefaultHandlers(registry))
	executor := vm.NewExecutor(dbMgr, registry, nil)

	aliceAddr := "alice"
	bobAddr := "bob"

	// Alice 挂单: 1.0 BTC @ 50000 USDT
	createE2ETestAccount(t, dbMgr, aliceAddr, map[string]string{"BTC": "10", "USDT": "0"})
	// Bob 吃单: 100000 USDT
	createE2ETestAccount(t, dbMgr, bobAddr, map[string]string{"BTC": "0", "USDT": "100000"})
	require.NoError(t, dbMgr.ForceFlush())

	// Block 1: Alice 挂单
	block1 := &pb.Block{
		BlockHash: "b1",
		Header:    &pb.BlockHeader{Height: 1},
		Body:      []*pb.AnyTx{createSellOrder("s1", aliceAddr, "BTC", "USDT", "50000", "2")},
	}
	require.NoError(t, executor.CommitFinalizedBlock(block1))
	require.NoError(t, dbMgr.ForceFlush())

	// Block 2: Bob 吃单 0.5 BTC @ 50000
	// 预期：
	// 1. 下单瞬间：冻结 0.5 * 50000 = 25000 USDT。Liquid: 75000, Frozen: 25000
	// 2. 撮合成交：扣除 Frozen 25000，增加 BTC 0.5。
	// 最终 Bob: Liquid BTC 0.5, Liquid USDT 75000, Frozen USDT 0.
	buyOrder := createBuyOrder("b1", bobAddr, "BTC", "USDT", "50000", "1")
	block2 := &pb.Block{
		BlockHash: "b2",
		Header:    &pb.BlockHeader{Height: 2, PrevBlockHash: "b1"},
		Body:      []*pb.AnyTx{buyOrder},
	}
	require.NoError(t, executor.CommitFinalizedBlock(block2))
	require.NoError(t, dbMgr.ForceFlush())

	bobUSDT := getE2EBalance(t, dbMgr, bobAddr, "USDT")
	bobBTC := getE2EBalance(t, dbMgr, bobAddr, "BTC")
	assert.Equal(t, "50000", bobUSDT.Balance)
	assert.Equal(t, "0", bobUSDT.OrderFrozenBalance)
	assert.Equal(t, "1", bobBTC.Balance)
}

// TestOrderBalance_BuyRefund 验证买单以低价成交时的退款逻辑
func TestOrderBalance_BuyRefund(t *testing.T) {
	tmpDir := t.TempDir()
	dbMgr, err := db.NewManager(tmpDir, logs.NewNodeLogger("test", 0))
	require.NoError(t, err)
	defer dbMgr.Close()
	dbMgr.InitWriteQueue(10, 50*time.Millisecond)

	registry := vm.NewHandlerRegistry()
	require.NoError(t, vm.RegisterDefaultHandlers(registry))
	executor := vm.NewExecutor(dbMgr, registry, nil)

	aliceAddr := "alice"
	bobAddr := "bob"

	// Alice 挂廉价卖单: 1.0 BTC @ 40000 USDT
	createE2ETestAccount(t, dbMgr, aliceAddr, map[string]string{"BTC": "10", "USDT": "0"})
	// Bob 挂高价买单: 1.0 BTC @ 60000 USDT
	createE2ETestAccount(t, dbMgr, bobAddr, map[string]string{"BTC": "0", "USDT": "100000"})
	require.NoError(t, dbMgr.ForceFlush())

	// Block 1: Alice 挂单
	block1 := &pb.Block{
		BlockHash: "b1",
		Header:    &pb.BlockHeader{Height: 1},
		Body:      []*pb.AnyTx{createSellOrder("s1", aliceAddr, "BTC", "USDT", "40000", "1")},
	}
	require.NoError(t, executor.CommitFinalizedBlock(block1))
	require.NoError(t, dbMgr.ForceFlush())

	// Block 2: Bob 挂单 @ 60000，成交价应该是 Alice 的 40000
	// 预期：
	// 1. 下单瞬间：冻结 1.0 * 60000 = 60000 USDT。Liquid: 40000, Frozen: 60000
	// 2. 撮合成交：
	//    - 实际支出的 Frozen = 1.0 * 60000 (按冻结时价格释放)
	//    - 实际支付给 Alice = 1.0 * 40000
	//    - 退回用户 Liquid = 1.0 * (60000 - 40000) = 20000
	// 最终 Bob: Liquid BTC 1.0, Liquid USDT (40000 + 20000) = 60000, Frozen USDT 0.
	buyOrder := createBuyOrder("b1", bobAddr, "BTC", "USDT", "60000", "1")
	block2 := &pb.Block{
		BlockHash: "b2",
		Header:    &pb.BlockHeader{Height: 2, PrevBlockHash: "b1"},
		Body:      []*pb.AnyTx{buyOrder},
	}
	require.NoError(t, executor.CommitFinalizedBlock(block2))
	require.NoError(t, dbMgr.ForceFlush())

	bobUSDT := getE2EBalance(t, dbMgr, bobAddr, "USDT")
	bobBTC := getE2EBalance(t, dbMgr, bobAddr, "BTC")
	t.Logf("Bob USDT Balance: %s, Frozen: %s", bobUSDT.Balance, bobUSDT.OrderFrozenBalance)
	assert.Equal(t, "60000", bobUSDT.Balance, "Bob should get 20000 USDT refund for price improvement")
	assert.Equal(t, "0", bobUSDT.OrderFrozenBalance)
	assert.Equal(t, "1", bobBTC.Balance)
}

// TestOrderBalance_RemoveOrder 验证撤单时的余额解冻精度
func TestOrderBalance_RemoveOrder(t *testing.T) {
	tmpDir := t.TempDir()
	dbMgr, err := db.NewManager(tmpDir, logs.NewNodeLogger("test", 0))
	require.NoError(t, err)
	defer dbMgr.Close()
	dbMgr.InitWriteQueue(10, 50*time.Millisecond)

	registry := vm.NewHandlerRegistry()
	require.NoError(t, vm.RegisterDefaultHandlers(registry))
	executor := vm.NewExecutor(dbMgr, registry, nil)

	aliceAddr := "alice"
	createE2ETestAccount(t, dbMgr, aliceAddr, map[string]string{"BTC": "0", "USDT": "100000"})
	require.NoError(t, dbMgr.ForceFlush())

	// Alice 挂买单: 1.0 BTC @ 60000 USDT，但只成交了 0.5 BTC (假想撮合，或者直接撤单)
	// 为了简单直接测试撤单：挂单 60000，冻结 60000。撤单应退回 60000。
	buyOrder := createBuyOrder("b1", aliceAddr, "BTC", "USDT", "60000", "1")
	block1 := &pb.Block{
		BlockHash: "b1",
		Header:    &pb.BlockHeader{Height: 1},
		Body:      []*pb.AnyTx{buyOrder},
	}
	require.NoError(t, executor.CommitFinalizedBlock(block1))
	require.NoError(t, dbMgr.ForceFlush())

	// Block 2: 撤单 b1
	removeTx := &pb.AnyTx{
		Content: &pb.AnyTx_OrderTx{
			OrderTx: &pb.OrderTx{
				Base: &pb.BaseMessage{
					FromAddress: aliceAddr,
					TxId:        "remove_b1",
				},
				Op:         pb.OrderOp_REMOVE,
				OpTargetId: "b1",
			},
		},
	}
	block2 := &pb.Block{
		BlockHash: "b2",
		Header:    &pb.BlockHeader{Height: 2, PrevBlockHash: "b1"},
		Body:      []*pb.AnyTx{removeTx},
	}
	require.NoError(t, executor.CommitFinalizedBlock(block2))
	require.NoError(t, dbMgr.ForceFlush())

	aliceUSDT := getE2EBalance(t, dbMgr, aliceAddr, "USDT")
	assert.Equal(t, "100000", aliceUSDT.Balance)
	assert.Equal(t, "0", aliceUSDT.OrderFrozenBalance)
}

func createE2ETestAccount(t *testing.T, dbMgr *db.Manager, address string, balances map[string]string) {
	account := &pb.Account{Address: address}
	accountData, err := proto.Marshal(account)
	require.NoError(t, err)
	dbMgr.EnqueueSet(keys.KeyAccount(address), string(accountData))

	for token, amount := range balances {
		bal := &pb.TokenBalanceRecord{
			Address: address,
			Token:   token,
			Balance: &pb.TokenBalance{
				Balance:            amount,
				OrderFrozenBalance: "0",
				MinerLockedBalance: "0",
			},
		}
		balData, err := proto.Marshal(bal)
		require.NoError(t, err)
		dbMgr.EnqueueSet(keys.KeyBalance(address, token), string(balData))
	}
}

func createSellOrder(txID, fromAddr, baseToken, quoteToken, price, amount string) *pb.AnyTx {
	return &pb.AnyTx{
		Content: &pb.AnyTx_OrderTx{
			OrderTx: &pb.OrderTx{
				Base: &pb.BaseMessage{
					TxId:        txID,
					FromAddress: fromAddr,
				},
				BaseToken:  baseToken,
				QuoteToken: quoteToken,
				Op:         pb.OrderOp_ADD,
				Side:       pb.OrderSide_SELL,
				Price:      price,
				Amount:     amount,
			},
		},
	}
}

func createBuyOrder(txID, fromAddr, baseToken, quoteToken, price, amount string) *pb.AnyTx {
	return &pb.AnyTx{
		Content: &pb.AnyTx_OrderTx{
			OrderTx: &pb.OrderTx{
				Base: &pb.BaseMessage{
					TxId:        txID,
					FromAddress: fromAddr,
				},
				BaseToken:  baseToken,
				QuoteToken: quoteToken,
				Op:         pb.OrderOp_ADD,
				Side:       pb.OrderSide_BUY,
				Price:      price,
				Amount:     amount,
			},
		},
	}
}

func getE2EBalance(t *testing.T, dbMgr *db.Manager, address, token string) *pb.TokenBalance {
	raw, err := dbMgr.Get(keys.KeyBalance(address, token))
	require.NoError(t, err)
	require.NotNil(t, raw, "balance should exist for %s %s", address, token)

	var rec pb.TokenBalanceRecord
	require.NoError(t, proto.Unmarshal(raw, &rec))
	require.NotNil(t, rec.Balance, "balance payload should not be nil")
	return rec.Balance
}
