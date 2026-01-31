package verkle

import (
	"encoding/binary"
	"errors"
)

// ============================================
// Verkle Tree 256 叉节点类型
// ============================================

// NodeType 节点类型
type NodeType byte

const (
	NodeTypeEmpty    NodeType = 0 // 空节点
	NodeTypeInternal NodeType = 1 // 内部节点 (256 叉)
	NodeTypeLeaf     NodeType = 2 // 叶子节点
)

// CommitmentSize Pedersen 承诺大小 (32 字节)
const CommitmentSize = 32

// StemSize Stem 大小 (31 字节)
const StemSize = 31

// ============================================
// InternalNode256 256 叉内部节点
// ============================================

// InternalNode256 256 叉内部节点
// 使用位图标记哪些子节点存在
type InternalNode256 struct {
	// Bitmap 256 位位图，标记哪些子节点存在
	// 使用 [4]uint64 表示 256 位
	Bitmap [4]uint64

	// Children 只存储非空子节点的承诺
	Children [][]byte

	// Commitment 本节点的 Pedersen 承诺 (C = Σ child_i * G_i)
	Commitment []byte
}

// NewInternalNode256 创建空的内部节点
func NewInternalNode256() *InternalNode256 {
	return &InternalNode256{
		Bitmap:     [4]uint64{},
		Children:   nil,
		Commitment: nil,
	}
}

// HasChild 检查指定索引是否有子节点
func (n *InternalNode256) HasChild(index byte) bool {
	wordIdx := index / 64
	bitIdx := index % 64
	return n.Bitmap[wordIdx]&(1<<bitIdx) != 0
}

// SetChild 设置指定索引的子节点承诺
func (n *InternalNode256) SetChild(index byte, commitment []byte) {
	wordIdx := index / 64
	bitIdx := index % 64
	mask := uint64(1) << bitIdx

	if n.Bitmap[wordIdx]&mask != 0 {
		// 已存在，更新
		idx := n.childIndex(index)
		n.Children[idx] = commitment
	} else {
		// 不存在，插入
		n.Bitmap[wordIdx] |= mask
		idx := n.childIndex(index)
		newChildren := make([][]byte, len(n.Children)+1)
		copy(newChildren[:idx], n.Children[:idx])
		newChildren[idx] = commitment
		if idx < len(n.Children) {
			copy(newChildren[idx+1:], n.Children[idx:])
		}
		n.Children = newChildren
	}
}

// GetChild 获取指定索引的子节点承诺
func (n *InternalNode256) GetChild(index byte) []byte {
	if !n.HasChild(index) {
		return nil
	}
	idx := n.childIndex(index)
	return n.Children[idx]
}

// RemoveChild 移除指定索引的子节点
func (n *InternalNode256) RemoveChild(index byte) {
	if !n.HasChild(index) {
		return
	}

	idx := n.childIndex(index)
	n.Children = append(n.Children[:idx], n.Children[idx+1:]...)

	wordIdx := index / 64
	bitIdx := index % 64
	n.Bitmap[wordIdx] &^= (1 << bitIdx)
}

// ChildCount 返回非空子节点数量
func (n *InternalNode256) ChildCount() int {
	count := 0
	for i := 0; i < 4; i++ {
		count += popCount64(n.Bitmap[i])
	}
	return count
}

// IsEmpty 检查节点是否为空
func (n *InternalNode256) IsEmpty() bool {
	return n.Bitmap[0] == 0 && n.Bitmap[1] == 0 && n.Bitmap[2] == 0 && n.Bitmap[3] == 0
}

// childIndex 计算索引在 Children 数组中的实际位置
func (n *InternalNode256) childIndex(index byte) int {
	count := 0
	wordIdx := index / 64
	bitIdx := index % 64

	// 统计前面完整 word 的置位数
	for i := 0; i < int(wordIdx); i++ {
		count += popCount64(n.Bitmap[i])
	}

	// 统计当前 word 中 bitIdx 之前的置位数
	mask := uint64(1)<<bitIdx - 1
	count += popCount64(n.Bitmap[wordIdx] & mask)

	return count
}

// GetAllChildren 获取所有非空子节点 (index, commitment) 对
func (n *InternalNode256) GetAllChildren() [][2]interface{} {
	result := make([][2]interface{}, 0, n.ChildCount())
	idx := 0
	for i := 0; i < 256; i++ {
		if n.HasChild(byte(i)) {
			result = append(result, [2]interface{}{byte(i), n.Children[idx]})
			idx++
		}
	}
	return result
}

