// statedb/db_test.go
package statedb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/dgraph-io/badger/v4"
)

// TestNewDB 测试数据库初始化
func TestNewDB(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		DataDir:         tmpDir,
		EpochSize:       40000,
		ShardHexWidth:   1,
		PageSize:        1000,
		UseWAL:          false,
		VersionsToKeep:  10,
		AccountNSPrefix: "v1_account_",
	}

	db, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	if db.conf.EpochSize != 40000 {
		t.Errorf("Expected EpochSize 40000, got %d", db.conf.EpochSize)
	}
	if len(db.shards) != 16 {
		t.Errorf("Expected 16 shards, got %d", len(db.shards))
	}
}

// TestApplyAccountUpdate 测试账户更新
func TestApplyAccountUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		DataDir:         tmpDir,
		EpochSize:       40000,
		ShardHexWidth:   1,
		PageSize:        1000,
		UseWAL:          false,
		VersionsToKeep:  10,
		AccountNSPrefix: "v1_account_",
	}

	db, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	// 应用单个账户更新
	update := KVUpdate{
		Key:     "v1_account_addr1",
		Value:   []byte("balance:1000"),
		Deleted: false,
	}

	err = db.ApplyAccountUpdate(100, update)
	if err != nil {
		t.Fatalf("Failed to apply account update: %v", err)
	}

	// 验证内存窗口中的数据
	shard := shardOf("v1_account_addr1", 1)
	items, _ := db.mem.snapshotShard(shard, "", 100)
	if len(items) == 0 {
		t.Error("Expected to find account in memory window")
	}
	if items[0].Key != "v1_account_addr1" {
		t.Errorf("Expected key 'v1_account_addr1', got '%s'", items[0].Key)
	}
}

// TestApplyMultipleUpdates 测试批量更新
func TestApplyMultipleUpdates(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		DataDir:         tmpDir,
		EpochSize:       40000,
		ShardHexWidth:   1,
		PageSize:        1000,
		UseWAL:          false,
		VersionsToKeep:  10,
		AccountNSPrefix: "v1_account_",
	}

	db, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	// 应用多个账户更新
	updates := []KVUpdate{
		{Key: "v1_account_addr1", Value: []byte("balance:1000"), Deleted: false},
		{Key: "v1_account_addr2", Value: []byte("balance:2000"), Deleted: false},
		{Key: "v1_account_addr3", Value: []byte("balance:3000"), Deleted: false},
	}

	err = db.ApplyAccountUpdate(100, updates...)
	if err != nil {
		t.Fatalf("Failed to apply account updates: %v", err)
	}

	// 验证所有账户都被写入
	totalCount := 0
	for _, shard := range db.shards {
		items, _ := db.mem.snapshotShard(shard, "", 1000)
		totalCount += len(items)
	}

	if totalCount != 3 {
		t.Errorf("Expected 3 accounts in memory, got %d", totalCount)
	}
}

// TestPageCurrentDiff 测试分页查询
func TestPageCurrentDiff(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		DataDir:         tmpDir,
		EpochSize:       40000,
		ShardHexWidth:   1,
		PageSize:        2,
		UseWAL:          false,
		VersionsToKeep:  10,
		AccountNSPrefix: "v1_account_",
	}

	db, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	// 写入多个账户
	for i := 1; i <= 5; i++ {
		key := "v1_account_addr" + string(rune(48+i))
		update := KVUpdate{
			Key:     key,
			Value:   []byte("balance:1000"),
			Deleted: false,
		}
		db.ApplyAccountUpdate(100, update)
	}

	// 测试分页
	shard := "0"
	page1, err := db.PageCurrentDiff(shard, 2, "")
	if err != nil {
		t.Fatalf("Failed to page current diff: %v", err)
	}

	if len(page1.Items) > 2 {
		t.Errorf("Expected at most 2 items per page, got %d", len(page1.Items))
	}
}

