package smt

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
)

// ============================================
// JMT 16 叉节点类型定义
// ============================================

// NodeType 节点类型
type NodeType byte

const (
	// NodeTypeNull 空节点 (placeholder)
	NodeTypeNull NodeType = 0
	// NodeTypeInternal 内部节点 (16 叉分支)
	NodeTypeInternal NodeType = 1
	// NodeTypeLeaf 叶子节点
	NodeTypeLeaf NodeType = 2
)

// 节点类型前缀 (用于序列化)
var (
	internalNodePrefix = []byte{byte(NodeTypeInternal)}
	leafNodePrefix     = []byte{byte(NodeTypeLeaf)}
)

// ============================================
// InternalNode 16 叉内部节点
// ============================================

// InternalNode 16 叉内部节点
// 使用 ChildBitmap 标记哪些子节点存在，避免存储 16 个空指针
type InternalNode struct {
	// ChildBitmap 16 位位图，第 i 位为 1 表示 children[i] 存在
	// Bit 0 对应 Nibble 0, Bit 15 对应 Nibble 15
	ChildBitmap uint16
	// Children 只存储非空子节点的哈希，顺序与 Bitmap 中置位的顺序一致
	// 每个哈希长度为 hashSize (通常 32 bytes for SHA256)
	Children [][]byte
}

// NewInternalNode 创建新的内部节点
func NewInternalNode() *InternalNode {
	return &InternalNode{
		ChildBitmap: 0,
		Children:    nil,
	}
}

// SetChild 设置指定 nibble 位置的子节点哈希
func (n *InternalNode) SetChild(nibble byte, hash []byte) {
	if nibble > 15 {
		return
	}
	mask := uint16(1) << nibble
	if n.ChildBitmap&mask != 0 {
		// 已存在，更新
		idx := n.childIndex(nibble)
		n.Children[idx] = hash
	} else {
		// 不存在，插入
		n.ChildBitmap |= mask
		idx := n.childIndex(nibble)
		newChildren := make([][]byte, len(n.Children)+1)
		copy(newChildren[:idx], n.Children[:idx])
		newChildren[idx] = hash
		copy(newChildren[idx+1:], n.Children[idx:])
		n.Children = newChildren
	}
}

// GetChild 获取指定 nibble 位置的子节点哈希
// 返回 nil 如果该位置没有子节点
func (n *InternalNode) GetChild(nibble byte) []byte {
	if nibble > 15 {
		return nil
	}
	mask := uint16(1) << nibble
	if n.ChildBitmap&mask == 0 {
		return nil
	}
	idx := n.childIndex(nibble)
	return n.Children[idx]
}

// RemoveChild 移除指定 nibble 位置的子节点
func (n *InternalNode) RemoveChild(nibble byte) {
	if nibble > 15 {
		return
	}
	mask := uint16(1) << nibble
	if n.ChildBitmap&mask == 0 {
		return // 不存在
	}
	idx := n.childIndex(nibble)
	n.Children = append(n.Children[:idx], n.Children[idx+1:]...)
	n.ChildBitmap &^= mask
}

// ChildCount 返回非空子节点数量
func (n *InternalNode) ChildCount() int {
	return bits.OnesCount16(n.ChildBitmap)
}

// IsEmpty 检查节点是否为空
func (n *InternalNode) IsEmpty() bool {
	return n.ChildBitmap == 0
}

// childIndex 计算 nibble 在 Children 数组中的实际索引
// 通过计算 bitmap 中小于 nibble 位置的置位数来确定
func (n *InternalNode) childIndex(nibble byte) int {
	// 计算 nibble 之前有多少个置位
	mask := uint16(1)<<nibble - 1 // nibble 之前的所有位
	return bits.OnesCount16(n.ChildBitmap & mask)
}

// GetAllChildren 返回所有非空子节点的 (nibble, hash) 对
func (n *InternalNode) GetAllChildren() [][2]interface{} {
	result := make([][2]interface{}, 0, n.ChildCount())
	idx := 0
	for nibble := byte(0); nibble < 16; nibble++ {
		if n.ChildBitmap&(1<<nibble) != 0 {
			result = append(result, [2]interface{}{nibble, n.Children[idx]})
			idx++
		}
	}
	return result
}

// ============================================
// LeafNode 叶子节点
// ============================================

// LeafNode 叶子节点
type LeafNode struct {
	// KeyHash 原始 Key 的哈希 (通常 32 bytes)
	KeyHash []byte
	// ValueHash 值的哈希 (通常 32 bytes)
	ValueHash []byte
}

// NewLeafNode 创建新的叶子节点
func NewLeafNode(keyHash, valueHash []byte) *LeafNode {
	return &LeafNode{
		KeyHash:   keyHash,
		ValueHash: valueHash,
	}
}

