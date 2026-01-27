package smt

import (
	"bytes"
	"errors"
	"hash"
)

// ============================================
// JMT 16 叉 Merkle Proof
// ============================================

// JMTProof 16 叉 JMT 的 Merkle 证明
type JMTProof struct {
	// Version 证明对应的版本
	Version Version

	// Key 被证明的 Key
	Key []byte

	// KeyHash Key 的哈希 (路径)
	KeyHash []byte

	// Value 被证明的值 (存在性证明时非空)
	Value []byte

	// Siblings 每层的兄弟节点信息
	// 从叶子到根的顺序
	Siblings []*SiblingInfo

	// LeafData 叶子节点数据 (存在性证明时非空)
	// 对于不存在性证明，可能包含路径上遇到的其他叶子
	LeafData []byte
}

// IsMembershipProof 检查是否为存在性证明
func (p *JMTProof) IsMembershipProof() bool {
	return p.Value != nil
}

// ============================================
// Proof 生成
// ============================================

// Prove 生成指定 Key 的 Merkle 证明
func (jmt *JellyfishMerkleTree) Prove(key []byte) (*JMTProof, error) {
	return jmt.ProveForRoot(key, jmt.Root(), jmt.Version())
}

// ProveForRoot 在指定根下生成 Merkle 证明
func (jmt *JellyfishMerkleTree) ProveForRoot(key []byte, root []byte, version Version) (*JMTProof, error) {
	jmt.mu.RLock()
	defer jmt.mu.RUnlock()

	path := jmt.hasher.Path(key)

	proof := &JMTProof{
		Version:  version,
		Key:      key,
		KeyHash:  path,
		Siblings: make([]*SiblingInfo, 0),
	}

	if jmt.hasher.IsPlaceholder(root) {
		// 空树，不存在性证明
		return proof, nil
	}

	currentHash := root
	var foundLeaf *LeafNode

	// 遍历树收集兄弟节点
	for depth := 0; depth < jmt.hasher.MaxDepth(); depth++ {
		nodeData, err := jmt.store.Get(currentHash, version)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				break
			}
			return nil, err
		}

		nodeType := jmt.hasher.GetNodeType(nodeData)

		switch nodeType {
		case NodeTypeLeaf:
			// 到达叶子节点
			leaf, err := jmt.hasher.ParseLeafNode(nodeData)
			if err != nil {
				return nil, err
			}
			proof.LeafData = nodeData
			foundLeaf = leaf
			// 退出循环
			depth = jmt.hasher.MaxDepth()

		case NodeTypeInternal:
			// 内部节点
			node, err := jmt.hasher.ParseInternalNode(nodeData)
			if err != nil {
				return nil, err
			}

			nibble := getNibbleAt(path, depth)

			// 提取兄弟节点信息
			siblingInfo := jmt.hasher.ExtractSiblings(node, nibble)
			proof.Siblings = append(proof.Siblings, siblingInfo)

			// 继续遍历
			childHash := node.GetChild(nibble)
			if childHash == nil || jmt.hasher.IsPlaceholder(childHash) {
				// 路径终止，不存在性证明
				break
			}
			currentHash = childHash

		default:
			return nil, errors.New("corrupted tree: unknown node type")
		}
	}

	// 检查是否匹配
	if foundLeaf != nil && bytes.Equal(foundLeaf.KeyHash, path) {
		// 存在性证明
		value, err := jmt.store.Get(valueKey(path), version)
		if err != nil {
			return nil, err
		}
		proof.Value = value
	}

	return proof, nil
}

// ============================================
// Proof 验证
// ============================================