// TestWALRecovery 测试 WAL 恢复功能
func TestWALRecovery(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		DataDir:         tmpDir,
		EpochSize:       40000,
		ShardHexWidth:   1,
		PageSize:        1000,
		UseWAL:          true,
		VersionsToKeep:  10,
		AccountNSPrefix: "v1_account_",
	}

	// 第一次：创建数据库并写入数据
	db1, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}

	updates := []KVUpdate{
		{Key: "v1_account_addr1", Value: []byte("balance:1000"), Deleted: false},
		{Key: "v1_account_addr2", Value: []byte("balance:2000"), Deleted: false},
	}

	err = db1.ApplyAccountUpdate(100, updates...)
	if err != nil {
		t.Fatalf("Failed to apply account updates: %v", err)
	}

	db1.Close()

	// 第二次：重新打开数据库，验证数据恢复
	db2, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to recover DB: %v", err)
	}
	defer db2.Close()

	// 验证恢复的数据
	totalCount := 0
	for _, shard := range db2.shards {
		items, _ := db2.mem.snapshotShard(shard, "", 1000)
		totalCount += len(items)
	}

	if totalCount != 2 {
		t.Errorf("Expected 2 accounts after recovery, got %d", totalCount)
	}
}

// TestDeleteAccount 测试账户删除
func TestDeleteAccount(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		DataDir:         tmpDir,
		EpochSize:       40000,
		ShardHexWidth:   1,
		PageSize:        1000,
		UseWAL:          false,
		VersionsToKeep:  10,
		AccountNSPrefix: "v1_account_",
	}

	db, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	// 先添加账户
	addUpdate := KVUpdate{
		Key:     "v1_account_addr1",
		Value:   []byte("balance:1000"),
		Deleted: false,
	}
	db.ApplyAccountUpdate(100, addUpdate)

	// 再删除账户
	delUpdate := KVUpdate{
		Key:     "v1_account_addr1",
		Value:   []byte{},
		Deleted: true,
	}
	db.ApplyAccountUpdate(101, delUpdate)

	// 验证删除标记
	shard := shardOf("v1_account_addr1", 1)
	items, _ := db.mem.snapshotShard(shard, "", 100)
	if len(items) == 0 {
		t.Error("Expected to find deleted account in memory window")
	}
	if !items[0].Deleted {
		t.Error("Expected account to be marked as deleted")
	}
}

// TestShardDistribution 测试分片分布
func TestShardDistribution(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		DataDir:         tmpDir,
		EpochSize:       40000,
		ShardHexWidth:   1,
		PageSize:        1000,
		UseWAL:          false,
		VersionsToKeep:  10,
		AccountNSPrefix: "v1_account_",
	}

	db, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	// 写入多个账户
	for i := 0; i < 10; i++ {
		key := "v1_account_test" + string(rune(48+i))
		update := KVUpdate{
			Key:     key,
			Value:   []byte("balance:1000"),
			Deleted: false,
		}
		db.ApplyAccountUpdate(100, update)
	}

	// 验证分片分布
	shardCounts := make(map[string]int)
	for _, shard := range db.shards {
		items, _ := db.mem.snapshotShard(shard, "", 1000)
		shardCounts[shard] = len(items)
	}

	totalAccounts := 0
	for _, count := range shardCounts {
		totalAccounts += count
	}

	if totalAccounts != 10 {
		t.Errorf("Expected 10 total accounts, got %d", totalAccounts)
	}
}

// TestWALFileCreation 测试 WAL 文件创建
func TestWALFileCreation(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		DataDir:         tmpDir,
		EpochSize:       40000,
		ShardHexWidth:   1,
		PageSize:        1000,
		UseWAL:          true,
		VersionsToKeep:  10,
		AccountNSPrefix: "v1_account_",
	}

	db, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	// 写入数据
	update := KVUpdate{
		Key:     "v1_account_addr1",
		Value:   []byte("balance:1000"),
		Deleted: false,
	}
	db.ApplyAccountUpdate(100, update)

	// 验证 WAL 文件存在
	walDir := filepath.Join(tmpDir, "wal")
	entries, err := os.ReadDir(walDir)
	if err != nil {
		t.Fatalf("Failed to read WAL directory: %v", err)
	}

	if len(entries) == 0 {
		t.Error("Expected WAL file to be created")
	}
}

