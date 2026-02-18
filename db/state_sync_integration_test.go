package db

import (
	"dex/config"
	"dex/keys"
	"dex/logs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type syncTestOp struct {
	key string
	val []byte
	del bool
}

func (o syncTestOp) GetKey() string   { return o.key }
func (o syncTestOp) GetValue() []byte { return o.val }
func (o syncTestOp) IsDel() bool      { return o.del }

func toIfaceOps(ops ...syncTestOp) []interface{} {
	out := make([]interface{}, 0, len(ops))
	for i := range ops {
		out = append(out, ops[i])
	}
	return out
}

func TestSyncStateUpdatesMirrorsStatefulKeys(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	mgr, err := NewManagerWithConfig(root, logs.NewNodeLogger("test", 0), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { mgr.Close() })
	mgr.InitWriteQueue(128, 10*time.Millisecond)

	accountKey := keys.KeyAccount("alice_sync")
	frostKey := keys.KeyFrostVaultState("BTC", 1)
	kvOnlyKey := keys.KeyTxRaw("tx_sync_1")

	accountVal := []byte("acc-v1")
	frostVal := []byte("frost-v1")
	kvOnlyVal := []byte("txraw-v1")

	mgr.EnqueueSet(accountKey, string(accountVal))
	mgr.EnqueueSet(frostKey, string(frostVal))
	mgr.EnqueueSet(kvOnlyKey, string(kvOnlyVal))

	n, err := mgr.SyncStateUpdates(1, toIfaceOps(
		syncTestOp{key: accountKey, val: accountVal},
		syncTestOp{key: frostKey, val: frostVal},
		syncTestOp{key: kvOnlyKey, val: kvOnlyVal},
	))
	require.NoError(t, err)
	require.Equal(t, 2, n, "only stateful keys should be mirrored into stateDB")
	require.NoError(t, mgr.ForceFlush())

	kvAccount, err := mgr.GetKV(accountKey)
	require.NoError(t, err)
	sdbAccount, exists, err := mgr.stateDB.Get(accountKey)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, kvAccount, sdbAccount)

	kvFrost, err := mgr.GetKV(frostKey)
	require.NoError(t, err)
	sdbFrost, exists, err := mgr.stateDB.Get(frostKey)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, kvFrost, sdbFrost)

	_, exists, err = mgr.stateDB.Get(kvOnlyKey)
	require.NoError(t, err)
	require.False(t, exists, "kv-only key should not appear in stateDB")
}

func TestSyncStateUpdatesMirrorsDeletes(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	mgr, err := NewManagerWithConfig(root, logs.NewNodeLogger("test", 0), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { mgr.Close() })
	mgr.InitWriteQueue(128, 10*time.Millisecond)

	accountKey := keys.KeyAccount("alice_del_sync")
	accountVal := []byte("acc-v1")

	mgr.EnqueueSet(accountKey, string(accountVal))
	_, err = mgr.SyncStateUpdates(1, toIfaceOps(syncTestOp{key: accountKey, val: accountVal}))
	require.NoError(t, err)
	require.NoError(t, mgr.ForceFlush())

	mgr.EnqueueDelete(accountKey)
	n, err := mgr.SyncStateUpdates(2, toIfaceOps(syncTestOp{key: accountKey, del: true}))
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.NoError(t, mgr.ForceFlush())

	_, err = mgr.GetKV(accountKey)
	require.Error(t, err, "key should be removed from KV")

	_, exists, err := mgr.stateDB.Get(accountKey)
	require.NoError(t, err)
	require.False(t, exists, "key should be removed from stateDB")
}

func TestStateDBConfigIsAppliedFromMainConfig(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Database.StateDB.CheckpointEvery = 2
	cfg.Database.StateDB.DataDir = "custom_state"
	cfg.Database.StateDB.ShardHexWidth = 2
	cfg.Database.StateDB.PageSize = 128
	cfg.Database.StateDB.CheckpointKeep = 4

	mgr, err := NewManagerWithConfig(root, logs.NewNodeLogger("test", 0), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { mgr.Close() })
	mgr.InitWriteQueue(128, 10*time.Millisecond)

	sdbCfg := mgr.stateDB.Config()
	require.Equal(t, uint64(2), sdbCfg.CheckpointEvery)
	require.Equal(t, 2, sdbCfg.ShardHexWidth)
	require.Equal(t, 128, sdbCfg.PageSize)
	require.Equal(t, 4, sdbCfg.CheckpointKeep)

	accountKey := keys.KeyAccount("cfg_sync")
	update := syncTestOp{key: accountKey, val: []byte("cfg-v")}

	_, err = mgr.SyncStateUpdates(2, toIfaceOps(update))
	require.NoError(t, err)
	_, err = mgr.SyncStateUpdates(4, toIfaceOps(update))
	require.NoError(t, err)

	cpRoot := filepath.Join(root, "custom_state", "checkpoints")
	cp4 := filepath.Join(cpRoot, "h_00000000000000000004")
	cp2 := filepath.Join(cpRoot, "h_00000000000000000002")

	_, err = os.Stat(cp4)
	require.NoError(t, err, "latest checkpoint should exist")
	_, err = os.Stat(cp2)
	require.NoError(t, err, "checkpoint at earlier epoch boundary should exist")
}
