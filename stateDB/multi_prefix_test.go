// statedb/multi_prefix_test.go
package statedb

import (
	"testing"

	"github.com/dgraph-io/badger/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsStatefulKey 测试 isStatefulKey 方法
// 验证：
// 1. 所有支持的前缀都返回 true
// 2. 不支持的前缀返回 false
func TestIsStatefulKey(t *testing.T) {
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
	require.NoError(t, err)
	defer db.Close()

	// 测试支持的前缀
	testCases := []struct {
		key      string
		expected bool
		desc     string
	}{
		// 支持的前缀
		{"v1_account_alice", true, "Account key should be stateful"},
		{"v1_freeze_alice_BTC", true, "Freeze key should be stateful"},
		{"v1_order_order123", true, "Order key should be stateful"},
		{"v1_token_token123", true, "Token key should be stateful"},
		{"v1_token_registry", true, "Token registry should be stateful"},
		{"v1_recharge_record_alice_tweak123", true, "Recharge record should be stateful"},

		// 不支持的前缀
		{"v1_transfer_history_tx123", false, "Transfer history should not be stateful"},
		{"v1_order_index_BTC_USDT", false, "Order index should not be stateful"},
		{"v1_freeze_history_tx123", false, "Freeze history should not be stateful"},
		{"v1_miner_history_tx123", false, "Miner history should not be stateful"},
		{"v1_candidate_history_tx123", false, "Candidate history should not be stateful"},
		{"v2_account_alice", false, "Wrong version prefix should not be stateful"},
		{"account_alice", false, "Missing version prefix should not be stateful"},
		{"", false, "Empty key should not be stateful"},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			result := db.isStatefulKey(tc.key)
			assert.Equal(t, tc.expected, result, tc.desc)
		})
	}
}

// TestMultiPrefixSync 测试多前缀数据同步到 StateDB
// 验证：
// 1. 所有支持的数据类型都能同步到 StateDB
// 2. 不支持的数据类型不会同步到 StateDB
func TestMultiPrefixSync(t *testing.T) {
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
	require.NoError(t, err)
	defer db.Close()

	height := uint64(100)

	// 准备各种类型的数据
	updates := []KVUpdate{
		// 应该同步的数据
		{Key: "v1_account_alice", Value: []byte("account_data"), Deleted: false},
		{Key: "v1_freeze_alice_BTC", Value: []byte("true"), Deleted: false},
		{Key: "v1_order_order123", Value: []byte("order_data"), Deleted: false},
		{Key: "v1_token_token123", Value: []byte("token_data"), Deleted: false},
		{Key: "v1_token_registry", Value: []byte("registry_data"), Deleted: false},
		{Key: "v1_recharge_record_alice_tweak123", Value: []byte("recharge_data"), Deleted: false},

		// 不应该同步的数据
		{Key: "v1_transfer_history_tx123", Value: []byte("history_data"), Deleted: false},
		{Key: "v1_order_index_BTC_USDT", Value: []byte("index_data"), Deleted: false},
		{Key: "v1_freeze_history_tx123", Value: []byte("freeze_history"), Deleted: false},
	}

	// 应用更新
	err = db.ApplyAccountUpdate(height, updates...)
	require.NoError(t, err)

	// 验证应该同步的数据在内存窗口中
	statefulKeys := []string{
		"v1_account_alice",
		"v1_freeze_alice_BTC",
		"v1_order_order123",
		"v1_token_token123",
		"v1_token_registry",
		"v1_recharge_record_alice_tweak123",
	}

	for _, key := range statefulKeys {
		shard := shardOf(key, cfg.ShardHexWidth)
		items, _ := db.mem.snapshotShard(shard, "", 1000)

		found := false
		for _, item := range items {
			if item.Key == key {
				found = true
				break
			}
		}
		assert.True(t, found, "Stateful key %s should be in memory window", key)
	}

	// 验证不应该同步的数据不在内存窗口中
	nonStatefulKeys := []string{
		"v1_transfer_history_tx123",
		"v1_order_index_BTC_USDT",
		"v1_freeze_history_tx123",
	}

	for _, key := range nonStatefulKeys {
		shard := shardOf(key, cfg.ShardHexWidth)
		items, _ := db.mem.snapshotShard(shard, "", 1000)

		found := false
		for _, item := range items {
			if item.Key == key {
				found = true
				break
			}
		}
		assert.False(t, found, "Non-stateful key %s should not be in memory window", key)
	}

	t.Log("✅ Multi-prefix sync test passed")
}

