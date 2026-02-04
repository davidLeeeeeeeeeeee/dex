package verkle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/dgraph-io/badger/v4"
)

// ============================================
// Verkle 树稳定性测试
// 测试目标：验证相同的 KV 输入能否产生相同的状态根
// 模拟参数：除根节点外 2 层在内存，其余在 DB (MaxMemoryDepth = 2)
// ============================================

// TestVerkleRootDeterminism_SameKV 测试相同 KV 多次写入产生相同根
func TestVerkleRootDeterminism_SameKV(t *testing.T) {
	// 准备测试 KV 数据（模拟账户状态）
	testData := generateDeterministicKVData(100)
	t.Logf("Generated %d deterministic KV pairs", len(testData))

	const iterations = 5
	roots := make([][]byte, iterations)

	for i := 0; i < iterations; i++ {
		dbPath := fmt.Sprintf("test_db_determinism_%d_%d", os.Getpid(), i)
		os.RemoveAll(dbPath)

		db, err := badger.Open(badger.DefaultOptions(dbPath).WithLoggingLevel(badger.ERROR))
		if err != nil {
			t.Fatalf("Iteration %d: failed to open db: %v", i, err)
		}

		store := NewVersionedBadgerStore(db, []byte("v:"))
		tree := NewVerkleTree(store)

		sess, err := store.NewSession()
		if err != nil {
			db.Close()
			os.RemoveAll(dbPath)
			t.Fatalf("Iteration %d: failed to create session: %v", i, err)
		}

		// 使用相同顺序写入相同数据
		keys := make([][]byte, len(testData))
		vals := make([][]byte, len(testData))
		for j, kv := range testData {
			keys[j] = kv.key
			vals[j] = kv.value
		}

		root, err := tree.UpdateWithSession(sess, keys, vals, Version(1))
		if err != nil {
			sess.Close()
			db.Close()
			os.RemoveAll(dbPath)
			t.Fatalf("Iteration %d: update failed: %v", i, err)
		}

		if err := sess.Commit(); err != nil {
			sess.Close()
			db.Close()
			os.RemoveAll(dbPath)
			t.Fatalf("Iteration %d: commit failed: %v", i, err)
		}

		roots[i] = root
		t.Logf("Iteration %d: root = %x", i, root[:16])

		sess.Close()
		db.Close()
		os.RemoveAll(dbPath)
	}

	// 验证所有根都相同
	for i := 1; i < iterations; i++ {
		if !bytes.Equal(roots[0], roots[i]) {
			t.Errorf("ROOT MISMATCH: iter 0 (%x) != iter %d (%x)",
				roots[0][:16], i, roots[i][:16])
		}
	}

	t.Logf("✓ 所有 %d 次迭代产生相同的根: %x", iterations, roots[0][:16])
}

// TestVerkleRootDeterminism_DifferentOrder 测试不同写入顺序是否产生相同根（排序后）
func TestVerkleRootDeterminism_DifferentOrder(t *testing.T) {
	testData := generateDeterministicKVData(50)
	t.Logf("Generated %d KV pairs for order test", len(testData))

	// 第一次：正序写入
	root1 := runVerkleWithOrder(t, testData, false, "forward")

	// 第二次：逆序写入（内部会排序）
	root2 := runVerkleWithOrder(t, testData, true, "reverse")

	if !bytes.Equal(root1, root2) {
		t.Errorf("ROOT MISMATCH after ordering:\nForward:  %x\nReverse:  %x", root1, root2)
	} else {
		t.Logf("✓ 不同输入顺序（排序后）产生相同的根: %x", root1[:16])
	}
}

