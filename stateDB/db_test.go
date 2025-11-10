// statedb/db_test.go
package statedb

import (
	"os"
	"path/filepath"
	"testing"
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

