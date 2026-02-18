package statedb

import (
	"os"
	"path/filepath"
	"testing"
)

func testPebbleConfig(dir string) Config {
	return Config{
		Backend:         BackendPebble,
		DataDir:         dir,
		CheckpointEvery: 10,
		ShardHexWidth:   1,
		PageSize:        2,
		CheckpointKeep:  2,
	}
}

func TestPebbleDefaultBackend(t *testing.T) {
	cfg := testPebbleConfig(t.TempDir())
	cfg.Backend = ""

	db, err := New(cfg)
	if err != nil {
		t.Fatalf("new db failed: %v", err)
	}
	defer db.Close()

	if db.conf.Backend != BackendPebble {
		t.Fatalf("expected default backend %q, got %q", BackendPebble, db.conf.Backend)
	}

	if _, err := os.Stat(filepath.Join(cfg.DataDir, "live")); err != nil {
		t.Fatalf("expected live dir to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "checkpoints")); err != nil {
		t.Fatalf("expected checkpoints dir to exist: %v", err)
	}
}

func TestPebbleApplyAndGetWriteThrough(t *testing.T) {
	cfg := testPebbleConfig(t.TempDir())

	db, err := New(cfg)
	if err != nil {
		t.Fatalf("new db failed: %v", err)
	}
	defer db.Close()

	key := "v1_account_addr1"
	if err := db.ApplyAccountUpdate(1, KVUpdate{Key: key, Value: []byte("balance:1000")}); err != nil {
		t.Fatalf("apply update failed: %v", err)
	}

	got, exists, err := db.Get(key)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if !exists {
		t.Fatal("expected key to exist")
	}
	if string(got) != "balance:1000" {
		t.Fatalf("unexpected value: %s", string(got))
	}

	// Non-state key should be ignored by ApplyAccountUpdate.
	if err := db.ApplyAccountUpdate(2, KVUpdate{Key: "v1_txraw_x", Value: []byte("raw")}); err != nil {
		t.Fatalf("apply non-state update failed: %v", err)
	}
	_, exists, err = db.Get("v1_txraw_x")
	if err != nil {
		t.Fatalf("get non-state key failed: %v", err)
	}
	if exists {
		t.Fatal("non-state key should not be stored in statedb")
	}
}

func TestCheckpointCreatedOnEpochBoundary(t *testing.T) {
	cfg := testPebbleConfig(t.TempDir())
	cfg.CheckpointKeep = 5

	db, err := New(cfg)
	if err != nil {
		t.Fatalf("new db failed: %v", err)
	}
	defer db.Close()

	key := "v1_account_addr_cp"
	if err := db.ApplyAccountUpdate(10, KVUpdate{Key: key, Value: []byte("v10")}); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	cpPath := filepath.Join(cfg.DataDir, "checkpoints", "h_00000000000000000010")
	if _, err := os.Stat(cpPath); err != nil {
		t.Fatalf("expected checkpoint at epoch boundary: %v", err)
	}

	latest, err := db.LatestCheckpointHeight()
	if err != nil {
		t.Fatalf("latest checkpoint read failed: %v", err)
	}
	if latest != 10 {
		t.Fatalf("expected latest checkpoint 10, got %d", latest)
	}
}

func TestCheckpointKeepPrunesOld(t *testing.T) {
	cfg := testPebbleConfig(t.TempDir())
	cfg.CheckpointKeep = 2

	db, err := New(cfg)
	if err != nil {
		t.Fatalf("new db failed: %v", err)
	}
	defer db.Close()

	key := "v1_account_addr_prune"
	heights := []uint64{10, 20, 30}
	for _, h := range heights {
		if err := db.ApplyAccountUpdate(h, KVUpdate{Key: key, Value: []byte{byte(h)}}); err != nil {
			t.Fatalf("apply failed at %d: %v", h, err)
		}
	}

	cpRoot := filepath.Join(cfg.DataDir, "checkpoints")
	entries, err := os.ReadDir(cpRoot)
	if err != nil {
		t.Fatalf("read checkpoints dir failed: %v", err)
	}

	found := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			found[e.Name()] = true
		}
	}

	if found["h_00000000000000000010"] {
		t.Fatal("old checkpoint should be pruned")
	}
	if !found["h_00000000000000000020"] || !found["h_00000000000000000030"] {
		t.Fatalf("expected checkpoints for 20 and 30, got %#v", found)
	}
}

func TestPageCurrentStateAndCheckpointSnapshot(t *testing.T) {
	cfg := testPebbleConfig(t.TempDir())
	cfg.PageSize = 1
	cfg.CheckpointKeep = 5

	db, err := New(cfg)
	if err != nil {
		t.Fatalf("new db failed: %v", err)
	}
	defer db.Close()

	k1 := "v1_account_page_1"
	k2 := "v1_account_page_2"
	if err := db.ApplyAccountUpdate(9, KVUpdate{Key: k1, Value: []byte("v9-1")}); err != nil {
		t.Fatalf("apply 9 failed: %v", err)
	}
	if err := db.ApplyAccountUpdate(10, KVUpdate{Key: k2, Value: []byte("v10-2")}); err != nil {
		t.Fatalf("apply 10 failed: %v", err)
	}

	// Mutate one key after checkpoint, snapshot@10 should keep old value.
	if err := db.ApplyAccountUpdate(11, KVUpdate{Key: k2, Value: []byte("v11-2")}); err != nil {
		t.Fatalf("apply 11 failed: %v", err)
	}

	shard := shardOf(k2, cfg.ShardHexWidth)

	// Page live state.
	page1, err := db.PageCurrentDiff(shard, 1, "")
	if err != nil {
		t.Fatalf("page current failed: %v", err)
	}
	if len(page1.Items) > 1 {
		t.Fatalf("expected <=1 item, got %d", len(page1.Items))
	}

	// Read checkpoint@10 shard page and verify k2 value at checkpoint.
	pageCP, err := db.PageSnapshotShard(10, shard, 0, 10, "")
	if err != nil {
		t.Fatalf("page snapshot failed: %v", err)
	}
	found := false
	for _, item := range pageCP.Items {
		if item.Key == k2 {
			found = true
			if string(item.Value) != "v10-2" {
				t.Fatalf("expected checkpoint value v10-2, got %s", string(item.Value))
			}
		}
	}
	if !found {
		t.Fatal("expected k2 in checkpoint page")
	}
}