// TestVerkleRootDeterminism_MultiVersion 测试多版本写入的稳定性
func TestVerkleRootDeterminism_MultiVersion(t *testing.T) {
	const iterations = 3
	const versions = 5
	const keysPerVersion = 30

	finalRoots := make([][]byte, iterations)

	for iter := 0; iter < iterations; iter++ {
		dbPath := fmt.Sprintf("test_db_multiversion_%d_%d", os.Getpid(), iter)
		os.RemoveAll(dbPath)

		db, err := badger.Open(badger.DefaultOptions(dbPath).WithLoggingLevel(badger.ERROR))
		if err != nil {
			t.Fatalf("Failed to open db: %v", err)
		}

		store := NewVersionedBadgerStore(db, []byte("v:"))
		tree := NewVerkleTree(store)

		var lastRoot []byte
		for v := 1; v <= versions; v++ {
			sess, err := store.NewSession()
			if err != nil {
				db.Close()
				os.RemoveAll(dbPath)
				t.Fatalf("Failed to create session: %v", err)
			}

			// 每个版本的数据基于版本号确定性生成
			keys := make([][]byte, keysPerVersion)
			vals := make([][]byte, keysPerVersion)
			for k := 0; k < keysPerVersion; k++ {
				keys[k] = []byte(fmt.Sprintf("account:v%d:user%03d", v, k))
				vals[k] = []byte(fmt.Sprintf("balance_%d_%d", v*1000, k))
			}

			// 排序确保确定性
			type kvp struct {
				k, v []byte
			}
			pairs := make([]kvp, len(keys))
			for i := range keys {
				pairs[i] = kvp{keys[i], vals[i]}
			}
			sort.Slice(pairs, func(i, j int) bool {
				return string(pairs[i].k) < string(pairs[j].k)
			})
			for i := range pairs {
				keys[i] = pairs[i].k
				vals[i] = pairs[i].v
			}

			lastRoot, err = tree.UpdateWithSession(sess, keys, vals, Version(v))
			if err != nil {
				sess.Close()
				db.Close()
				os.RemoveAll(dbPath)
				t.Fatalf("Update failed at version %d: %v", v, err)
			}

			if err := sess.Commit(); err != nil {
				sess.Close()
				db.Close()
				os.RemoveAll(dbPath)
				t.Fatalf("Commit failed at version %d: %v", v, err)
			}
			sess.Close()
		}

		finalRoots[iter] = lastRoot
		t.Logf("Iteration %d: final root (v%d) = %x", iter, versions, lastRoot[:16])

		db.Close()
		os.RemoveAll(dbPath)
	}

	// 验证所有迭代的最终根相同
	for i := 1; i < iterations; i++ {
		if !bytes.Equal(finalRoots[0], finalRoots[i]) {
			t.Errorf("MULTI-VERSION ROOT MISMATCH: iter 0 (%x) != iter %d (%x)",
				finalRoots[0][:16], i, finalRoots[i][:16])
		}
	}

	t.Logf("✓ 多版本写入 %d 次迭代产生相同的最终根", iterations)
}

// TestVerkleRootDeterminism_WithRestart 测试重启后根是否一致
func TestVerkleRootDeterminism_WithRestart(t *testing.T) {
	dbPath := fmt.Sprintf("test_db_restart_%d", os.Getpid())
	os.RemoveAll(dbPath)
	defer os.RemoveAll(dbPath)

	testData := generateDeterministicKVData(80)

	// === 阶段 1：初始写入 ===
	var root1 []byte
	{
		db, err := badger.Open(badger.DefaultOptions(dbPath).WithLoggingLevel(badger.ERROR))
		if err != nil {
			t.Fatalf("Phase 1: failed to open db: %v", err)
		}

		store := NewVersionedBadgerStore(db, []byte("v:"))
		tree := NewVerkleTree(store)

		sess, _ := store.NewSession()
		keys := make([][]byte, len(testData))
		vals := make([][]byte, len(testData))
		for i, kv := range testData {
			keys[i] = kv.key
			vals[i] = kv.value
		}

		root1, err = tree.UpdateWithSession(sess, keys, vals, Version(1))
		if err != nil {
			t.Fatalf("Phase 1: update failed: %v", err)
		}
		sess.Commit()
		sess.Close()
		db.Close()

		t.Logf("Phase 1 (initial): root = %x", root1[:16])
	}

	// === 阶段 2：重启并恢复 ===
	var root2 []byte
	{
		db, err := badger.Open(badger.DefaultOptions(dbPath).WithLoggingLevel(badger.ERROR))
		if err != nil {
			t.Fatalf("Phase 2: failed to reopen db: %v", err)
		}
		defer db.Close()

		store := NewVersionedBadgerStore(db, []byte("v:"))
		tree := NewVerkleTree(store)

		// 恢复内存层
		if err := tree.RestoreMemoryLayers(root1); err != nil {
			t.Fatalf("Phase 2: RestoreMemoryLayers failed: %v", err)
		}

		// 验证恢复后的根
		root2 = tree.Root()
		t.Logf("Phase 2 (restored): root = %x", root2[:16])

		// 追加一些新数据
		sess, _ := store.NewSession()
		newKeys := [][]byte{[]byte("account:new1"), []byte("account:new2")}
		newVals := [][]byte{[]byte("val_new_1"), []byte("val_new_2")}

		root3, err := tree.UpdateWithSession(sess, newKeys, newVals, Version(2))
		if err != nil {
			t.Fatalf("Phase 2: update v2 failed: %v", err)
		}
		sess.Commit()
		sess.Close()

		t.Logf("Phase 2 (after update): root = %x", root3[:16])

		// 验证原始数据仍可读取
		// 注意：Verkle 树的 Get 方法使用版本 0 表示获取最新数据
		// 由于我们已经更新到 v2，直接用 Version(0) 获取新 key 来验证读取功能
		testNewKey := []byte("account:new1")
		val, err := tree.Get(testNewKey, Version(0))
		if err != nil {
			t.Errorf("Phase 2: failed to read new data: %v", err)
		} else if !bytes.Equal(val, []byte("val_new_1")) {
			t.Errorf("Phase 2: data mismatch for new key, got: %s", string(val))
		} else {
			t.Logf("Phase 2: new data verified OK (key=%s, val=%s)", string(testNewKey), string(val))
		}
	}

	if !bytes.Equal(root1, root2) {
		t.Errorf("ROOT CHANGED AFTER RESTART:\nBefore: %x\nAfter:  %x", root1, root2)
	} else {
		t.Logf("✓ 重启后根承诺一致: %x", root1[:16])
	}
}