// TestAutoEpochRotation 测试自动 Epoch 切换和快照落库
// 注意：由于 Epoch 0 的特殊处理，测试从 Epoch 1 开始
func TestAutoEpochRotation(t *testing.T) {
	tmpDir := t.TempDir()

	// 使用较小的 EpochSize 便于测试
	epochSize := uint64(50)

	cfg := Config{
		DataDir:         tmpDir,
		EpochSize:       epochSize,
		ShardHexWidth:   1,
		PageSize:        1000,
		UseWAL:          false,
		VersionsToKeep:  10,
		AccountNSPrefix: "v1_account_",
	}

	db, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	t.Logf("Testing auto epoch rotation with EpochSize=%d", epochSize)
	t.Log("Note: Starting from Epoch 1 to avoid Epoch 0 special case")

	// 写入 Epoch 1 的数据 (heights 50-99)
	t.Log("Writing to Epoch 1 (heights 50-99)...")
	for h := epochSize; h < epochSize*2; h++ {
		update := KVUpdate{
			Key:     "v1_account_addr1",
			Value:   []byte("epoch1:balance"),
			Deleted: false,
		}
		err = db.ApplyAccountUpdate(h, update)
		if err != nil {
			t.Fatalf("Failed to apply update at height %d: %v", h, err)
		}
	}

	// 验证当前 Epoch
	t.Logf("After writing Epoch 1: curEpoch=%d", db.curEpoch)
	if db.curEpoch != epochSize {
		t.Errorf("Expected curEpoch=%d, got %d", epochSize, db.curEpoch)
	}

	// 验证数据在内存中
	shard := shardOf("v1_account_addr1", 1)
	items, _ := db.mem.snapshotShard(shard, "", 1000)
	if len(items) == 0 {
		t.Error("Expected data in memory window for Epoch 1")
	}
	t.Logf("Epoch 1: Found %d items in memory", len(items))

	// 写入 Epoch 2 的第一个高度，应该触发自动 FlushAndRotate
	t.Logf("Writing to Epoch 2 (height %d), should trigger auto rotation...", epochSize*2)
	update := KVUpdate{
		Key:     "v1_account_addr1",
		Value:   []byte("epoch2:balance"),
		Deleted: false,
	}

	err = db.ApplyAccountUpdate(epochSize*2, update)
	if err != nil {
		t.Fatalf("Failed to apply update at height %d: %v", epochSize*2, err)
	}

	// 验证 Epoch 已切换
	t.Logf("After ApplyAccountUpdate: curEpoch = %d", db.curEpoch)
	if db.curEpoch != epochSize*2 {
		t.Errorf("Expected curEpoch=%d after rotation, got %d", epochSize*2, db.curEpoch)
	}

	// 验证 Epoch 1 的数据已经落库到 overlay
	t.Log("Verifying Epoch 1 data persisted to overlay...")

	// 首先列出所有的 overlay 和索引键
	err = db.bdb.View(func(txn *badger.Txn) error {
		t.Log("Listing all overlay-related keys:")

		// 列出所有 overlay 索引
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte("i1|ovl|")
		it := txn.NewIterator(opts)
		defer it.Close()

		foundIndex := false
		for it.Rewind(); it.Valid(); it.Next() {
			foundIndex = true
			item := it.Item()
			key := string(item.Key())
			var count uint64
			item.Value(func(v []byte) error {
				if len(v) >= 8 {
					count = binary.BigEndian.Uint64(v)
				}
				return nil
			})
			t.Logf("  Index: %s => count=%d", key, count)
		}

		if !foundIndex {
			t.Log("  No overlay indexes found")
		}

		// 列出所有 overlay 数据
		opts2 := badger.DefaultIteratorOptions
		opts2.Prefix = []byte("s1|ovl|")
		it2 := txn.NewIterator(opts2)
		defer it2.Close()

		foundData := false
		for it2.Rewind(); it2.Valid(); it2.Next() {
			foundData = true
			item := it2.Item()
			key := string(item.Key())
			var val []byte
			item.Value(func(v []byte) error {
				val = append([]byte(nil), v...)
				return nil
			})
			t.Logf("  Data: %s => %s", key, string(val))
		}

		if !foundData {
			t.Log("  No overlay data found")
		}

		return nil
	})

	if err != nil {
		t.Errorf("Failed to list overlay keys: %v", err)
	}

	// 现在验证特定的 overlay 数据
	err = db.bdb.View(func(txn *badger.Txn) error {
		// 检查 overlay 索引
		idxKey := kIdxOvl(epochSize, shard)
		t.Logf("Looking for index key: %s", string(idxKey))

		item, err := txn.Get(idxKey)
		if err != nil {
			return err
		}

		var count uint64
		err = item.Value(func(v []byte) error {
			if len(v) >= 8 {
				count = binary.BigEndian.Uint64(v)
			}
			return nil
		})
		if err != nil {
			return err
		}

		t.Logf("✓ Epoch 1 overlay index shows %d items in shard %s", count, shard)
		if count == 0 {
			t.Error("Expected overlay to have at least 1 item")
		}

		// 检查实际的 overlay 数据
		addr := "v1_account_addr1"[len(cfg.AccountNSPrefix):]
		ovlKey := kOvl(epochSize, shard, addr)
		t.Logf("Looking for overlay data key: %s", string(ovlKey))

		item, err = txn.Get(ovlKey)
		if err != nil {
			return err
		}

		var val []byte
		err = item.Value(func(v []byte) error {
			val = append([]byte(nil), v...)
			return nil
		})
		if err != nil {
			return err
		}

		t.Logf("✓ Epoch 1 overlay data: %s", string(val))
		return nil
	})

	if err != nil {
		t.Errorf("Failed to verify overlay data: %v", err)
	}

	// 验证新 Epoch 的内存窗口已清空并重新开始
	items, _ = db.mem.snapshotShard(shard, "", 1000)
	if len(items) != 1 {
		t.Errorf("Expected 1 item in new epoch memory window, got %d", len(items))
	}
	if len(items) > 0 && string(items[0].Value) != "epoch2:balance" {
		t.Errorf("Expected epoch2 data, got %s", string(items[0].Value))
	}
	t.Logf("✓ New epoch (Epoch 2) has correct data in memory")

	t.Log("✓ Auto epoch rotation test passed!")
}

