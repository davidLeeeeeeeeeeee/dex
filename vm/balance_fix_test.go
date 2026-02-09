package vm_test

import (
	"dex/db"
	"dex/logs"
	"dex/pb"
	"dex/vm"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOrderBalance_TakerAtomic 楠岃瘉 Taker 璐︽埛鍦ㄤ竴娆′氦鏄撳唴鐨勪袱娆′綑棰濆彉鏇达紙鍐荤粨+鎵ｅ噺锛夋槸鍚﹀師瀛?
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

	// Alice 鎸傚崟: 1.0 BTC @ 50000 USDT
	createE2ETestAccount(t, dbMgr, aliceAddr, map[string]string{"BTC": "10", "USDT": "0"})
	// Bob 鍚冨崟: 100000 USDT
	createE2ETestAccount(t, dbMgr, bobAddr, map[string]string{"BTC": "0", "USDT": "100000"})
	require.NoError(t, dbMgr.ForceFlush())

	// Block 1: Alice 鎸傚崟
	block1 := &pb.Block{
		BlockHash: "b1",
		Header:    &pb.BlockHeader{Height: 1},
		Body:      []*pb.AnyTx{createSellOrder("s1", aliceAddr, "BTC", "USDT", "50000", "2")},
	}
	require.NoError(t, executor.CommitFinalizedBlock(block1))
	require.NoError(t, dbMgr.ForceFlush())

	// Block 2: Bob 鍚冨崟 0.5 BTC @ 50000
	// 棰勬湡锛?
	// 1. 涓嬪崟鐬棿锛氬喕缁?0.5 * 50000 = 25000 USDT銆侺iquid: 75000, Frozen: 25000
	// 2. 鎾悎鎴愪氦锛氭墸闄?Frozen 25000锛屽鍔?BTC 0.5銆?
	// 鏈€缁?Bob: Liquid BTC 0.5, Liquid USDT 75000, Frozen USDT 0.
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

// TestOrderBalance_BuyRefund 楠岃瘉涔板崟浠ヤ綆浠锋垚浜ゆ椂鐨勯€€娆鹃€昏緫
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

	// Alice 鎸傚粔浠峰崠鍗? 1.0 BTC @ 40000 USDT
	createE2ETestAccount(t, dbMgr, aliceAddr, map[string]string{"BTC": "10", "USDT": "0"})
	// Bob 鎸傞珮浠蜂拱鍗? 1.0 BTC @ 60000 USDT
	createE2ETestAccount(t, dbMgr, bobAddr, map[string]string{"BTC": "0", "USDT": "100000"})
	require.NoError(t, dbMgr.ForceFlush())

	// Block 1: Alice 鎸傚崟
	block1 := &pb.Block{
		BlockHash: "b1",
		Header:    &pb.BlockHeader{Height: 1},
		Body:      []*pb.AnyTx{createSellOrder("s1", aliceAddr, "BTC", "USDT", "40000", "1")},
	}
	require.NoError(t, executor.CommitFinalizedBlock(block1))
	require.NoError(t, dbMgr.ForceFlush())

	// Block 2: Bob 鎸傚崟 @ 60000锛屾垚浜や环搴旇鏄?Alice 鐨?40000
	// 棰勬湡锛?
	// 1. 涓嬪崟鐬棿锛氬喕缁?1.0 * 60000 = 60000 USDT銆侺iquid: 40000, Frozen: 60000
	// 2. 鎾悎鎴愪氦锛?
	//    - 瀹為檯鏀嚭鐨?Frozen = 1.0 * 60000 (鎸夊喕缁撴椂浠锋牸閲婃斁)
	//    - 瀹為檯鏀粯缁?Alice = 1.0 * 40000
	//    - 閫€鍥炵敤鎴?Liquid = 1.0 * (60000 - 40000) = 20000
	// 鏈€缁?Bob: Liquid BTC 1.0, Liquid USDT (40000 + 20000) = 60000, Frozen USDT 0.
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

// TestOrderBalance_RemoveOrder 楠岃瘉鎾ゅ崟鏃剁殑浣欓瑙ｅ喕绮惧害
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

	// Alice 鎸備拱鍗? 1.0 BTC @ 60000 USDT锛屼絾鍙垚浜や簡 0.5 BTC (鍋囨兂鎾悎锛屾垨鑰呯洿鎺ユ挙鍗?
	// 涓轰簡绠€鍗曠洿鎺ユ祴璇曟挙鍗曪細鎸傚崟 60000锛屽喕缁?60000銆傛挙鍗曞簲閫€鍥?60000銆?
	buyOrder := createBuyOrder("b1", aliceAddr, "BTC", "USDT", "60000", "1")
	block1 := &pb.Block{
		BlockHash: "b1",
		Header:    &pb.BlockHeader{Height: 1},
		Body:      []*pb.AnyTx{buyOrder},
	}
	require.NoError(t, executor.CommitFinalizedBlock(block1))
	require.NoError(t, dbMgr.ForceFlush())

	// Block 2: 鎾ゅ崟 b1
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
