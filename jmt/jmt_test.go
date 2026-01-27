package smt

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"testing"
)

// ============================================
// JMT 核心功能测试
// ============================================

func TestJMT_EmptyTree(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	// 空树的根应该是 placeholder
	if !jmt.hasher.IsPlaceholder(jmt.Root()) {
		t.Errorf("Empty tree root should be placeholder")
	}

	// 查询不存在的 Key
	_, err := jmt.Get([]byte("nonexistent"), 0)
	if err != ErrNotFound {
		t.Errorf("Get on empty tree should return ErrNotFound, got %v", err)
	}
}

func TestJMT_SingleInsert(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	key := []byte("hello")
	value := []byte("world")

	// 插入
	root, err := jmt.Update([][]byte{key}, [][]byte{value}, 1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// 根不应该是 placeholder
	if jmt.hasher.IsPlaceholder(root) {
		t.Errorf("Root should not be placeholder after insert")
	}

	// 查询
	got, err := jmt.Get(key, 0)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !bytes.Equal(got, value) {
		t.Errorf("Get() = %v, want %v", got, value)
	}
}

func TestJMT_MultipleInserts(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	// 插入多个 Key
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

	// 验证所有 Key
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

func TestJMT_UpdateExisting(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	key := []byte("key")
	value1 := []byte("value1")
	value2 := []byte("value2")

	// 第一次插入
	_, err := jmt.Update([][]byte{key}, [][]byte{value1}, 1)
	if err != nil {
		t.Fatalf("First update failed: %v", err)
	}

	// 更新
	_, err = jmt.Update([][]byte{key}, [][]byte{value2}, 2)
	if err != nil {
		t.Fatalf("Second update failed: %v", err)
	}

	// 验证更新后的值
	got, err := jmt.Get(key, 0)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !bytes.Equal(got, value2) {
		t.Errorf("Get() = %v, want %v", got, value2)
	}
}

func TestJMT_Delete(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	key := []byte("key")
	value := []byte("value")

	// 插入
	_, err := jmt.Update([][]byte{key}, [][]byte{value}, 1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// 删除
	_, err = jmt.Delete(key, 2)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// 验证删除后查询返回 ErrNotFound
	_, err = jmt.Get(key, 0)
	if err != ErrNotFound {
		t.Errorf("Get after delete should return ErrNotFound, got %v", err)
	}
}

func TestJMT_ManyKeys(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	// 插入 100 个 Key
	count := 100
	keys := make([][]byte, count)
	values := make([][]byte, count)
	for i := 0; i < count; i++ {
		keys[i] = []byte(fmt.Sprintf("key-%04d", i))
		values[i] = []byte(fmt.Sprintf("value-%04d", i))
	}

	_, err := jmt.Update(keys, values, 1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// 验证所有 Key
	for i := 0; i < count; i++ {
		got, err := jmt.Get(keys[i], 0)
		if err != nil {
			t.Fatalf("Get(key-%04d) failed: %v", i, err)
		}
		if !bytes.Equal(got, values[i]) {
			t.Errorf("Get(key-%04d) = %v, want %v", i, got, values[i])
		}
	}
}

func TestJMT_RootChangesOnUpdate(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	key := []byte("key")

	// 第一次插入
	root1, err := jmt.Update([][]byte{key}, [][]byte{[]byte("value1")}, 1)
	if err != nil {
		t.Fatalf("First update failed: %v", err)
	}

	// 第二次更新
	root2, err := jmt.Update([][]byte{key}, [][]byte{[]byte("value2")}, 2)
	if err != nil {
		t.Fatalf("Second update failed: %v", err)
	}

	// 根哈希应该不同
	if bytes.Equal(root1, root2) {
		t.Errorf("Root should change after update")
	}
}

func TestJMT_DeleteReducesTree(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	key1 := []byte("key1")
	key2 := []byte("key2")

	// 插入两个 Key
	_, err := jmt.Update([][]byte{key1, key2}, [][]byte{[]byte("v1"), []byte("v2")}, 1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// 删除一个
	_, err = jmt.Delete(key1, 2)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// 另一个应该还在
	got, err := jmt.Get(key2, 0)
	if err != nil {
		t.Fatalf("Get(key2) after delete failed: %v", err)
	}
	if !bytes.Equal(got, []byte("v2")) {
		t.Errorf("Get(key2) = %v, want v2", got)
	}
}

// ============================================
// JMTHasher 测试
// ============================================

func TestJMTHasher_DigestLeafNode(t *testing.T) {
	jh := NewJMTHasher(sha256.New())

	keyHash := make([]byte, 32)
	keyHash[0] = 0x01
	valueHash := make([]byte, 32)
	valueHash[0] = 0x02

	hash1, data1 := jh.DigestLeafNode(keyHash, valueHash)
	hash2, data2 := jh.DigestLeafNode(keyHash, valueHash)

	// 相同输入应产生相同输出
	if !bytes.Equal(hash1, hash2) {
		t.Errorf("Same input should produce same hash")
	}
	if !bytes.Equal(data1, data2) {
		t.Errorf("Same input should produce same data")
	}

	// 哈希应该是 32 字节
	if len(hash1) != 32 {
		t.Errorf("Hash should be 32 bytes, got %d", len(hash1))
	}
}

func TestJMTHasher_DigestInternalNode(t *testing.T) {
	jh := NewJMTHasher(sha256.New())

	var children [16][]byte
	for i := 0; i < 16; i++ {
		children[i] = jh.Placeholder()
	}

	// 设置两个非空子节点
	children[3] = make([]byte, 32)
	children[3][0] = 0x03
	children[10] = make([]byte, 32)
	children[10][0] = 0x0A

	hash, _ := jh.DigestInternalNode(children)

	// 哈希应该是 32 字节
	if len(hash) != 32 {
		t.Errorf("Hash should be 32 bytes, got %d", len(hash))
	}
}

func TestJMTHasher_ExtractSiblings(t *testing.T) {
	jh := NewJMTHasher(sha256.New())

	node := NewInternalNode()
	hash1 := make([]byte, 32)
	hash1[0] = 0x01
	hash2 := make([]byte, 32)
	hash2[0] = 0x02
	hash3 := make([]byte, 32)
	hash3[0] = 0x03

	node.SetChild(3, hash1)
	node.SetChild(7, hash2)
	node.SetChild(10, hash3)

	// 提取 nibble 7 的兄弟节点
	siblings := jh.ExtractSiblings(node, 7)

	// 应该有 2 个兄弟节点 (nibble 3 和 10)
	if len(siblings.Siblings) != 2 {
		t.Errorf("Expected 2 siblings, got %d", len(siblings.Siblings))
	}

	// Bitmap 应该标记 3 和 10
	expectedBitmap := uint16((1 << 3) | (1 << 10))
	if siblings.Bitmap != expectedBitmap {
		t.Errorf("Bitmap = %016b, want %016b", siblings.Bitmap, expectedBitmap)
	}
}