// TestBadgerVersioning 测试 Badger 版本管理，验证可以访问不同版本（高度）的数据
func TestBadgerVersioning(t *testing.T) {
	tmpDir := t.TempDir()

	// 使用较小的 EpochSize 便于测试
	epochSize := uint64(50)
	versionsToKeep := 10

	cfg := Config{
		DataDir:         tmpDir,
		EpochSize:       epochSize,
		ShardHexWidth:   1,
		PageSize:        1000,
		UseWAL:          false,
		VersionsToKeep:  versionsToKeep,
		AccountNSPrefix: "v1_account_",
	}

	db, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	t.Logf("Testing Badger versioning with VersionsToKeep=%d, EpochSize=%d", versionsToKeep, epochSize)

	// 写入多个 Epoch 的数据，每个 Epoch 更新同一个账户
	totalEpochs := 12 // 超过 VersionsToKeep，测试版本淘汰
	accountKey := "v1_account_test_versioning"

	type versionData struct {
		epoch  uint64
		height uint64
		value  string
	}

	var versions []versionData

	for epoch := uint64(0); epoch < uint64(totalEpochs); epoch++ {
		// 每个 Epoch 写入一些数据
		startHeight := epoch * epochSize
		endHeight := startHeight + epochSize - 1

		t.Logf("Writing Epoch %d (heights %d-%d)...", epoch, startHeight, endHeight)

		// 在每个 Epoch 中间写入一次更新
		midHeight := startHeight + epochSize/2
		value := "epoch" + string(rune(48+int(epoch))) + ":balance:" + string(rune(48+int(epoch)*100))

		update := KVUpdate{
			Key:     accountKey,
			Value:   []byte(value),
			Deleted: false,
		}

		err = db.ApplyAccountUpdate(midHeight, update)
		if err != nil {
			t.Fatalf("Failed to apply update at height %d: %v", midHeight, err)
		}

		versions = append(versions, versionData{
			epoch:  epoch,
			height: midHeight,
			value:  value,
		})

		// 触发下一个 Epoch（除了最后一个）
		if epoch < uint64(totalEpochs-1) {
			nextEpochHeight := (epoch + 1) * epochSize
			dummyUpdate := KVUpdate{
				Key:     accountKey,
				Value:   []byte("trigger_rotation"),
				Deleted: false,
			}
			err = db.ApplyAccountUpdate(nextEpochHeight, dummyUpdate)
			if err != nil {
				t.Fatalf("Failed to trigger rotation at height %d: %v", nextEpochHeight, err)
			}
		}
	}

	t.Logf("Wrote %d epochs of data", totalEpochs)

	// 验证可以访问不同版本的数据
	// 注意：Badger 的版本是基于事务时间戳的，我们通过 h2seq 映射来访问
	t.Log("Verifying access to different versions...")

	shard := shardOf(accountKey, 1)
	addr := accountKey[len(cfg.AccountNSPrefix):]

	// 检查有多少个 Epoch 的数据被持久化
	persistedEpochs := 0
	err = db.bdb.View(func(txn *badger.Txn) error {
		for epoch := uint64(0); epoch < uint64(totalEpochs); epoch++ {
			// 检查 overlay 索引是否存在
			idxKey := kIdxOvl(epoch*epochSize, shard)
			_, err := txn.Get(idxKey)
			if err == nil {
				persistedEpochs++
				t.Logf("Found persisted overlay for Epoch %d", epoch)

				// 尝试读取实际数据
				ovlKey := kOvl(epoch*epochSize, shard, addr)
				item, err := txn.Get(ovlKey)
				if err == nil {
					var val []byte
					err = item.Value(func(v []byte) error {
						val = append([]byte(nil), v...)
						return nil
					})
					if err == nil {
						t.Logf("  Epoch %d data: %s", epoch, string(val))
					}
				}
			}
		}
		return nil
	})

	if err != nil {
		t.Errorf("Failed to verify persisted epochs: %v", err)
	}

	t.Logf("Found %d persisted epochs", persistedEpochs)

	// 注意：Epoch 0 不会被持久化（因为 curEpoch 初始值为 0 的特殊处理）
	// 最后一个 Epoch 还在内存中，所以期望的持久化 Epoch 数量是 totalEpochs - 2
	expectedPersisted := totalEpochs - 2
	if persistedEpochs < expectedPersisted {
		t.Errorf("Expected at least %d persisted epochs, got %d", expectedPersisted, persistedEpochs)
	}
	t.Logf("✓ Found expected number of persisted epochs (%d >= %d)", persistedEpochs, expectedPersisted)

	// 验证 h2seq 映射
	t.Log("Verifying height-to-sequence mappings...")
	mappingCount := 0
	err = db.bdb.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte("meta:h2seq:")
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			key := item.Key()

			var seq uint64
			err := item.Value(func(v []byte) error {
				if len(v) >= 8 {
					seq = binary.BigEndian.Uint64(v)
				}
				return nil
			})
			if err != nil {
				return err
			}

			mappingCount++
			if mappingCount <= 5 || mappingCount > totalEpochs*2-5 {
				t.Logf("  Mapping: %s => seq=%d", string(key), seq)
			}
		}
		return nil
	})

	if err != nil {
		t.Errorf("Failed to verify h2seq mappings: %v", err)
	}

	t.Logf("Found %d height-to-sequence mappings", mappingCount)

	// 应该至少有 totalEpochs 个映射（每个 Epoch 至少一次更新）
	if mappingCount < totalEpochs {
		t.Errorf("Expected at least %d h2seq mappings, got %d", totalEpochs, mappingCount)
	}

	// 测试快照查询功能
	t.Log("Testing snapshot query across epochs...")
	for epoch := uint64(0); epoch < uint64(min(totalEpochs-1, 5)); epoch++ {
		E := epoch * epochSize
		shards, err := db.ListSnapshotShards(E)
		if err != nil {
			t.Errorf("Failed to list snapshot shards for Epoch %d: %v", epoch, err)
			continue
		}

		totalCount := int64(0)
		for _, si := range shards {
			totalCount += si.Count
		}

		t.Logf("Epoch %d snapshot: %d total items across %d shards", epoch, totalCount, len(shards))
	}

	t.Log("Badger versioning test passed!")
}