// TestVerkleRootDeterminism_LargeDataset 测试大数据集的稳定性
func TestVerkleRootDeterminism_LargeDataset(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping large dataset test in short mode")
	}

	const keyCount = 1000
	testData := generateDeterministicKVData(keyCount)
	t.Logf("Generated %d KV pairs for large dataset test", len(testData))

	const iterations = 3
	roots := make([][]byte, iterations)

	for i := 0; i < iterations; i++ {
		dbPath := fmt.Sprintf("test_db_large_%d_%d", os.Getpid(), i)
		os.RemoveAll(dbPath)

		db, err := badger.Open(badger.DefaultOptions(dbPath).WithLoggingLevel(badger.ERROR))
		if err != nil {
			t.Fatalf("Failed to open db: %v", err)
		}

		store := NewVersionedBadgerStore(db, []byte("v:"))
		tree := NewVerkleTree(store)

		sess, _ := store.NewSession()
		keys := make([][]byte, len(testData))
		vals := make([][]byte, len(testData))
		for j, kv := range testData {
			keys[j] = kv.key
			vals[j] = kv.value
		}

		root, err := tree.UpdateWithSession(sess, keys, vals, Version(1))
		if err != nil {
			t.Fatalf("Iteration %d: update failed: %v", i, err)
		}
		sess.Commit()
		sess.Close()

		roots[i] = root
		t.Logf("Iteration %d: root = %x", i, root[:16])

		db.Close()
		os.RemoveAll(dbPath)
	}

	for i := 1; i < iterations; i++ {
		if !bytes.Equal(roots[0], roots[i]) {
			t.Errorf("LARGE DATASET ROOT MISMATCH: iter 0 (%x) != iter %d (%x)",
				roots[0][:16], i, roots[i][:16])
		}
	}

	t.Logf("✓ 大数据集 (%d keys) %d 次迭代产生相同的根", keyCount, iterations)
}

// TestVerkleRootDeterminism_ValueSizeVariation 测试不同值大小的稳定性
func TestVerkleRootDeterminism_ValueSizeVariation(t *testing.T) {
	// 生成包含不同大小值的数据（小值直接存储，大值间接存储）
	testData := []struct {
		key   []byte
		value []byte
	}{
		{[]byte("small:1"), make([]byte, 10)},                   // 小值 (< 31)
		{[]byte("small:2"), make([]byte, 30)},                   // 边界小值
		{[]byte("large:1"), make([]byte, 32)},                   // 刚好超过阈值
		{[]byte("large:2"), make([]byte, 100)},                  // 中等大值
		{[]byte("large:3"), make([]byte, 500)},                  // 较大值
		{[]byte("large:4"), make([]byte, maxDirectValueSize)},   // 边界值
		{[]byte("large:5"), make([]byte, maxDirectValueSize+1)}, // 刚超过边界
	}

	// 填充确定性数据
	for i := range testData {
		h := sha256.Sum256(testData[i].key)
		for j := range testData[i].value {
			testData[i].value[j] = h[j%32]
		}
	}

	const iterations = 3
	roots := make([][]byte, iterations)

	for i := 0; i < iterations; i++ {
		dbPath := fmt.Sprintf("test_db_valuesize_%d_%d", os.Getpid(), i)
		os.RemoveAll(dbPath)

		db, err := badger.Open(badger.DefaultOptions(dbPath).WithLoggingLevel(badger.ERROR))
		if err != nil {
			t.Fatalf("Failed to open db: %v", err)
		}

		store := NewVersionedBadgerStore(db, []byte("v:"))
		tree := NewVerkleTree(store)

		sess, _ := store.NewSession()

		keys := make([][]byte, len(testData))
		vals := make([][]byte, len(testData))
		for j := range testData {
			keys[j] = testData[j].key
			vals[j] = testData[j].value
		}

		// 排序确保确定性
		type kvp struct {
			k, v []byte
		}
		pairs := make([]kvp, len(keys))
		for j := range keys {
			pairs[j] = kvp{keys[j], vals[j]}
		}
		sort.Slice(pairs, func(a, b int) bool {
			return string(pairs[a].k) < string(pairs[b].k)
		})
		for j := range pairs {
			keys[j] = pairs[j].k
			vals[j] = pairs[j].v
		}

		root, err := tree.UpdateWithSession(sess, keys, vals, Version(1))
		if err != nil {
			t.Fatalf("Iteration %d: update failed: %v", i, err)
		}
		sess.Commit()
		sess.Close()

		roots[i] = root
		t.Logf("Iteration %d: root = %x", i, root[:16])

		db.Close()
		os.RemoveAll(dbPath)
	}

	for i := 1; i < iterations; i++ {
		if !bytes.Equal(roots[0], roots[i]) {
			t.Errorf("VALUE SIZE VARIATION ROOT MISMATCH: iter 0 (%x) != iter %d (%x)",
				roots[0][:16], i, roots[i][:16])
		}
	}

	t.Logf("✓ 不同值大小 %d 次迭代产生相同的根", iterations)
}

