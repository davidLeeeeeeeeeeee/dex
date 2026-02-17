package db

import (
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
	"google.golang.org/protobuf/proto"
)

func TestMinerCacheRefreshByEpoch(t *testing.T) {
	db, err := pebble.Open("", &pebble.Options{FS: vfs.NewMem()})
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	logger := logs.NewNodeLogger("test_miner_cache", 0)
	indexMgr, err := NewMinerIndexManager(db, logger)
	if err != nil {
		t.Fatalf("new index manager: %v", err)
	}

	mgr := &Manager{
		Db:       db,
		IndexMgr: indexMgr,
		Logger:   logger,
	}

	if err := putMinerAccount(db, 1, &pb.Account{Address: "miner_1", Ip: "127.0.0.1:6001", IsMiner: true}); err != nil {
		t.Fatalf("put miner1: %v", err)
	}
	if err := putMinerAccount(db, 2, &pb.Account{Address: "miner_2", Ip: "127.0.0.1:6002", IsMiner: true}); err != nil {
		t.Fatalf("put miner2: %v", err)
	}
	if err := mgr.IndexMgr.RebuildBitmapFromDB(); err != nil {
		t.Fatalf("rebuild bitmap: %v", err)
	}

	if err := mgr.RefreshMinerParticipantsForEpoch(0); err != nil {
		t.Fatalf("refresh epoch 0: %v", err)
	}
	miners, err := mgr.GetRandomMinersFast(2)
	if err != nil {
		t.Fatalf("get miners epoch 0: %v", err)
	}
	if len(miners) != 2 {
		t.Fatalf("expected 2 miners in epoch 0 cache, got %d", len(miners))
	}

	// Change DB state inside the same epoch: cache should remain unchanged.
	if err := putMinerAccount(db, 1, &pb.Account{Address: "miner_1", Ip: "127.0.0.1:6001", IsMiner: false}); err != nil {
		t.Fatalf("update miner1 to non-miner: %v", err)
	}
	miners, err = mgr.GetRandomMinersFast(2)
	if err != nil {
		t.Fatalf("get miners same epoch after db change: %v", err)
	}
	if len(miners) != 2 {
		t.Fatalf("expected cache to stay at 2 miners in same epoch, got %d", len(miners))
	}

	// Advance epoch: cache should refresh and drop miner_1.
	if err := mgr.RefreshMinerParticipantsForEpoch(1); err != nil {
		t.Fatalf("refresh epoch 1: %v", err)
	}
	miners, err = mgr.GetRandomMinersFast(2)
	if err != nil {
		t.Fatalf("get miners epoch 1: %v", err)
	}
	if len(miners) != 1 {
		t.Fatalf("expected 1 miner after epoch refresh, got %d", len(miners))
	}
	if miners[0].Address != "miner_2" {
		t.Fatalf("expected remaining miner to be miner_2, got %s", miners[0].Address)
	}
}

func putMinerAccount(db *pebble.DB, idx uint64, account *pb.Account) error {
	accountBytes, err := proto.Marshal(account)
	if err != nil {
		return err
	}
	accountKey := keys.KeyAccount(account.Address)
	indexKey := keys.KeyIndexToAccount(idx)
	b := db.NewBatch()
	defer b.Close()
	if err := b.Set([]byte(accountKey), accountBytes, nil); err != nil {
		return err
	}
	if err := b.Set([]byte(indexKey), []byte(accountKey), nil); err != nil {
		return err
	}
	return b.Commit(pebble.Sync)
}
