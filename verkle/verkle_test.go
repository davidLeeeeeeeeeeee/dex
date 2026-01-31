package verkle

import (
	"testing"
)

// ============================================
// Verkle Tree 核心功能测试
// ============================================

func TestVerkleTreeBasicCRUD(t *testing.T) {
	// 创建内存存储
	store := NewSimpleVersionedMap()
	tree := NewVerkleTree(store)

	// 1. 测试空树获取
	_, err := tree.Get([]byte("key1"), 0)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound for empty tree, got: %v", err)
	}

	// 2. 测试插入
	keys := [][]byte{[]byte("key1")}
	values := [][]byte{[]byte("value1")}

	root1, err := tree.Update(keys, values, Version(1))
	if err != nil {
		t.Fatalf("failed to update tree: %v", err)
	}
	if len(root1) == 0 {
		t.Error("root commitment should not be empty after update")
	}

	// 3. 测试读取
	val, err := tree.Get([]byte("key1"), Version(1))
	if err != nil {
		t.Fatalf("failed to get value: %v", err)
	}
	if string(val) != "value1" {
		t.Errorf("expected 'value1', got '%s'", string(val))
	}

	// 4. 测试更新
	root2, err := tree.Update([][]byte{[]byte("key1")}, [][]byte{[]byte("value1_updated")}, Version(2))
	if err != nil {
		t.Fatalf("failed to update tree: %v", err)
	}
	if string(root1) == string(root2) {
		t.Error("root should change after update")
	}

	val2, err := tree.Get([]byte("key1"), Version(2))
	if err != nil {
		t.Fatalf("failed to get updated value: %v", err)
	}
	if string(val2) != "value1_updated" {
		t.Errorf("expected 'value1_updated', got '%s'", string(val2))
	}

	t.Logf("Basic CRUD test passed. Root v1: %x, Root v2: %x", root1[:8], root2[:8])
}

func TestVerkleTreeMultipleKeys(t *testing.T) {
	store := NewSimpleVersionedMap()
	tree := NewVerkleTree(store)

	// 插入多个 key
	keys := [][]byte{
		[]byte("account:alice"),
		[]byte("account:bob"),
		[]byte("account:charlie"),
	}
	values := [][]byte{
		[]byte("1000"),
		[]byte("2000"),
		[]byte("3000"),
	}

	root, err := tree.Update(keys, values, Version(1))
	if err != nil {
		t.Fatalf("failed to update multiple keys: %v", err)
	}

	// 验证所有 key
	for i, key := range keys {
		val, err := tree.Get(key, Version(1))
		if err != nil {
			t.Errorf("failed to get key %s: %v", string(key), err)
			continue
		}
		if string(val) != string(values[i]) {
			t.Errorf("key %s: expected '%s', got '%s'", string(key), string(values[i]), string(val))
		}
	}

	t.Logf("Multiple keys test passed. Root: %x", root[:8])
}

func TestVerkleTreeDelete(t *testing.T) {
	store := NewSimpleVersionedMap()
	tree := NewVerkleTree(store)

	// 插入
	keys := [][]byte{[]byte("key1"), []byte("key2")}
	values := [][]byte{[]byte("value1"), []byte("value2")}
	_, err := tree.Update(keys, values, Version(1))
	if err != nil {
		t.Fatalf("failed to insert: %v", err)
	}

	// 删除 key1
	root2, err := tree.Delete([]byte("key1"), Version(2))
	if err != nil {
		t.Fatalf("failed to delete: %v", err)
	}

	// 验证 key1 已删除
	_, err = tree.Get([]byte("key1"), Version(2))
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound for deleted key, got: %v", err)
	}

	// 验证 key2 仍存在
	val, err := tree.Get([]byte("key2"), Version(2))
	if err != nil {
		t.Errorf("key2 should still exist: %v", err)
	}
	if string(val) != "value2" {
		t.Errorf("key2: expected 'value2', got '%s'", string(val))
	}

	t.Logf("Delete test passed. Root after delete: %x", root2[:8])
}

func TestVerkleTreeProof(t *testing.T) {
	store := NewSimpleVersionedMap()
	tree := NewVerkleTree(store)

	// 插入
	_, err := tree.Update([][]byte{[]byte("key1")}, [][]byte{[]byte("value1")}, Version(1))
	if err != nil {
		t.Fatalf("failed to insert: %v", err)
	}

	// 生成存在性证明
	proof, err := tree.Prove([]byte("key1"))
	if err != nil {
		t.Fatalf("failed to generate proof: %v", err)
	}

	if !proof.IsMembershipProof() {
		t.Error("should be a membership proof")
	}

	if string(proof.Value) != "value1" {
		t.Errorf("proof value: expected 'value1', got '%s'", string(proof.Value))
	}

	// 验证证明
	valid := VerifyVerkleProof(proof, tree.Root(), tree.Committer())
	if !valid {
		t.Error("proof verification failed")
	}

	// 生成不存在性证明
	proof2, err := tree.Prove([]byte("nonexistent"))
	if err != nil {
		t.Fatalf("failed to generate non-membership proof: %v", err)
	}

	if proof2.IsMembershipProof() {
		t.Error("should be a non-membership proof")
	}

	t.Logf("Proof test passed. Proof commitments count: %d", len(proof.Commitments))
}