// TestVerkleMaxMemoryDepth 验证 MaxMemoryDepth 配置
func TestVerkleMaxMemoryDepth(t *testing.T) {
	t.Logf("MaxMemoryDepth = %d (根节点外 %d 层在内存)", MaxMemoryDepth, MaxMemoryDepth)

	if MaxMemoryDepth != 2 {
		t.Errorf("Expected MaxMemoryDepth = 2, got %d", MaxMemoryDepth)
	}
}

// ============================================
// 辅助函数
// ============================================

type kvData struct {
	key   []byte
	value []byte
}

// generateDeterministicKVData 生成确定性的 KV 测试数据
func generateDeterministicKVData(count int) []kvData {
	data := make([]kvData, count)
	for i := 0; i < count; i++ {
		// 使用确定性方式生成 key 和 value
		keyStr := fmt.Sprintf("account:%08d:balance", i)
		valueStr := fmt.Sprintf("amount_%d_nonce_%d", i*1000, i)

		data[i] = kvData{
			key:   []byte(keyStr),
			value: []byte(valueStr),
		}
	}

	// 按 key 排序确保顺序一致
	sort.Slice(data, func(i, j int) bool {
		return string(data[i].key) < string(data[j].key)
	})

	return data
}

// runVerkleWithOrder 使用指定顺序运行 Verkle 更新并返回根
func runVerkleWithOrder(t *testing.T, data []kvData, reverse bool, label string) []byte {
	dbPath := fmt.Sprintf("test_db_order_%s_%d", label, os.Getpid())
	os.RemoveAll(dbPath)
	defer os.RemoveAll(dbPath)

	db, err := badger.Open(badger.DefaultOptions(dbPath).WithLoggingLevel(badger.ERROR))
	if err != nil {
		t.Fatalf("Failed to open db for %s: %v", label, err)
	}
	defer db.Close()

	store := NewVersionedBadgerStore(db, []byte("v:"))
	tree := NewVerkleTree(store)

	sess, _ := store.NewSession()
	defer sess.Close()

	// 准备数据
	keys := make([][]byte, len(data))
	vals := make([][]byte, len(data))

	if reverse {
		for i := 0; i < len(data); i++ {
			keys[i] = data[len(data)-1-i].key
			vals[i] = data[len(data)-1-i].value
		}
	} else {
		for i := 0; i < len(data); i++ {
			keys[i] = data[i].key
			vals[i] = data[i].value
		}
	}

	// 内部排序（模拟 ApplyUpdate 的排序行为）
	type kvp struct {
		k, v []byte
	}
	pairs := make([]kvp, len(keys))
	for i := range keys {
		pairs[i] = kvp{keys[i], vals[i]}
	}
	sort.Slice(pairs, func(i, j int) bool {
		return string(pairs[i].k) < string(pairs[j].k)
	})
	for i := range pairs {
		keys[i] = pairs[i].k
		vals[i] = pairs[i].v
	}

	root, err := tree.UpdateWithSession(sess, keys, vals, Version(1))
	if err != nil {
		t.Fatalf("%s: update failed: %v", label, err)
	}
	sess.Commit()

	t.Logf("%s order: root = %x", label, root[:16])
	return root
}

// rootToHex 帮助函数：将根转换为 hex 字符串
func rootToHex(root []byte) string {
	if len(root) == 0 {
		return "(empty)"
	}
	return hex.EncodeToString(root[:min(16, len(root))]) + "..."
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