// popCount64 计算 64 位整数中 1 的个数
func popCount64(x uint64) int {
	// Brian Kernighan's algorithm
	count := 0
	for x != 0 {
		x &= x - 1
		count++
	}
	return count
}

// ============================================
// LeafNode 叶子节点
// ============================================

// LeafNode 叶子节点
// 存储一个 Stem 和最多 256 个值
type LeafNode struct {
	// Stem Key 的前 31 字节
	Stem [StemSize]byte

	// Values 256 个值槽（索引由 Key 的最后一字节决定）
	// nil 表示该槽为空
	Values [256][]byte

	// Commitment 本节点的 Pedersen 承诺
	Commitment []byte
}

// NewLeafNode 创建新的叶子节点
func NewLeafNode(stem [StemSize]byte) *LeafNode {
	return &LeafNode{
		Stem:       stem,
		Values:     [256][]byte{},
		Commitment: nil,
	}
}

// SetValue 设置指定索引的值
func (n *LeafNode) SetValue(index byte, value []byte) {
	n.Values[index] = value
}

// GetValue 获取指定索引的值
func (n *LeafNode) GetValue(index byte) []byte {
	return n.Values[index]
}

// DeleteValue 删除指定索引的值
func (n *LeafNode) DeleteValue(index byte) {
	n.Values[index] = nil
}

// HasValue 检查指定索引是否有值
func (n *LeafNode) HasValue(index byte) bool {
	return n.Values[index] != nil
}

// ValueCount 返回非空值的数量
func (n *LeafNode) ValueCount() int {
	count := 0
	for i := 0; i < 256; i++ {
		if n.Values[i] != nil {
			count++
		}
	}
	return count
}

// IsEmpty 检查叶子节点是否为空
func (n *LeafNode) IsEmpty() bool {
	for i := 0; i < 256; i++ {
		if n.Values[i] != nil {
			return false
		}
	}
	return true
}

// ============================================
// 节点序列化
// ============================================

// EncodeInternalNode256 序列化内部节点
// 格式: [Type 1B][Bitmap 32B][NumChildren 2B][Children...]
func EncodeInternalNode256(node *InternalNode256) []byte {
	numChildren := node.ChildCount()
	size := 1 + 32 + 2 + numChildren*CommitmentSize
	if node.Commitment != nil {
		size += CommitmentSize
	}

	buf := make([]byte, size)
	offset := 0

	// Type
	buf[offset] = byte(NodeTypeInternal)
	offset++

	// Bitmap (4 * 8 bytes = 32 bytes)
	for i := 0; i < 4; i++ {
		binary.BigEndian.PutUint64(buf[offset:offset+8], node.Bitmap[i])
		offset += 8
	}

	// NumChildren
	binary.BigEndian.PutUint16(buf[offset:offset+2], uint16(numChildren))
	offset += 2

	// Children
	for _, child := range node.Children {
		copy(buf[offset:offset+CommitmentSize], child)
		offset += CommitmentSize
	}

	// Commitment (if present)
	if node.Commitment != nil {
		copy(buf[offset:offset+CommitmentSize], node.Commitment)
	}

	return buf
}

// DecodeInternalNode256 反序列化内部节点
func DecodeInternalNode256(data []byte) (*InternalNode256, error) {
	if len(data) < 35 { // 1 + 32 + 2
		return nil, errors.New("internal node data too short")
	}
	if data[0] != byte(NodeTypeInternal) {
		return nil, errors.New("invalid node type for internal node")
	}

	offset := 1
	node := &InternalNode256{}

	// Bitmap
	for i := 0; i < 4; i++ {
		node.Bitmap[i] = binary.BigEndian.Uint64(data[offset : offset+8])
		offset += 8
	}

	// NumChildren
	numChildren := int(binary.BigEndian.Uint16(data[offset : offset+2]))
	offset += 2

	// Children
	expectedLen := offset + numChildren*CommitmentSize
	if len(data) < expectedLen {
		return nil, errors.New("internal node data too short for children")
	}

	node.Children = make([][]byte, numChildren)
	for i := 0; i < numChildren; i++ {
		node.Children[i] = make([]byte, CommitmentSize)
		copy(node.Children[i], data[offset:offset+CommitmentSize])
		offset += CommitmentSize
	}

	// Commitment (optional)
	if len(data) >= offset+CommitmentSize {
		node.Commitment = make([]byte, CommitmentSize)
		copy(node.Commitment, data[offset:offset+CommitmentSize])
	}

	return node, nil
}