// ============================================
// 序列化与反序列化
// ============================================

// 序列化格式:
// InternalNode: [Type=1][Bitmap 2 bytes][Child1 Hash]...[ChildN Hash]
// LeafNode:     [Type=2][KeyHash 32 bytes][ValueHash 32 bytes]

// EncodeInternalNode 序列化内部节点
func EncodeInternalNode(node *InternalNode, hashSize int) []byte {
	// 1 byte type + 2 bytes bitmap + N * hashSize bytes children
	size := 1 + 2 + len(node.Children)*hashSize
	buf := make([]byte, size)
	buf[0] = byte(NodeTypeInternal)
	binary.BigEndian.PutUint16(buf[1:3], node.ChildBitmap)
	offset := 3
	for _, child := range node.Children {
		copy(buf[offset:offset+hashSize], child)
		offset += hashSize
	}
	return buf
}

// DecodeInternalNode 反序列化内部节点
func DecodeInternalNode(data []byte, hashSize int) (*InternalNode, error) {
	if len(data) < 3 {
		return nil, errors.New("internal node data too short")
	}
	if data[0] != byte(NodeTypeInternal) {
		return nil, fmt.Errorf("expected internal node type, got %d", data[0])
	}

	bitmap := binary.BigEndian.Uint16(data[1:3])
	childCount := bits.OnesCount16(bitmap)
	expectedLen := 3 + childCount*hashSize
	if len(data) < expectedLen {
		return nil, fmt.Errorf("internal node data too short: expected %d, got %d", expectedLen, len(data))
	}

	children := make([][]byte, childCount)
	offset := 3
	for i := 0; i < childCount; i++ {
		child := make([]byte, hashSize)
		copy(child, data[offset:offset+hashSize])
		children[i] = child
		offset += hashSize
	}

	return &InternalNode{
		ChildBitmap: bitmap,
		Children:    children,
	}, nil
}

// EncodeLeafNode 序列化叶子节点
func EncodeLeafNode(node *LeafNode) []byte {
	// 1 byte type + keyHash + valueHash
	size := 1 + len(node.KeyHash) + len(node.ValueHash)
	buf := make([]byte, size)
	buf[0] = byte(NodeTypeLeaf)
	copy(buf[1:1+len(node.KeyHash)], node.KeyHash)
	copy(buf[1+len(node.KeyHash):], node.ValueHash)
	return buf
}

// DecodeLeafNode 反序列化叶子节点
func DecodeLeafNode(data []byte, hashSize int) (*LeafNode, error) {
	expectedLen := 1 + 2*hashSize
	if len(data) < expectedLen {
		return nil, fmt.Errorf("leaf node data too short: expected %d, got %d", expectedLen, len(data))
	}
	if data[0] != byte(NodeTypeLeaf) {
		return nil, fmt.Errorf("expected leaf node type, got %d", data[0])
	}

	keyHash := make([]byte, hashSize)
	valueHash := make([]byte, hashSize)
	copy(keyHash, data[1:1+hashSize])
	copy(valueHash, data[1+hashSize:1+2*hashSize])

	return &LeafNode{
		KeyHash:   keyHash,
		ValueHash: valueHash,
	}, nil
}

// GetNodeType 从序列化数据中获取节点类型
func GetNodeType(data []byte) NodeType {
	if len(data) == 0 {
		return NodeTypeNull
	}
	return NodeType(data[0])
}

// IsLeafNodeData 检查数据是否为叶子节点
func IsLeafNodeData(data []byte) bool {
	return len(data) > 0 && data[0] == byte(NodeTypeLeaf)
}

// IsInternalNodeData 检查数据是否为内部节点
func IsInternalNodeData(data []byte) bool {
	return len(data) > 0 && data[0] == byte(NodeTypeInternal)
}

// ============================================
// 节点哈希计算辅助
// ============================================

// InternalNodeFromChildren 从 16 个子节点哈希创建内部节点
// placeholder 是空节点的占位符哈希
func InternalNodeFromChildren(children [16][]byte, placeholder []byte) *InternalNode {
	node := NewInternalNode()
	for i := byte(0); i < 16; i++ {
		if children[i] != nil && !bytes.Equal(children[i], placeholder) {
			node.SetChild(i, children[i])
		}
	}
	return node
}

// ToChildrenArray 将内部节点展开为 16 元素数组
// 空位置用 placeholder 填充
func (n *InternalNode) ToChildrenArray(placeholder []byte) [16][]byte {
	var result [16][]byte
	idx := 0
	for nibble := byte(0); nibble < 16; nibble++ {
		if n.ChildBitmap&(1<<nibble) != 0 {
			result[nibble] = n.Children[idx]
			idx++
		} else {
			result[nibble] = placeholder
		}
	}
	return result
}
