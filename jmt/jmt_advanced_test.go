package smt

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"
)

// ============================================
// 边界情况测试
// ============================================

func TestJMT_EmptyKey(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	// 空 Key
	key := []byte{}
	value := []byte("empty key value")

	root, err := jmt.Update([][]byte{key}, [][]byte{value}, 1)
	if err != nil {
		t.Fatalf("Update with empty key failed: %v", err)
	}

	got, err := jmt.Get(key, 0)
	if err != nil {
		t.Fatalf("Get with empty key failed: %v", err)
	}
	if !bytes.Equal(got, value) {
		t.Errorf("Get() = %v, want %v", got, value)
	}

	// 验证 Proof
	proof, err := jmt.Prove(key)
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}
	if !VerifyJMTProof(proof, root, sha256.New()) {
		t.Errorf("Proof verification failed for empty key")
	}
}

func TestJMT_LargeValue(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	key := []byte("large-value-key")
	// 1MB 的值
	value := make([]byte, 1024*1024)
	for i := range value {
		value[i] = byte(i % 256)
	}

	_, err := jmt.Update([][]byte{key}, [][]byte{value}, 1)
	if err != nil {
		t.Fatalf("Update with large value failed: %v", err)
	}

	got, err := jmt.Get(key, 0)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !bytes.Equal(got, value) {
		t.Errorf("Large value mismatch")
	}
}

