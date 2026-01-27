package smt

import (
	"crypto/sha256"
	"errors"
	"testing"
)

func TestBugFixPanicOnCorruptedNode(t *testing.T) {
	// 复现报送的 Bug 场景：MapStore 返回长度不足的数据
	smn, smv := NewSimpleMap(), NewSimpleMap()
	smt := NewSparseMerkleTree(smn, smv, sha256.New())

	// 手动向节点库注入一条非法（长度不足）的数据
	// 在原 Bug 中，parseNode 会尝试读取 [1:33] 和 [33:]，如果长度只有 1 字节就会 Panic
	smn.Set([]byte("not my root"), []byte("v"))

	// 捕获可能产生的 Panic
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("The code still panics: %v", r)
		}
	}()

	// 执行操作。在修复后，这里应该返回 errCorruptedTree 而不是 Panic
	_, err := smt.UpdateForRoot([]byte("key"), []byte("values"), []byte("not my root"))

	if err == nil {
		t.Error("Expected an error for corrupted node, but got nil")
	}

	if !errors.Is(err, errCorruptedTree) {
		t.Errorf("Expected error %v, but got %v", errCorruptedTree, err)
	}
}

func TestBugFixPanicOnCorruptedLeaf(t *testing.T) {
	smn, smv := NewSimpleMap(), NewSimpleMap()
	smt := NewSparseMerkleTree(smn, smv, sha256.New())

	// 构造一个场景：根节点被标记为叶子节点，但数据长度不足
	// leafPrefix 是 []byte{0}
	smn.Set([]byte("bad leaf root"), []byte{0, 1, 2}) // 长度不足

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("The code still panics on corrupted leaf: %v", r)
		}
	}()

	_, err := smt.UpdateForRoot([]byte("key"), []byte("values"), []byte("bad leaf root"))

	if err == nil {
		t.Error("Expected an error for corrupted leaf, but got nil")
	}

	if !errors.Is(err, errCorruptedTree) {
		t.Errorf("Expected error %v, but got %v", errCorruptedTree, err)
	}
}