// VerifyJMTProof 验证 JMT Merkle 证明
func VerifyJMTProof(proof *JMTProof, root []byte, hasher hash.Hash) bool {
	jh := NewJMTHasher(hasher)

	if jh.IsPlaceholder(root) {
		// 空树只能验证不存在性证明
		return !proof.IsMembershipProof()
	}

	path := proof.KeyHash

	var currentHash []byte

	if proof.IsMembershipProof() {
		// 存在性证明：从叶子开始向上计算
		valueHash := jh.Digest(proof.Value)
		leafHash, _ := jh.DigestLeafNode(path, valueHash)
		currentHash = leafHash
	} else {
		// 不存在性证明
		if proof.LeafData != nil {
			// 有其他叶子在路径上
			leaf, err := jh.ParseLeafNode(proof.LeafData)
			if err != nil {
				return false
			}
			// 确保这个叶子不是目标 Key
			if bytes.Equal(leaf.KeyHash, path) {
				return false // 存在但声称不存在
			}
			// 计算这个叶子的哈希
			currentHash = jh.Digest(proof.LeafData)
		} else {
			// 路径上没有叶子，使用 placeholder
			currentHash = jh.Placeholder()
		}
	}

	// 从叶子向根遍历
	for i := len(proof.Siblings) - 1; i >= 0; i-- {
		siblingInfo := proof.Siblings[i]
		depth := i
		nibble := getNibbleAt(path, depth)

		// 重建内部节点
		var children [16][]byte
		for j := byte(0); j < 16; j++ {
			children[j] = jh.Placeholder()
		}

		// 设置当前路径上的子节点
		children[nibble] = currentHash

		// 恢复兄弟节点
		sibIdx := 0
		for j := byte(0); j < 16; j++ {
			if j == nibble {
				continue
			}
			if siblingInfo.Bitmap&(1<<j) != 0 {
				if sibIdx >= len(siblingInfo.Siblings) {
					return false // 兄弟节点数量不匹配
				}
				children[j] = siblingInfo.Siblings[sibIdx]
				sibIdx++
			}
		}

		// 计算内部节点哈希
		nodeHash, _ := jh.DigestInternalNode(children)
		currentHash = nodeHash
	}

	return bytes.Equal(currentHash, root)
}

// ============================================
// Proof 序列化 (用于网络传输)
// ============================================

// EncodeSiblingInfo 序列化 SiblingInfo
func EncodeSiblingInfo(info *SiblingInfo, hashSize int) []byte {
	// 格式: [Bitmap 2 bytes][NumSiblings 1 byte][Siblings...]
	size := 2 + 1 + len(info.Siblings)*hashSize
	buf := make([]byte, size)

	buf[0] = byte(info.Bitmap >> 8)
	buf[1] = byte(info.Bitmap)
	buf[2] = byte(len(info.Siblings))

	offset := 3
	for _, sib := range info.Siblings {
		copy(buf[offset:offset+hashSize], sib)
		offset += hashSize
	}

	return buf
}

// DecodeSiblingInfo 反序列化 SiblingInfo
func DecodeSiblingInfo(data []byte, hashSize int) (*SiblingInfo, int, error) {
	if len(data) < 3 {
		return nil, 0, errors.New("sibling info too short")
	}

	bitmap := uint16(data[0])<<8 | uint16(data[1])
	numSiblings := int(data[2])

	expectedLen := 3 + numSiblings*hashSize
	if len(data) < expectedLen {
		return nil, 0, errors.New("sibling info data too short")
	}

	siblings := make([][]byte, numSiblings)
	offset := 3
	for i := 0; i < numSiblings; i++ {
		sib := make([]byte, hashSize)
		copy(sib, data[offset:offset+hashSize])
		siblings[i] = sib
		offset += hashSize
	}

	return &SiblingInfo{
		Bitmap:   bitmap,
		Siblings: siblings,
	}, expectedLen, nil
}

// ============================================
// 辅助方法
// ============================================

// ProofSize 返回证明的大约字节数
func (p *JMTProof) ProofSize(hashSize int) int {
	size := 8 // version
	size += len(p.Key)
	size += len(p.KeyHash)
	size += len(p.Value)
	size += len(p.LeafData)

	for _, sib := range p.Siblings {
		size += 3 + len(sib.Siblings)*hashSize
	}

	return size
}
