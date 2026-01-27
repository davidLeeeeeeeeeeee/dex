package smt

import (
	"bytes"
	"hash"
	"reflect"
	"sync"
)

// ============================================
// JMT 16 叉 TreeHasher
// ============================================

// JMTHasher 是 16 叉 JMT 的哈希计算器
type JMTHasher struct {
	hasherPool  *sync.Pool
	zeroValue   []byte // placeholder hash (all zeros)
	hashSize    int
	placeholder []byte
}

// NewJMTHasher 创建新的 JMT 哈希器
func NewJMTHasher(hasher hash.Hash) *JMTHasher {
	hashSize := hasher.Size()
	jh := &JMTHasher{
		hashSize:    hashSize,
		zeroValue:   make([]byte, hashSize),
		placeholder: make([]byte, hashSize),
	}
	jh.hasherPool = &sync.Pool{
		New: func() interface{} {
			return reflect.New(reflect.TypeOf(hasher).Elem()).Interface().(hash.Hash)
		},
	}
	return jh
}

// HashSize 返回哈希长度
func (jh *JMTHasher) HashSize() int {
	return jh.hashSize
}

// Placeholder 返回空节点的占位符哈希 (全零)
func (jh *JMTHasher) Placeholder() []byte {
	return jh.placeholder
}

// IsPlaceholder 检查是否为占位符
func (jh *JMTHasher) IsPlaceholder(hash []byte) bool {
	return bytes.Equal(hash, jh.placeholder)
}

// Digest 计算任意数据的哈希
func (jh *JMTHasher) Digest(data []byte) []byte {
	h := jh.hasherPool.Get().(hash.Hash)
	defer jh.hasherPool.Put(h)
	h.Reset()
	h.Write(data)
	return h.Sum(nil)
}

// Path 将原始 Key 转换为路径 (哈希后的结果)
func (jh *JMTHasher) Path(key []byte) []byte {
	return jh.Digest(key)
}

// MaxDepth 返回树的最大深度 (Nibble 数量)
// 对于 32 字节哈希，深度为 64
func (jh *JMTHasher) MaxDepth() int {
	return jh.hashSize * 2
}

// ============================================
// 叶子节点哈希
// ============================================

// DigestLeafNode 计算叶子节点的哈希
// 输入: keyHash (路径), valueHash (值的哈希)
// 输出: nodeHash, encodedNode
func (jh *JMTHasher) DigestLeafNode(keyHash, valueHash []byte) ([]byte, []byte) {
	leaf := NewLeafNode(keyHash, valueHash)
	encoded := EncodeLeafNode(leaf)
	nodeHash := jh.Digest(encoded)
	return nodeHash, encoded
}

// ============================================
// 内部节点哈希
// ============================================

// DigestInternalNode 计算内部节点的哈希
// 输入: 16 个子节点哈希 (空子节点使用 placeholder)
// 输出: nodeHash, encodedNode
func (jh *JMTHasher) DigestInternalNode(children [16][]byte) ([]byte, []byte) {
	node := InternalNodeFromChildren(children, jh.placeholder)
	encoded := EncodeInternalNode(node, jh.hashSize)
	nodeHash := jh.Digest(encoded)
	return nodeHash, encoded
}

// DigestInternalNodeFromNode 从 InternalNode 结构计算哈希
func (jh *JMTHasher) DigestInternalNodeFromNode(node *InternalNode) ([]byte, []byte) {
	encoded := EncodeInternalNode(node, jh.hashSize)
	nodeHash := jh.Digest(encoded)
	return nodeHash, encoded
}

// ============================================
// 节点解析
// ============================================

// ParseLeafNode 解析叶子节点数据
func (jh *JMTHasher) ParseLeafNode(data []byte) (*LeafNode, error) {
	return DecodeLeafNode(data, jh.hashSize)
}

// ParseInternalNode 解析内部节点数据
func (jh *JMTHasher) ParseInternalNode(data []byte) (*InternalNode, error) {
	return DecodeInternalNode(data, jh.hashSize)
}

// GetNodeType 获取节点类型
func (jh *JMTHasher) GetNodeType(data []byte) NodeType {
	return GetNodeType(data)
}

// ============================================
// 批量子节点哈希 (用于 Proof 验证)
// ============================================

// ComputeInternalNodeHash 根据 bitmap 和兄弟节点计算内部节点哈希
// targetNibble: 目标子节点位置
// targetHash: 目标子节点的哈希
// siblings: 兄弟节点信息
func (jh *JMTHasher) ComputeInternalNodeHash(targetNibble byte, targetHash []byte, siblings *SiblingInfo) []byte {
	var children [16][]byte
	for i := byte(0); i < 16; i++ {
		children[i] = jh.placeholder
	}

	// 设置目标子节点
	children[targetNibble] = targetHash

	// 从 siblings 中恢复其他子节点
	sibIdx := 0
	for nibble := byte(0); nibble < 16; nibble++ {
		if nibble == targetNibble {
			continue
		}
		if siblings.Bitmap&(1<<nibble) != 0 {
			children[nibble] = siblings.Siblings[sibIdx]
			sibIdx++
		}
	}

	hash, _ := jh.DigestInternalNode(children)
	return hash
}

// ============================================
// SiblingInfo 兄弟节点信息 (用于 Proof)
// ============================================

// SiblingInfo 单层的兄弟节点信息
type SiblingInfo struct {
	// Bitmap 该层 InternalNode 的子节点位图 (不包含路径上的子节点)
	Bitmap uint16
	// Siblings 除了路径上的子节点外，其他所有非空兄弟节点的哈希
	Siblings [][]byte
}

// ExtractSiblings 从内部节点中提取兄弟节点信息
// pathNibble: 当前路径使用的 nibble
// 返回: 除 pathNibble 外所有非空子节点的信息
func (jh *JMTHasher) ExtractSiblings(node *InternalNode, pathNibble byte) *SiblingInfo {
	info := &SiblingInfo{
		Bitmap:   0,
		Siblings: make([][]byte, 0),
	}

	for nibble := byte(0); nibble < 16; nibble++ {
		if nibble == pathNibble {
			continue
		}
		child := node.GetChild(nibble)
		if child != nil && !jh.IsPlaceholder(child) {
			info.Bitmap |= (1 << nibble)
			info.Siblings = append(info.Siblings, child)
		}
	}

	return info
}