// TestMultiPrefixFlushAndRotate 测试多前缀数据的 FlushAndRotate
// 验证：
// 1. 所有支持的数据类型都能正确写入 Badger overlay
// 2. 使用完整 key 作为 overlay key 的 addr 参数
func TestMultiPrefixFlushAndRotate(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		DataDir:         tmpDir,
		EpochSize:       100, // 小的 epoch 便于测试
		ShardHexWidth:   1,
		PageSize:        1000,
		UseWAL:          false,
		VersionsToKeep:  10,
		AccountNSPrefix: "v1_account_",
	}

	db, err := New(cfg)
	require.NoError(t, err)
	defer db.Close()

	// 写入各种类型的数据
	updates := []KVUpdate{
		{Key: "v1_account_alice", Value: []byte("account_data_alice"), Deleted: false},
		{Key: "v1_freeze_bob_BTC", Value: []byte("true"), Deleted: false},
		{Key: "v1_order_order456", Value: []byte("order_data_456"), Deleted: false},
		{Key: "v1_token_token789", Value: []byte("token_data_789"), Deleted: false},
		{Key: "v1_recharge_record_charlie_tweak999", Value: []byte("recharge_data_999"), Deleted: false},
	}

	// 在 epoch 0 写入数据（高度 1-99）
	for i := uint64(1); i <= 99; i++ {
		err = db.ApplyAccountUpdate(i, updates...)
		require.NoError(t, err)
	}

	// 触发 FlushAndRotate（epoch 0 结束，高度 99）
	// epochOf(99, 100) = 0
	err = db.FlushAndRotate(99)
	require.NoError(t, err)

	t.Log("✅ FlushAndRotate completed for multi-prefix data")

	// 验证数据已写入 Badger
	// 注意：这里我们验证的是 overlay 数据，格式为 s1|ovl|<E>|<shard>|<full_key>
	// epochOf(99, 100) = 0
	epoch := uint64(0)
	
	for _, update := range updates {
		shard := shardOf(update.Key, cfg.ShardHexWidth)
		overlayKey := kOvl(epoch, shard, update.Key) // 使用完整 key

		var value []byte
		err = db.bdb.View(func(txn *badger.Txn) error {
			item, err := txn.Get([]byte(overlayKey))
			if err != nil {
				return err
			}
			value, err = item.ValueCopy(nil)
			return err
		})

		require.NoError(t, err, "Should find overlay data for key: %s", update.Key)
		assert.Equal(t, update.Value, value, "Value should match for key: %s", update.Key)
		t.Logf("✅ Found overlay data for %s", update.Key)
	}

	t.Log("✅ Multi-prefix FlushAndRotate test passed")
}

// TestMultiPrefixSharding 测试多前缀数据的分片分布
// 验证：
// 1. 不同前缀的数据根据完整 key 进行分片
// 2. 分片分布合理
func TestMultiPrefixSharding(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		DataDir:         tmpDir,
		EpochSize:       40000,
		ShardHexWidth:   1, // 16 个分片
		PageSize:        1000,
		UseWAL:          false,
		VersionsToKeep:  10,
		AccountNSPrefix: "v1_account_",
	}

	db, err := New(cfg)
	require.NoError(t, err)
	defer db.Close()

	// 写入大量不同类型的数据
	height := uint64(100)
	var updates []KVUpdate

	// 账户数据
	for i := 0; i < 20; i++ {
		key := "v1_account_user" + string(rune(65+i))
		updates = append(updates, KVUpdate{
			Key:     key,
			Value:   []byte("account_data"),
			Deleted: false,
		})
	}

	// 冻结标记
	for i := 0; i < 20; i++ {
		key := "v1_freeze_user" + string(rune(65+i)) + "_BTC"
		updates = append(updates, KVUpdate{
			Key:     key,
			Value:   []byte("true"),
			Deleted: false,
		})
	}

	// 订单数据
	for i := 0; i < 20; i++ {
		key := "v1_order_order" + string(rune(65+i))
		updates = append(updates, KVUpdate{
			Key:     key,
			Value:   []byte("order_data"),
			Deleted: false,
		})
	}

	// Token 数据
	for i := 0; i < 20; i++ {
		key := "v1_token_token" + string(rune(65+i))
		updates = append(updates, KVUpdate{
			Key:     key,
			Value:   []byte("token_data"),
			Deleted: false,
		})
	}

	// 充值记录
	for i := 0; i < 20; i++ {
		key := "v1_recharge_record_user" + string(rune(65+i)) + "_tweak" + string(rune(65+i))
		updates = append(updates, KVUpdate{
			Key:     key,
			Value:   []byte("recharge_data"),
			Deleted: false,
		})
	}

	// 应用所有更新
	err = db.ApplyAccountUpdate(height, updates...)
	require.NoError(t, err)

	// 统计每个分片的数据量
	shardCounts := make(map[string]int)
	for _, shard := range db.shards {
		items, _ := db.mem.snapshotShard(shard, "", 10000)
		shardCounts[shard] = len(items)
	}

	// 验证数据分布
	totalItems := 0
	for shard, count := range shardCounts {
		totalItems += count
		t.Logf("Shard %s: %d items", shard, count)
	}

	assert.Equal(t, len(updates), totalItems, "Total items should match")
	t.Logf("✅ Total %d items distributed across %d shards", totalItems, len(db.shards))

	// 验证至少有多个分片有数据（说明分片在工作）
	nonEmptyShards := 0
	for _, count := range shardCounts {
		if count > 0 {
			nonEmptyShards++
		}
	}
	assert.Greater(t, nonEmptyShards, 1, "Should have multiple non-empty shards")
	t.Logf("✅ %d non-empty shards (good distribution)", nonEmptyShards)

	t.Log("✅ Multi-prefix sharding test passed")
}

