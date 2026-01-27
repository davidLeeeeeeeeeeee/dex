package smt

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

// ============================================
// Nibble 操作函数测试
// ============================================

func TestGetNibbleAt(t *testing.T) {
	// 0xAB = 1010_1011 -> nibble[0]=0xA, nibble[1]=0xB
	// 0xCD = 1100_1101 -> nibble[2]=0xC, nibble[3]=0xD
	path := []byte{0xAB, 0xCD}

	tests := []struct {
		position int
		expected byte
	}{
		{0, 0xA},
		{1, 0xB},
		{2, 0xC},
		{3, 0xD},
	}

	for _, tt := range tests {
		got := getNibbleAt(path, tt.position)
		if got != tt.expected {
			t.Errorf("getNibbleAt(path, %d) = 0x%X, want 0x%X", tt.position, got, tt.expected)
		}
	}
}

func TestCountCommonNibblePrefix(t *testing.T) {
	tests := []struct {
		path1    []byte
		path2    []byte
		expected int
	}{
		{[]byte{0xAB, 0xCD}, []byte{0xAB, 0xCD}, 4}, // 完全相同
		{[]byte{0xAB, 0xCD}, []byte{0xAB, 0xCE}, 3}, // 前3个nibble相同
		{[]byte{0xAB, 0xCD}, []byte{0xAC, 0xCD}, 1}, // 只有第一个nibble相同
		{[]byte{0xAB, 0xCD}, []byte{0xBB, 0xCD}, 0}, // 没有公共前缀
		{[]byte{0x12, 0x34}, []byte{0x12, 0x35}, 3}, // 前3个nibble相同
	}

	for i, tt := range tests {
		got := countCommonNibblePrefix(tt.path1, tt.path2)
		if got != tt.expected {
			t.Errorf("Test %d: countCommonNibblePrefix() = %d, want %d", i, got, tt.expected)
		}
	}
}

func TestNibbleSlice(t *testing.T) {
	path := []byte{0xAB, 0xCD, 0xEF}
	// nibbles: A, B, C, D, E, F

	got := nibbleSlice(path, 1, 4)
	expected := []byte{0xB, 0xC, 0xD}
	if !bytes.Equal(got, expected) {
		t.Errorf("nibbleSlice(path, 1, 4) = %v, want %v", got, expected)
	}

	// 空切片
	got = nibbleSlice(path, 2, 2)
	if got != nil {
		t.Errorf("nibbleSlice(path, 2, 2) should be nil, got %v", got)
	}
}

func TestNibblesToBytes(t *testing.T) {
	tests := []struct {
		nibbles  []byte
		expected []byte
	}{
		{[]byte{0xA, 0xB}, []byte{0xAB}},
		{[]byte{0xA, 0xB, 0xC, 0xD}, []byte{0xAB, 0xCD}},
		{[]byte{0xA, 0xB, 0xC}, []byte{0xAB, 0xC0}}, // 奇数个nibble
		{[]byte{0x1}, []byte{0x10}},                 // 单个nibble
	}

	for i, tt := range tests {
		got := nibblesToBytes(tt.nibbles)
		if !bytes.Equal(got, tt.expected) {
			t.Errorf("Test %d: nibblesToBytes(%v) = %v, want %v", i, tt.nibbles, got, tt.expected)
		}
	}
}

// ============================================
// 版本化存储测试
// ============================================

func TestSimpleVersionedMap_BasicOperations(t *testing.T) {
	store := NewSimpleVersionedMap()
	defer store.Close()

	key := []byte("test-key")
	value1 := []byte("value-v1")
	value2 := []byte("value-v2")

	// 写入版本1
	if err := store.Set(key, value1, 1); err != nil {
		t.Fatalf("Set v1 failed: %v", err)
	}

	// 写入版本2
	if err := store.Set(key, value2, 2); err != nil {
		t.Fatalf("Set v2 failed: %v", err)
	}

	// 读取版本1
	got, err := store.Get(key, 1)
	if err != nil {
		t.Fatalf("Get v1 failed: %v", err)
	}
	if !bytes.Equal(got, value1) {
		t.Errorf("Get v1 = %v, want %v", got, value1)
	}

	// 读取版本2
	got, err = store.Get(key, 2)
	if err != nil {
		t.Fatalf("Get v2 failed: %v", err)
	}
	if !bytes.Equal(got, value2) {
		t.Errorf("Get v2 = %v, want %v", got, value2)
	}

	// 读取最新版本 (version=0)
	got, err = store.Get(key, 0)
	if err != nil {
		t.Fatalf("Get latest failed: %v", err)
	}
	if !bytes.Equal(got, value2) {
		t.Errorf("Get latest = %v, want %v", got, value2)
	}

	// 读取版本1.5 (应该返回版本1的值)
	// 注意：版本1.5不是有效的整数，这里用版本1测试
}

func TestSimpleVersionedMap_Delete(t *testing.T) {
	store := NewSimpleVersionedMap()
	defer store.Close()

	key := []byte("test-key")
	value := []byte("value")

	// 写入
	store.Set(key, value, 1)

	// 删除
	store.Delete(key, 2)

	// 版本1应该还能读取
	got, err := store.Get(key, 1)
	if err != nil {
		t.Fatalf("Get v1 after delete failed: %v", err)
	}
	if !bytes.Equal(got, value) {
		t.Errorf("Get v1 = %v, want %v", got, value)
	}

	// 版本2及之后应该返回 ErrNotFound
	_, err = store.Get(key, 2)
	if err != ErrNotFound {
		t.Errorf("Get v2 should return ErrNotFound, got %v", err)
	}

	// 最新版本应该返回 ErrNotFound
	_, err = store.Get(key, 0)
	if err != ErrNotFound {
		t.Errorf("Get latest should return ErrNotFound, got %v", err)
	}
}