// EncodeLeafNode 序列化叶子节点
// 格式: [Type 1B][Stem 31B][ValueBitmap 32B][NumValues 2B][Values...][Commitment 32B?]
func EncodeLeafNode(node *LeafNode) []byte {
	// 计算值位图和值数量
	var valueBitmap [4]uint64
	numValues := 0
	for i := 0; i < 256; i++ {
		if node.Values[i] != nil {
			wordIdx := i / 64
			bitIdx := i % 64
			valueBitmap[wordIdx] |= 1 << bitIdx
			numValues++
		}
	}

	// 计算总大小
	valuesSize := 0
	for i := 0; i < 256; i++ {
		if node.Values[i] != nil {
			valuesSize += 4 + len(node.Values[i]) // length prefix + value
		}
	}

	size := 1 + StemSize + 32 + 2 + valuesSize
	if node.Commitment != nil {
		size += CommitmentSize
	}

	buf := make([]byte, size)
	offset := 0

	// Type
	buf[offset] = byte(NodeTypeLeaf)
	offset++

	// Stem
	copy(buf[offset:offset+StemSize], node.Stem[:])
	offset += StemSize

	// Value Bitmap
	for i := 0; i < 4; i++ {
		binary.BigEndian.PutUint64(buf[offset:offset+8], valueBitmap[i])
		offset += 8
	}

	// NumValues
	binary.BigEndian.PutUint16(buf[offset:offset+2], uint16(numValues))
	offset += 2

	// Values (with length prefix)
	for i := 0; i < 256; i++ {
		if node.Values[i] != nil {
			binary.BigEndian.PutUint32(buf[offset:offset+4], uint32(len(node.Values[i])))
			offset += 4
			copy(buf[offset:offset+len(node.Values[i])], node.Values[i])
			offset += len(node.Values[i])
		}
	}

	// Commitment (if present)
	if node.Commitment != nil {
		copy(buf[offset:offset+CommitmentSize], node.Commitment)
	}

	return buf
}

// DecodeLeafNode 反序列化叶子节点
func DecodeLeafNode(data []byte) (*LeafNode, error) {
	if len(data) < 1+StemSize+32+2 {
		return nil, errors.New("leaf node data too short")
	}
	if data[0] != byte(NodeTypeLeaf) {
		return nil, errors.New("invalid node type for leaf node")
	}

	offset := 1
	node := &LeafNode{}

	// Stem
	copy(node.Stem[:], data[offset:offset+StemSize])
	offset += StemSize

	// Value Bitmap
	var valueBitmap [4]uint64
	for i := 0; i < 4; i++ {
		valueBitmap[i] = binary.BigEndian.Uint64(data[offset : offset+8])
		offset += 8
	}

	// NumValues
	numValues := int(binary.BigEndian.Uint16(data[offset : offset+2]))
	offset += 2

	// Values
	valuesRead := 0
	for i := 0; i < 256 && valuesRead < numValues; i++ {
		wordIdx := i / 64
		bitIdx := i % 64
		if valueBitmap[wordIdx]&(1<<bitIdx) != 0 {
			if offset+4 > len(data) {
				return nil, errors.New("leaf node data too short for value length")
			}
			valueLen := int(binary.BigEndian.Uint32(data[offset : offset+4]))
			offset += 4

			if offset+valueLen > len(data) {
				return nil, errors.New("leaf node data too short for value")
			}
			node.Values[i] = make([]byte, valueLen)
			copy(node.Values[i], data[offset:offset+valueLen])
			offset += valueLen
			valuesRead++
		}
	}

	// Commitment (optional)
	if len(data) >= offset+CommitmentSize {
		node.Commitment = make([]byte, CommitmentSize)
		copy(node.Commitment, data[offset:offset+CommitmentSize])
	}

	return node, nil
}

// GetNodeType 获取节点类型
func GetNodeType(data []byte) NodeType {
	if len(data) == 0 {
		return NodeTypeEmpty
	}
	return NodeType(data[0])
}

// IsInternalNodeData 检查是否为内部节点数据
func IsInternalNodeData(data []byte) bool {
	return len(data) > 0 && data[0] == byte(NodeTypeInternal)
}

// IsLeafNodeData 检查是否为叶子节点数据
func IsLeafNodeData(data []byte) bool {
	return len(data) > 0 && data[0] == byte(NodeTypeLeaf)
}