// ============================================
// 节点测试
// ============================================

func TestInternalNode256(t *testing.T) {
	node := NewInternalNode256()

	// 测试空节点
	if !node.IsEmpty() {
		t.Error("new node should be empty")
	}

	// 设置子节点
	commitment := make([]byte, 32)
	commitment[0] = 0x01
	node.SetChild(0, commitment)

	if node.IsEmpty() {
		t.Error("node should not be empty after SetChild")
	}

	if !node.HasChild(0) {
		t.Error("node should have child at index 0")
	}

	if node.HasChild(1) {
		t.Error("node should not have child at index 1")
	}

	// 获取子节点
	got := node.GetChild(0)
	if string(got) != string(commitment) {
		t.Errorf("GetChild mismatch: expected %x, got %x", commitment, got)
	}

	// 测试序列化
	data := EncodeInternalNode256(node)
	decoded, err := DecodeInternalNode256(data)
	if err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if decoded.ChildCount() != node.ChildCount() {
		t.Errorf("child count mismatch: expected %d, got %d", node.ChildCount(), decoded.ChildCount())
	}

	t.Logf("InternalNode256 test passed. Encoded size: %d bytes", len(data))
}

func TestLeafNode(t *testing.T) {
	var stem [31]byte
	stem[0] = 0xAB
	stem[1] = 0xCD

	leaf := NewLeafNode(stem)

	// 设置值
	leaf.SetValue(0, []byte("value0"))
	leaf.SetValue(255, []byte("value255"))

	if leaf.ValueCount() != 2 {
		t.Errorf("expected 2 values, got %d", leaf.ValueCount())
	}

	if !leaf.HasValue(0) || !leaf.HasValue(255) {
		t.Error("missing expected values")
	}

	// 测试序列化
	data := EncodeLeafNode(leaf)
	decoded, err := DecodeLeafNode(data)
	if err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if decoded.Stem != leaf.Stem {
		t.Errorf("stem mismatch")
	}

	if string(decoded.GetValue(0)) != "value0" {
		t.Errorf("value0 mismatch")
	}

	if string(decoded.GetValue(255)) != "value255" {
		t.Errorf("value255 mismatch")
	}

	t.Logf("LeafNode test passed. Encoded size: %d bytes", len(data))
}

// ============================================
// 承诺测试
// ============================================

func TestPedersenCommitment(t *testing.T) {
	committer := NewPedersenCommitter()

	// 零承诺
	zero := committer.ZeroCommitment()
	if len(zero) != 32 {
		t.Errorf("zero commitment should be 32 bytes, got %d", len(zero))
	}

	// 测试向量承诺
	var values [256][]byte
	values[0] = []byte{1, 2, 3}
	values[1] = []byte{4, 5, 6}

	commitment1, err := committer.CommitToVector(values)
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	if len(commitment1) != 32 {
		t.Errorf("commitment should be 32 bytes, got %d", len(commitment1))
	}

	// 相同输入应产生相同承诺
	commitment2, err := committer.CommitToVector(values)
	if err != nil {
		t.Fatalf("failed to commit again: %v", err)
	}

	if string(commitment1) != string(commitment2) {
		t.Error("same input should produce same commitment")
	}

	// 不同输入应产生不同承诺
	values[0] = []byte{7, 8, 9}
	commitment3, err := committer.CommitToVector(values)
	if err != nil {
		t.Fatalf("failed to commit different values: %v", err)
	}

	if string(commitment1) == string(commitment3) {
		t.Error("different input should produce different commitment")
	}

	t.Logf("Pedersen commitment test passed. Commitment size: %d bytes", len(commitment1))
}

// ============================================
// 工具函数测试
// ============================================

func TestToVerkleKey(t *testing.T) {
	key1 := ToVerkleKey([]byte("hello"))
	key2 := ToVerkleKey([]byte("hello"))
	key3 := ToVerkleKey([]byte("world"))

	// 相同输入相同输出
	if key1 != key2 {
		t.Error("same input should produce same key")
	}

	// 不同输入不同输出
	if key1 == key3 {
		t.Error("different input should produce different key")
	}

	// 输出应该是 32 字节
	if len(key1) != 32 {
		t.Errorf("key should be 32 bytes, got %d", len(key1))
	}
}

func TestStemAndSuffix(t *testing.T) {
	key := ToVerkleKey([]byte("test"))
	stem := GetStemFromKey(key)
	suffix := GetSuffixFromKey(key)

	// stem 应该是前 31 字节
	for i := 0; i < 31; i++ {
		if key[i] != stem[i] {
			t.Errorf("stem[%d] mismatch", i)
		}
	}

	// suffix 应该是最后 1 字节
	if key[31] != suffix {
		t.Error("suffix mismatch")
	}
}