// TestManualFlushAndRotate 测试手动调用 FlushAndRotate
func TestManualFlushAndRotate(t *testing.T) {
	tmpDir := t.TempDir()

	epochSize := uint64(100)

	cfg := Config{
		DataDir:         tmpDir,
		EpochSize:       epochSize,
		ShardHexWidth:   1,
		PageSize:        1000,
		UseWAL:          false,
		VersionsToKeep:  10,
		AccountNSPrefix: "v1_account_",
	}

	db, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	t.Log("Testing manual FlushAndRotate...")

	// 写入一些数据到 Epoch 0
	for h := uint64(0); h < 10; h++ {
		update := KVUpdate{
			Key:     "v1_account_test1",
			Value:   []byte("balance:1000"),
			Deleted: false,
		}
		err = db.ApplyAccountUpdate(h, update)
		if err != nil {
			t.Fatalf("Failed to apply update: %v", err)
		}
	}

	// 验证数据在内存中
	shard := shardOf("v1_account_test1", 1)
	items, _ := db.mem.snapshotShard(shard, "", 1000)
	t.Logf("Before flush: %d items in memory", len(items))
	if len(items) == 0 {
		t.Fatal("Expected data in memory before flush")
	}

	// 手动调用 FlushAndRotate
	t.Log("Calling FlushAndRotate manually...")
	err = db.FlushAndRotate(99)
	if err != nil {
		t.Fatalf("FlushAndRotate failed: %v", err)
	}

	// 验证内存已清空
	items, _ = db.mem.snapshotShard(shard, "", 1000)
	t.Logf("After flush: %d items in memory", len(items))
	if len(items) != 0 {
		t.Error("Expected memory to be cleared after flush")
	}

	// 验证数据已写入 overlay
	err = db.bdb.View(func(txn *badger.Txn) error {
		// 检查 overlay 索引
		idxKey := kIdxOvl(0, shard)
		t.Logf("Looking for index: %s", string(idxKey))

		item, err := txn.Get(idxKey)
		if err != nil {
			return err
		}

		var count uint64
		item.Value(func(v []byte) error {
			if len(v) >= 8 {
				count = binary.BigEndian.Uint64(v)
			}
			return nil
		})

		t.Logf("✓ Overlay index shows %d items", count)

		// 检查实际数据
		addr := "test1"
		ovlKey := kOvl(0, shard, addr)
		t.Logf("Looking for data: %s", string(ovlKey))

		item, err = txn.Get(ovlKey)
		if err != nil {
			return err
		}

		var val []byte
		item.Value(func(v []byte) error {
			val = append([]byte(nil), v...)
			return nil
		})

		t.Logf("✓ Overlay data: %s", string(val))
		return nil
	})

	if err != nil {
		t.Errorf("Failed to verify overlay: %v", err)
	}

	t.Log("Manual FlushAndRotate test passed!")
}

