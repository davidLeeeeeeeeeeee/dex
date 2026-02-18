// statedb/example_wal_usage.go
// Legacy filename kept for compatibility; examples now demonstrate
// checkpoint-based versioning with write-through updates.

package statedb

import (
	"fmt"
	"log"
)

func ExampleWriteThroughWithCheckpoint() {
	cfg := Config{
		DataDir:         "stateDB/data",
		CheckpointEvery: 100,
		ShardHexWidth:   1,
		PageSize:        1000,
		CheckpointKeep:  10,
	}

	db, err := New(cfg)
	if err != nil {
		log.Fatalf("failed to create DB: %v", err)
	}
	defer db.Close()

	// Height 100 hits checkpoint boundary and creates checkpoint h_100.
	if err := db.ApplyAccountUpdate(100, KVUpdate{
		Key:     "v1_account_0x1234",
		Value:   []byte("balance:1000"),
		Deleted: false,
	}); err != nil {
		log.Fatalf("apply failed: %v", err)
	}

	latest, err := db.LatestCheckpointHeight()
	if err != nil {
		log.Fatalf("latest checkpoint failed: %v", err)
	}
	fmt.Printf("latest checkpoint: %d\n", latest)
}

func ExampleManualCheckpoint() {
	cfg := Config{
		DataDir:         "stateDB/data",
		CheckpointEvery: 100,
		ShardHexWidth:   1,
		PageSize:        1000,
		CheckpointKeep:  5,
	}

	db, err := New(cfg)
	if err != nil {
		log.Fatalf("failed to create DB: %v", err)
	}
	defer db.Close()

	if err := db.ApplyAccountUpdate(55, KVUpdate{
		Key:     "v1_account_0xbeef",
		Value:   []byte("balance:555"),
		Deleted: false,
	}); err != nil {
		log.Fatalf("apply failed: %v", err)
	}

	// Compatibility API: force a checkpoint at height 55.
	if err := db.FlushAndRotate(55); err != nil {
		log.Fatalf("manual checkpoint failed: %v", err)
	}
}