func TestJMT_SimilarKeys(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	// 具有相同前缀的 Key
	keys := [][]byte{
		[]byte("prefix-aaa"),
		[]byte("prefix-aab"),
		[]byte("prefix-aac"),
		[]byte("prefix-baa"),
	}
	values := [][]byte{
		[]byte("value1"),
		[]byte("value2"),
		[]byte("value3"),
		[]byte("value4"),
	}

	_, err := jmt.Update(keys, values, 1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	for i, key := range keys {
		got, err := jmt.Get(key, 0)
		if err != nil {
			t.Fatalf("Get(%s) failed: %v", key, err)
		}
		if !bytes.Equal(got, values[i]) {
			t.Errorf("Get(%s) = %v, want %v", key, got, values[i])
		}
	}
}

func TestJMT_DeleteNonExistent(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	key1 := []byte("exists")
	key2 := []byte("not-exists")

	_, err := jmt.Update([][]byte{key1}, [][]byte{[]byte("value")}, 1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// 删除不存在的 Key 应该不报错
	_, err = jmt.Delete(key2, 2)
	if err != nil {
		t.Fatalf("Delete non-existent key failed: %v", err)
	}

	// 原有的 Key 应该还在
	got, err := jmt.Get(key1, 0)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !bytes.Equal(got, []byte("value")) {
		t.Errorf("Original key was affected by deleting non-existent key")
	}
}

func TestJMT_UpdateSameKeyMultipleTimes(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	key := []byte("key")

	// 多次更新同一个 Key
	for i := 1; i <= 10; i++ {
		value := []byte(fmt.Sprintf("value-%d", i))
		_, err := jmt.Update([][]byte{key}, [][]byte{value}, Version(i))
		if err != nil {
			t.Fatalf("Update %d failed: %v", i, err)
		}
	}

	// 验证最终值
	got, err := jmt.Get(key, 0)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !bytes.Equal(got, []byte("value-10")) {
		t.Errorf("Get() = %s, want value-10", got)
	}
}

func TestJMT_DeleteAllKeys(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	keys := [][]byte{
		[]byte("key1"),
		[]byte("key2"),
		[]byte("key3"),
	}
	values := [][]byte{
		[]byte("value1"),
		[]byte("value2"),
		[]byte("value3"),
	}

	_, err := jmt.Update(keys, values, 1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// 删除所有 Key
	for i, key := range keys {
		_, err := jmt.Delete(key, Version(2+i))
		if err != nil {
			t.Fatalf("Delete %s failed: %v", key, err)
		}
	}

	// 树应该为空 (根为 placeholder)
	if !jmt.hasher.IsPlaceholder(jmt.Root()) {
		t.Errorf("Root should be placeholder after deleting all keys")
	}
}

// ============================================
// 版本化测试
// ============================================

func TestJMT_VersionedQuery(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	key := []byte("key")

	// 版本 1: value-1
	_, err := jmt.Update([][]byte{key}, [][]byte{[]byte("value-1")}, 1)
	if err != nil {
		t.Fatalf("Update v1 failed: %v", err)
	}

	// 版本 2: value-2
	_, err = jmt.Update([][]byte{key}, [][]byte{[]byte("value-2")}, 2)
	if err != nil {
		t.Fatalf("Update v2 failed: %v", err)
	}

	// 版本 3: value-3
	_, err = jmt.Update([][]byte{key}, [][]byte{[]byte("value-3")}, 3)
	if err != nil {
		t.Fatalf("Update v3 failed: %v", err)
	}

	// 查询版本 1
	got, err := jmt.Get(key, 1)
	if err != nil {
		t.Fatalf("Get v1 failed: %v", err)
	}
	if !bytes.Equal(got, []byte("value-1")) {
		t.Errorf("Get(key, 1) = %s, want value-1", got)
	}

	// 查询版本 2
	got, err = jmt.Get(key, 2)
	if err != nil {
		t.Fatalf("Get v2 failed: %v", err)
	}
	if !bytes.Equal(got, []byte("value-2")) {
		t.Errorf("Get(key, 2) = %s, want value-2", got)
	}

	// 查询最新版本
	got, err = jmt.Get(key, 0)
	if err != nil {
		t.Fatalf("Get latest failed: %v", err)
	}
	if !bytes.Equal(got, []byte("value-3")) {
		t.Errorf("Get(key, 0) = %s, want value-3", got)
	}
}

func TestJMT_VersionedDelete(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	key := []byte("key")

	// 版本 1: 插入
	_, err := jmt.Update([][]byte{key}, [][]byte{[]byte("value")}, 1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// 版本 2: 删除
	_, err = jmt.Delete(key, 2)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// 版本 1 应该还能查到
	got, err := jmt.Get(key, 1)
	if err != nil {
		t.Fatalf("Get v1 failed: %v", err)
	}
	if !bytes.Equal(got, []byte("value")) {
		t.Errorf("Get(key, 1) = %s, want value", got)
	}

	// 版本 2 及之后应该查不到
	_, err = jmt.Get(key, 2)
	if err != ErrNotFound {
		t.Errorf("Get(key, 2) should return ErrNotFound, got %v", err)
	}
}

func TestJMT_MultipleKeysVersioned(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	// 版本 1: 插入 key1, key2
	_, err := jmt.Update(
		[][]byte{[]byte("key1"), []byte("key2")},
		[][]byte{[]byte("v1-1"), []byte("v1-2")},
		1,
	)
	if err != nil {
		t.Fatalf("Update v1 failed: %v", err)
	}

	// 版本 2: 更新 key1, 插入 key3
	_, err = jmt.Update(
		[][]byte{[]byte("key1"), []byte("key3")},
		[][]byte{[]byte("v2-1"), []byte("v2-3")},
		2,
	)
	if err != nil {
		t.Fatalf("Update v2 failed: %v", err)
	}

	// 验证版本 1
	got, _ := jmt.Get([]byte("key1"), 1)
	if !bytes.Equal(got, []byte("v1-1")) {
		t.Errorf("key1@v1 = %s, want v1-1", got)
	}

	// 验证版本 2
	got, _ = jmt.Get([]byte("key1"), 2)
	if !bytes.Equal(got, []byte("v2-1")) {
		t.Errorf("key1@v2 = %s, want v2-1", got)
	}

	// key3 在版本 1 不存在
	_, err = jmt.Get([]byte("key3"), 1)
	if err != ErrNotFound && err != ErrVersionNotFound {
		t.Errorf("key3@v1 should not exist, got %v", err)
	}

	// key3 在版本 2 存在
	got, err = jmt.Get([]byte("key3"), 2)
	if err != nil {
		t.Fatalf("Get key3@v2 failed: %v", err)
	}
	if !bytes.Equal(got, []byte("v2-3")) {
		t.Errorf("key3@v2 = %s, want v2-3", got)
	}
}

func TestSimpleVersionedMap_PruneEffect(t *testing.T) {
	store := NewSimpleVersionedMap()

	key := []byte("key")
	store.Set(key, []byte("v1"), 1)
	store.Set(key, []byte("v2"), 2)
	store.Set(key, []byte("v3"), 3)

	// Prune 版本 2 之前
	store.Prune(2)

	// 版本 1 不可访问
	_, err := store.Get(key, 1)
	if err != ErrVersionNotFound {
		t.Errorf("v1 should be pruned, got %v", err)
	}

	// 版本 2、3 仍可访问
	got, err := store.Get(key, 2)
	if err != nil || !bytes.Equal(got, []byte("v2")) {
		t.Errorf("v2 should still exist")
	}

	got, err = store.Get(key, 3)
	if err != nil || !bytes.Equal(got, []byte("v3")) {
		t.Errorf("v3 should still exist")
	}
}
func TestJMT_ConcurrentHighCollisionStress(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	count := 10000
	concurrency := 8
	keys := make([][]byte, count)
	values := make([][]byte, count)

	prefix := "v1_acc_orders_very_long_prefix_to_increase_depth_"
	for i := 0; i < count; i++ {
		keys[i] = []byte(fmt.Sprintf("%s%08d", prefix, i))
		values[i] = []byte(fmt.Sprintf("v_%d", i))
	}

	// 并发插入，但共享同一个逻辑版本，模拟同一个区块内的并行处理
	// 注意：JMT.Update 是带锁的，这里主要测试大量 Key 冲突时的路径重建正确性
	var wg sync.WaitGroup
	batchSize := count / concurrency
	version := Version(1)

	for g := 0; g < concurrency; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			start := gid * batchSize
			end := start + batchSize
			// 在同一个版本下进行增量更新
			_, err := jmt.Update(keys[start:end], values[start:end], version)
			if err != nil {
				t.Errorf("Goroutine %d failed: %v", gid, err)
			}
		}(g)
	}
	wg.Wait()

	// 再次验证：JMT 现在的 Update 逻辑应该保证最后一次写入的版本是 version
	if jmt.Version() != version {
		t.Errorf("Version mismatch: got %d, want %d", jmt.Version(), version)
	}

	// 验证数据完整性
	for i := 0; i < count; i++ {
		got, err := jmt.Get(keys[i], 0)
		if err != nil {
			t.Errorf("Key %d missing: %v", i, err)
			continue
		}
		if string(got) != fmt.Sprintf("v_%d", i) {
			t.Errorf("Value mismatch for key %d: got %s", i, string(got))
		}
	}
}