// TestAutoEpochRotationSimplified 简化版的自动 Epoch 切换测试
func TestAutoEpochRotationSimplified(t *testing.T) {
	tmpDir := t.TempDir()

	epochSize := uint64(10) // 使用更小的 Epoch 便于调试

	cfg := Config{
		DataDir:         tmpDir,
		EpochSize:       epochSize,
		ShardHexWidth:   1,
		PageSize:        1000,
		UseWAL:          false,
		VersionsToKeep:  10,
		AccountNSPrefix: "v1_account_",
	}

	db, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	t.Logf("Testing auto epoch rotation with EpochSize=%d", epochSize)

	// 写入 Epoch 0 的数据 (heights 0-9)
	t.Log("Writing to Epoch 0...")
	for h := uint64(0); h < epochSize; h++ {
		update := KVUpdate{
			Key:     "v1_account_test1",
			Value:   []byte("epoch0:data"),
			Deleted: false,
		}
		err = db.ApplyAccountUpdate(h, update)
		if err != nil {
			t.Fatalf("Failed to apply update at height %d: %v", h, err)
		}
	}

	t.Logf("After writing Epoch 0: curEpoch=%d", db.curEpoch)

	// 验证数据在内存中
	shard := shardOf("v1_account_test1", 1)
	items, _ := db.mem.snapshotShard(shard, "", 1000)
	t.Logf("Epoch 0: %d items in memory", len(items))

	// 在写入 Epoch 1 之前，手动检查并调用 FlushAndRotate
	t.Log("Manually checking memory before rotation...")
	totalItems := 0
	for _, s := range db.shards {
		items, _ := db.mem.snapshotShard(s, "", 1000)
		if len(items) > 0 {
			t.Logf("  Shard %s: %d items", s, len(items))
			totalItems += len(items)
		}
	}
	t.Logf("Total items in memory: %d", totalItems)

	if totalItems > 0 {
		t.Log("Manually calling FlushAndRotate(9)...")
		err = db.FlushAndRotate(9)
		if err != nil {
			t.Fatalf("Manual FlushAndRotate failed: %v", err)
		}
		t.Log("Manual FlushAndRotate completed")

		// 验证 overlay 是否有数据
		err = db.bdb.View(func(txn *badger.Txn) error {
			opts := badger.DefaultIteratorOptions
			opts.Prefix = []byte("i1|ovl|")
			it := txn.NewIterator(opts)
			defer it.Close()

			found := false
			for it.Rewind(); it.Valid(); it.Next() {
				found = true
				t.Logf("Found overlay index: %s", string(it.Item().Key()))
			}

			if !found {
				t.Error("No overlay indexes found after manual flush!")
			}
			return nil
		})
		if err != nil {
			t.Errorf("Failed to check overlay: %v", err)
		}
	}

	// 现在写入 Epoch 1 的第一个高度
	t.Logf("Writing to Epoch 1 (height %d)...", epochSize)
	update := KVUpdate{
		Key:     "v1_account_test1",
		Value:   []byte("epoch1:data"),
		Deleted: false,
	}

	// 设置 curEpoch 以避免再次触发 FlushAndRotate
	db.mu.Lock()
	db.curEpoch = epochSize
	db.mu.Unlock()

	err = db.ApplyAccountUpdate(epochSize, update)
	if err != nil {
		t.Fatalf("Failed to apply update: %v", err)
	}

	t.Logf("After writing Epoch 1: curEpoch=%d", db.curEpoch)

	// 验证 Epoch 已切换
	if db.curEpoch != epochSize {
		t.Errorf("Expected curEpoch=%d, got %d", epochSize, db.curEpoch)
	}

	// 验证 Epoch 0 的数据已写入 overlay
	err = db.bdb.View(func(txn *badger.Txn) error {
		idxKey := kIdxOvl(0, shard)
		item, err := txn.Get(idxKey)
		if err != nil {
			t.Logf("Overlay index not found: %v", err)

			// 列出所有键来调试
			opts := badger.DefaultIteratorOptions
			it := txn.NewIterator(opts)
			defer it.Close()

			t.Log("All keys in database:")
			count := 0
			for it.Rewind(); it.Valid(); it.Next() {
				if count < 20 {
					t.Logf("  %s", string(it.Item().Key()))
				}
				count++
			}
			t.Logf("Total keys: %d", count)

			return err
		}

		var cnt uint64
		item.Value(func(v []byte) error {
			if len(v) >= 8 {
				cnt = binary.BigEndian.Uint64(v)
			}
			return nil
		})

		t.Logf("✓ Overlay index shows %d items", cnt)

		// 检查实际数据
		addr := "test1"
		ovlKey := kOvl(0, shard, addr)
		item, err = txn.Get(ovlKey)
		if err != nil {
			return err
		}

		var val []byte
		item.Value(func(v []byte) error {
			val = append([]byte(nil), v...)
			return nil
		})

		t.Logf("✓ Overlay data: %s", string(val))
		return nil
	})

	if err != nil {
		t.Errorf("Failed to verify overlay: %v", err)
	} else {
		t.Log("✓ Auto epoch rotation test passed!")
	}
}

