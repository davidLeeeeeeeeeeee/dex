package smt

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"testing"
)

// ============================================
// JMT Proof 测试
// ============================================

func TestJMTProof_MembershipProof(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	key := []byte("hello")
	value := []byte("world")

	// 插入
	root, err := jmt.Update([][]byte{key}, [][]byte{value}, 1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// 生成证明
	proof, err := jmt.Prove(key)
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}

	// 验证是存在性证明
	if !proof.IsMembershipProof() {
		t.Errorf("Should be membership proof")
	}

	// 验证证明
	if !VerifyJMTProof(proof, root, sha256.New()) {
		t.Errorf("Proof verification failed")
	}
}

func TestJMTProof_NonMembershipProof(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	key1 := []byte("key1")
	value1 := []byte("value1")
	key2 := []byte("nonexistent")

	// 插入 key1
	root, err := jmt.Update([][]byte{key1}, [][]byte{value1}, 1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// 为不存在的 key2 生成证明
	proof, err := jmt.Prove(key2)
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}

	// 验证是不存在性证明
	if proof.IsMembershipProof() {
		t.Errorf("Should be non-membership proof")
	}

	// 验证证明
	if !VerifyJMTProof(proof, root, sha256.New()) {
		t.Errorf("Non-membership proof verification failed")
	}
}

func TestJMTProof_EmptyTree(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	key := []byte("any")

	// 为空树生成证明
	proof, err := jmt.Prove(key)
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}

	// 应该是不存在性证明
	if proof.IsMembershipProof() {
		t.Errorf("Should be non-membership proof for empty tree")
	}

	// 验证
	if !VerifyJMTProof(proof, jmt.Root(), sha256.New()) {
		t.Errorf("Proof verification failed for empty tree")
	}
}

func TestJMTProof_MultipleKeys(t *testing.T) {
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

	root, err := jmt.Update(keys, values, 1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// 为每个 Key 生成并验证证明
	for i, key := range keys {
		proof, err := jmt.Prove(key)
		if err != nil {
			t.Fatalf("Prove(%s) failed: %v", key, err)
		}

		if !proof.IsMembershipProof() {
			t.Errorf("Proof for %s should be membership proof", key)
		}

		if !bytes.Equal(proof.Value, values[i]) {
			t.Errorf("Proof value mismatch for %s", key)
		}

		if !VerifyJMTProof(proof, root, sha256.New()) {
			t.Errorf("Proof verification failed for %s", key)
		}
	}
}

func TestJMTProof_WrongRootFails(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	key := []byte("key")
	value := []byte("value")

	_, err := jmt.Update([][]byte{key}, [][]byte{value}, 1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	proof, err := jmt.Prove(key)
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}

	// 使用错误的根验证
	wrongRoot := make([]byte, 32)
	wrongRoot[0] = 0xFF

	if VerifyJMTProof(proof, wrongRoot, sha256.New()) {
		t.Errorf("Proof should fail with wrong root")
	}
}

func TestJMTProof_ManyKeys(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	// 插入 50 个 Key
	count := 50
	keys := make([][]byte, count)
	values := make([][]byte, count)
	for i := 0; i < count; i++ {
		keys[i] = []byte(fmt.Sprintf("key-%04d", i))
		values[i] = []byte(fmt.Sprintf("value-%04d", i))
	}

	root, err := jmt.Update(keys, values, 1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// 验证部分 Key 的证明
	for i := 0; i < count; i += 5 {
		proof, err := jmt.Prove(keys[i])
		if err != nil {
			t.Fatalf("Prove(%d) failed: %v", i, err)
		}

		if !VerifyJMTProof(proof, root, sha256.New()) {
			t.Errorf("Proof verification failed for key-%04d", i)
		}
	}
}

func TestSiblingInfo_Serialization(t *testing.T) {
	hashSize := 32

	info := &SiblingInfo{
		Bitmap: 0b0000010000001000, // nibble 3 和 10
		Siblings: [][]byte{
			make([]byte, hashSize),
			make([]byte, hashSize),
		},
	}
	info.Siblings[0][0] = 0x03
	info.Siblings[1][0] = 0x0A

	// 序列化
	encoded := EncodeSiblingInfo(info, hashSize)

	// 反序列化
	decoded, bytesRead, err := DecodeSiblingInfo(encoded, hashSize)
	if err != nil {
		t.Fatalf("DecodeSiblingInfo failed: %v", err)
	}

	if bytesRead != len(encoded) {
		t.Errorf("bytesRead = %d, want %d", bytesRead, len(encoded))
	}

	if decoded.Bitmap != info.Bitmap {
		t.Errorf("Bitmap mismatch")
	}

	if len(decoded.Siblings) != len(info.Siblings) {
		t.Errorf("Siblings count mismatch")
	}

	for i, sib := range decoded.Siblings {
		if !bytes.Equal(sib, info.Siblings[i]) {
			t.Errorf("Sibling %d mismatch", i)
		}
	}
}

func TestJMTProof_Size(t *testing.T) {
	store := NewSimpleVersionedMap()
	jmt := NewJMT(store, sha256.New())

	// 插入一些数据
	keys := make([][]byte, 100)
	values := make([][]byte, 100)
	for i := 0; i < 100; i++ {
		keys[i] = []byte(fmt.Sprintf("key-%04d", i))
		values[i] = []byte(fmt.Sprintf("value-%04d", i))
	}

	_, err := jmt.Update(keys, values, 1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// 检查证明大小
	proof, err := jmt.Prove(keys[50])
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}

	size := proof.ProofSize(32)
	t.Logf("Proof size for key-0050: %d bytes, %d siblings", size, len(proof.Siblings))

	// 16 叉树的证明应该比较紧凑
	// 对于 100 个 Key，树深度应该很浅
	if len(proof.Siblings) > 10 {
		t.Logf("Warning: proof has %d siblings, expected less than 10 for 100 keys", len(proof.Siblings))
	}
}