func TestSimpleVersionedMap_Prune(t *testing.T) {
	store := NewSimpleVersionedMap()
	defer store.Close()

	key := []byte("test-key")

	store.Set(key, []byte("v1"), 1)
	store.Set(key, []byte("v2"), 2)
	store.Set(key, []byte("v3"), 3)

	// Prune 版本2之前的数据
	store.Prune(2)

	// 版本1应该不存在
	_, err := store.Get(key, 1)
	if err != ErrVersionNotFound {
		t.Errorf("Get v1 after prune should return ErrVersionNotFound, got %v", err)
	}

	// 版本2和3应该还在
	got, err := store.Get(key, 2)
	if err != nil || !bytes.Equal(got, []byte("v2")) {
		t.Errorf("Get v2 after prune failed")
	}
}

// ============================================
// 节点类型测试
// ============================================

func TestInternalNode_SetGetChild(t *testing.T) {
	node := NewInternalNode()

	hash1 := make([]byte, 32)
	hash1[0] = 0x01
	hash2 := make([]byte, 32)
	hash2[0] = 0x02

	// 设置 nibble 5 的子节点
	node.SetChild(5, hash1)
	if node.ChildCount() != 1 {
		t.Errorf("ChildCount should be 1, got %d", node.ChildCount())
	}

	// 获取 nibble 5
	got := node.GetChild(5)
	if !bytes.Equal(got, hash1) {
		t.Errorf("GetChild(5) = %v, want %v", got, hash1)
	}

	// 获取不存在的 nibble
	got = node.GetChild(3)
	if got != nil {
		t.Errorf("GetChild(3) should be nil, got %v", got)
	}

	// 设置另一个子节点
	node.SetChild(3, hash2)
	if node.ChildCount() != 2 {
		t.Errorf("ChildCount should be 2, got %d", node.ChildCount())
	}

	// 验证两个子节点都存在
	got = node.GetChild(3)
	if !bytes.Equal(got, hash2) {
		t.Errorf("GetChild(3) = %v, want %v", got, hash2)
	}
	got = node.GetChild(5)
	if !bytes.Equal(got, hash1) {
		t.Errorf("GetChild(5) = %v, want %v", got, hash1)
	}
}

func TestInternalNode_RemoveChild(t *testing.T) {
	node := NewInternalNode()

	hash1 := make([]byte, 32)
	hash1[0] = 0x01

	node.SetChild(5, hash1)
	node.RemoveChild(5)

	if node.ChildCount() != 0 {
		t.Errorf("ChildCount should be 0 after remove, got %d", node.ChildCount())
	}
	if !node.IsEmpty() {
		t.Errorf("Node should be empty after remove")
	}
}

func TestInternalNode_Serialization(t *testing.T) {
	hashSize := 32
	node := NewInternalNode()

	hash1 := make([]byte, hashSize)
	hash1[0] = 0x01
	hash2 := make([]byte, hashSize)
	hash2[0] = 0x02

	node.SetChild(3, hash1)
	node.SetChild(10, hash2)

	// 序列化
	encoded := EncodeInternalNode(node, hashSize)

	// 反序列化
	decoded, err := DecodeInternalNode(encoded, hashSize)
	if err != nil {
		t.Fatalf("DecodeInternalNode failed: %v", err)
	}

	// 验证
	if decoded.ChildBitmap != node.ChildBitmap {
		t.Errorf("Bitmap mismatch: %016b vs %016b", decoded.ChildBitmap, node.ChildBitmap)
	}
	if decoded.ChildCount() != 2 {
		t.Errorf("ChildCount should be 2, got %d", decoded.ChildCount())
	}
	if !bytes.Equal(decoded.GetChild(3), hash1) {
		t.Errorf("Child at nibble 3 mismatch")
	}
	if !bytes.Equal(decoded.GetChild(10), hash2) {
		t.Errorf("Child at nibble 10 mismatch")
	}
}

func TestLeafNode_Serialization(t *testing.T) {
	hashSize := sha256.Size

	keyHash := make([]byte, hashSize)
	valueHash := make([]byte, hashSize)
	keyHash[0] = 0xAB
	valueHash[0] = 0xCD

	node := NewLeafNode(keyHash, valueHash)

	// 序列化
	encoded := EncodeLeafNode(node)

	// 反序列化
	decoded, err := DecodeLeafNode(encoded, hashSize)
	if err != nil {
		t.Fatalf("DecodeLeafNode failed: %v", err)
	}

	if !bytes.Equal(decoded.KeyHash, keyHash) {
		t.Errorf("KeyHash mismatch")
	}
	if !bytes.Equal(decoded.ValueHash, valueHash) {
		t.Errorf("ValueHash mismatch")
	}
}

func TestGetNodeType(t *testing.T) {
	internalData := []byte{byte(NodeTypeInternal), 0, 0}
	leafData := []byte{byte(NodeTypeLeaf), 0, 0}
	emptyData := []byte{}

	if GetNodeType(internalData) != NodeTypeInternal {
		t.Errorf("Should detect internal node")
	}
	if GetNodeType(leafData) != NodeTypeLeaf {
		t.Errorf("Should detect leaf node")
	}
	if GetNodeType(emptyData) != NodeTypeNull {
		t.Errorf("Should detect null node")
	}

	if !IsInternalNodeData(internalData) {
		t.Errorf("IsInternalNodeData should return true")
	}
	if !IsLeafNodeData(leafData) {
		t.Errorf("IsLeafNodeData should return true")
	}
}