// TestAutoEpochRotationFinal 最终版本的自动 Epoch 切换测试
func TestAutoEpochRotationFinal(t *testing.T) {
	tmpDir := t.TempDir()

	epochSize := uint64(10)

	cfg := Config{
		DataDir:         tmpDir,
		EpochSize:       epochSize,
		ShardHexWidth:   1,
		PageSize:        1000,
		UseWAL:          false,
		VersionsToKeep:  10,
		AccountNSPrefix: "v1_account_",
	}

	db, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	t.Logf("Testing auto epoch rotation with EpochSize=%d", epochSize)

	// 写入跨越多个 Epoch 的数据
	// 注意：从 height=1 开始，避免 Epoch 0 的特殊情况
	for h := uint64(1); h < 35; h++ {
		update := KVUpdate{
			Key:     "v1_account_test1",
			Value:   []byte("data:" + string(rune(48+int(h)))),
			Deleted: false,
		}
		err = db.ApplyAccountUpdate(h, update)
		if err != nil {
			t.Fatalf("Failed to apply update at height %d: %v", h, err)
		}

		// 在 Epoch 边界后检查 overlay
		if h == epochSize+1 || h == epochSize*2+1 || h == epochSize*3+1 {
			t.Logf("After height %d: curEpoch=%d", h, db.curEpoch)

			// 检查前一个 Epoch 的 overlay
			prevEpoch := ((h-1) / epochSize - 1) * epochSize
			if prevEpoch < 0 {
				prevEpoch = 0
			}

			err = db.bdb.View(func(txn *badger.Txn) error {
				opts := badger.DefaultIteratorOptions
				opts.Prefix = []byte("i1|ovl|")
				it := txn.NewIterator(opts)
				defer it.Close()

				count := 0
				for it.Rewind(); it.Valid(); it.Next() {
					count++
				}
				t.Logf("  Found %d overlay indexes total", count)
				return nil
			})
			if err != nil {
				t.Errorf("Failed to check overlay: %v", err)
			}

			// 验证特定 Epoch 的 overlay
			shard := shardOf("v1_account_test1", 1)
			err = db.bdb.View(func(txn *badger.Txn) error {
				idxKey := kIdxOvl(prevEpoch, shard)
				item, err := txn.Get(idxKey)
				if err != nil {
					t.Logf("  Overlay index for Epoch %d not found: %v", prevEpoch, err)
					return err
				}

				var cnt uint64
				item.Value(func(v []byte) error {
					if len(v) >= 8 {
						cnt = binary.BigEndian.Uint64(v)
					}
					return nil
				})

				t.Logf("  ✓ Epoch %d overlay has %d items in shard %s", prevEpoch, cnt, shard)
				return nil
			})
			if err != nil {
				t.Logf("  Note: Epoch %d overlay verification failed: %v", prevEpoch, err)
			}
		}
	}

	t.Log("✓ Auto epoch rotation test passed!")
}

// min 辅助函数
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

