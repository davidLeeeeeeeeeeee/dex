package vm_test

import (
	"dex/db"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"dex/vm"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCommitFinalizedBlockMirrorsStateIntoStateDB(t *testing.T) {
	tmpDir := t.TempDir()
	dbMgr, err := db.NewManager(tmpDir, logs.NewNodeLogger("test", 0))
	require.NoError(t, err)
	defer dbMgr.Close()
	dbMgr.InitWriteQueue(64, 20*time.Millisecond)

	registry := vm.NewHandlerRegistry()
	require.NoError(t, vm.RegisterDefaultHandlers(registry))
	executor := vm.NewExecutor(dbMgr, registry, nil)

	alice := "alice_state_mirror"
	bob := "bob_state_mirror"
	createE2ETestAccount(t, dbMgr, alice, map[string]string{"FB": "100"})
	createE2ETestAccount(t, dbMgr, bob, map[string]string{"FB": "0"})
	require.NoError(t, dbMgr.ForceFlush())

	tx := &pb.AnyTx{
		Content: &pb.AnyTx_Transaction{
			Transaction: &pb.Transaction{
				Base: &pb.BaseMessage{
					TxId:        "tx_state_mirror_1",
					FromAddress: alice,
					Status:      pb.Status_PENDING,
				},
				To:           bob,
				TokenAddress: "FB",
				Amount:       "10",
			},
		},
	}

	block := &pb.Block{
		BlockHash: "block_state_mirror_1",
		Header: &pb.BlockHeader{
			Height: 1,
		},
		Body: []*pb.AnyTx{tx},
	}
	require.NoError(t, executor.CommitFinalizedBlock(block))

	statefulKeys := []string{
		keys.KeyAccount(alice),
		keys.KeyAccount(bob),
		keys.KeyBalance(alice, "FB"),
		keys.KeyBalance(bob, "FB"),
	}
	for _, key := range statefulKeys {
		_, err := dbMgr.GetKV(key)
		require.Error(t, err, "stateful key should not be persisted in KV main DB")

		stateVal, exists, err := dbMgr.GetState(key)
		require.NoError(t, err)
		require.True(t, exists, "stateDB should contain key %s", key)

		getVal, err := dbMgr.Get(key)
		require.NoError(t, err)
		require.Equal(t, getVal, stateVal, "stateDB value mismatch for key %s", key)
	}
}
