package verkle

import (
	"fmt"
	"os"
	"testing"

	"github.com/dgraph-io/badger/v4"
)

func TestVerkleFlushCorruption(t *testing.T) {
	// 1. 设置临时数据库 (使用更唯一的路径)
	dbPath := fmt.Sprintf("test_db_flush_%d", os.Getpid())
	os.RemoveAll(dbPath)
	defer os.RemoveAll(dbPath)

	db, err := badger.Open(badger.DefaultOptions(dbPath).WithLoggingLevel(badger.ERROR))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := NewVersionedBadgerStore(db, []byte("v:"))
	tree := NewVerkleTree(store)

	// 2. 插入一些测试数据
	key1 := []byte("user:1:balance")
	val1 := []byte("1000")
	version1 := Version(1)

	sess1, _ := store.NewSession()
	_, err = tree.UpdateWithSession(sess1, [][]byte{key1}, [][]byte{val1}, version1)
	if err != nil {
		t.Fatal(err)
	}
	sess1.Commit()

	// 此时树已经经过了一轮 flushNodes。
	// 为了验证猜想，我们需要模拟“从数据库加载 -> 再次 Flush”的过程。

	rootHash := tree.Root()
	t.Logf("Initial root hash: %x", rootHash)

	// 3. 模拟重启：创建一个新的树对象并加载根节点
	newTree := NewVerkleTree(store)
	err = newTree.RestoreMemoryLayers(rootHash)
	if err != nil {
		t.Fatalf("Failed to restore memory layers: %v", err)
	}

	// 4. 在新版本中再次执行 Update（即使不改数据，只执行 Flush）
	version2 := Version(2)
	sess2, _ := store.NewSession()
	_, err = newTree.UpdateWithSession(sess2, [][]byte{key1}, [][]byte{val1}, version2)
	if err != nil {
		t.Fatal(err)
	}
	sess2.Commit()

	// 5. 验证：尝试解析之前的节点
	// 首先验证 v1 版本的数据是否还在且正确
	rawV1, err := store.Get(nodeKey(rootHash), version1)
	if err != nil {
		t.Errorf("v1 data missing: %v", err)
	} else if len(rawV1) <= 32 {
		t.Errorf("v1 data corrupted! len=%d", len(rawV1))
	}

	// 如果猜想正确，此时 resolveNode (读取最新版本) 可能会报错或返回不完整的 payload
	_, err = newTree.resolveNode(rootHash)
	if err != nil {
		t.Errorf("Hypothesis confirmed! resolveNode failed after second flush: %v", err)
		// 手动检查数据库里的内容长度
		rawV2, _ := store.Get(nodeKey(rootHash), version2)
		t.Logf("Database payload for root at v2 (len=%d): %x", len(rawV2), rawV2)
	} else {
		t.Log("resolveNode still works, checking if meta data is intact...")
	}
}

func TestVerklePruneMetadata(t *testing.T) {
	// 1. 设置临时数据库
	dbPath := "test_db_prune"
	os.RemoveAll(dbPath)
	defer os.RemoveAll(dbPath)

	db, err := badger.Open(badger.DefaultOptions(dbPath).WithLoggingLevel(badger.ERROR))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := NewVersionedBadgerStore(db, []byte("v:"))

	key := []byte("test_key")
	val := []byte("test_val")

	// 2. 写入多个版本
	for i := 1; i <= 10; i++ {
		err = store.Set(key, val, Version(i))
		if err != nil {
			t.Fatal(err)
		}
	}

	// 验证 Latest 索引是否存在
	ver, err := store.GetLatestVersion(key)
	if err != nil || ver != 10 {
		t.Fatalf("Latest version should be 10, got %v, err: %v", ver, err)
	}

	// 3. 执行 Prune 删掉版本 5 之前的
	err = store.Prune(Version(5))
	if err != nil {
		t.Fatal(err)
	}

	// 4. 再次验证 Latest 索引
	ver, err = store.GetLatestVersion(key)
	if err != nil {
		t.Errorf("Hypothesis confirmed! Latest metadata might have been deleted: %v", err)
	} else if ver != 10 {
		t.Errorf("Latest version changed unexpectedly after prune: %v", ver)
	} else {
		t.Log("Latest metadata is still intact.")
	}
}

func TestVerklePruneSafety(t *testing.T) {
	dbPath := fmt.Sprintf("test_db_prune_safety_%d", os.Getpid())
	os.RemoveAll(dbPath)
	defer os.RemoveAll(dbPath)

	db, err := badger.Open(badger.DefaultOptions(dbPath).WithLoggingLevel(badger.ERROR))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store := NewVersionedBadgerStore(db, []byte("v:"))

	// 1. 模拟一个长期不改动的状态 A 和一个频繁改动的状态 B
	keyA := []byte("persistent_state")
	valA := []byte("stay_forever")

	keyB := []byte("frequent_state")
	valB1 := []byte("old_value")
	valB2 := []byte("new_value")

	_ = store.Set(keyA, valA, Version(1))  // A 只有版本 1
	_ = store.Set(keyB, valB1, Version(2)) // B 有版本 2 和 版本 20
	_ = store.Set(keyB, valB2, Version(20))

	// 2. 执行激进的剪枝：保留版本 10 之后的
	// 如果逻辑正确：
	// - A (v1) 应该被保留，因为它是 A 的最新版本
	// - B (v2) 应该被删除，因为 B 有更高版本 v20
	// - B (v20) 应该被保留
	err = store.Prune(Version(10))
	if err != nil {
		t.Fatal(err)
	}

	// 3. 验证 A (v1)
	resA, err := store.Get(keyA, Version(1))
	if err != nil {
		t.Errorf("FAIL: KeyA (v1) was incorrectly pruned despite being the latest version: %v", err)
	} else if string(resA) != string(valA) {
		t.Errorf("FAIL: KeyA res mismatch: got %s", string(resA))
	} else {
		t.Log("PASS: KeyA (v1) survived aggressive pruning.")
	}

	// 4. 验证 B (v2)
	_, err = store.Get(keyB, Version(2))
	if err == nil {
		t.Errorf("FAIL: KeyB (v2) should have been pruned!")
	} else {
		t.Log("PASS: KeyB (v2) was correctly pruned.")
	}

	// 5. 验证 B (v20)
	resB2, err := store.Get(keyB, Version(20))
	if err != nil {
		t.Errorf("FAIL: KeyB (v20) missing: %v", err)
	} else if string(resB2) != string(valB2) {
		t.Errorf("FAIL: KeyB res mismatch")
	} else {
		t.Log("PASS: KeyB (v20) survived.")
	}
}
