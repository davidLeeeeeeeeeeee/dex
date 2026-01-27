package smt

import (
	"bytes"
	"errors"
	"hash"
	"sync"
)

// ============================================
// Jellyfish Merkle Tree 16 叉版本化实现
// ============================================

// JellyfishMerkleTree 16 叉版本化 Merkle 树
type JellyfishMerkleTree struct {
	hasher *JMTHasher
	store  VersionedStore

	// 当前版本 (通常等于最新区块高度)
	version Version
	// 当前根哈希
	root []byte
	// 历史根哈希缓存 (version -> rootHash)
	rootHistory map[Version][]byte

	mu sync.RWMutex
}

// NewJMT 创建新的空 JMT
func NewJMT(store VersionedStore, hasher hash.Hash) *JellyfishMerkleTree {
	jh := NewJMTHasher(hasher)
	return &JellyfishMerkleTree{
		hasher:      jh,
		store:       store,
		version:     0,
		root:        jh.Placeholder(),
		rootHistory: make(map[Version][]byte),
	}
}

// ImportJMT 从指定版本导入 JMT
func ImportJMT(store VersionedStore, hasher hash.Hash, version Version, root []byte) *JellyfishMerkleTree {
	jh := NewJMTHasher(hasher)
	history := make(map[Version][]byte)
	history[version] = root
	return &JellyfishMerkleTree{
		hasher:      jh,
		store:       store,
		version:     version,
		root:        root,
		rootHistory: history,
	}
}

// ============================================
// 基本访问器
// ============================================

// Root 获取当前根哈希
func (jmt *JellyfishMerkleTree) Root() []byte {
	jmt.mu.RLock()
	defer jmt.mu.RUnlock()
	return jmt.root
}

// Version 获取当前版本
func (jmt *JellyfishMerkleTree) Version() Version {
	jmt.mu.RLock()
	defer jmt.mu.RUnlock()
	return jmt.version
}

// Hasher 获取哈希器
func (jmt *JellyfishMerkleTree) Hasher() *JMTHasher {
	return jmt.hasher
}

// ============================================
// Get 操作
// ============================================

// Get 获取指定版本的值
// version=0 表示当前版本
func (jmt *JellyfishMerkleTree) Get(key []byte, version Version) ([]byte, error) {
	jmt.mu.RLock()
	defer jmt.mu.RUnlock()

	if version == 0 {
		version = jmt.version
	}

	// 获取指定版本的根哈希
	rootHash, err := jmt.getRootForVersion(version)
	if err != nil {
		return nil, err
	}

	if jmt.hasher.IsPlaceholder(rootHash) {
		return nil, ErrNotFound
	}

	return jmt.getForRoot(key, rootHash, version)
}

// getRootForVersion 获取指定版本的根哈希
func (jmt *JellyfishMerkleTree) getRootForVersion(version Version) ([]byte, error) {
	if version == jmt.version {
		return jmt.root, nil
	}
	if root, ok := jmt.rootHistory[version]; ok {
		return root, nil
	}
	// 尝试从存储中获取
	rootData, err := jmt.store.Get(rootKey(version), version)
	if err != nil {
		return nil, ErrVersionNotFound
	}
	return rootData, nil
}

// getForRoot 在指定根下获取值
func (jmt *JellyfishMerkleTree) getForRoot(key []byte, root []byte, version Version) ([]byte, error) {
	if jmt.hasher.IsPlaceholder(root) {
		return nil, ErrNotFound
	}

	path := jmt.hasher.Path(key)
	currentHash := root

	// 遍历树
	for depth := 0; depth < jmt.hasher.MaxDepth(); depth++ {
		// 从存储中获取当前节点
		nodeData, err := jmt.store.Get(currentHash, version)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil, ErrNotFound
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
			// 检查是否匹配
			if bytes.Equal(leaf.KeyHash, path) {
				// 获取实际值
				return jmt.store.Get(valueKey(path), version)
			}
			// Key 不存在
			return nil, ErrNotFound

		case NodeTypeInternal:
			// 内部节点，继续遍历
			node, err := jmt.hasher.ParseInternalNode(nodeData)
			if err != nil {
				return nil, err
			}
			nibble := getNibbleAt(path, depth)
			childHash := node.GetChild(nibble)
			if childHash == nil || jmt.hasher.IsPlaceholder(childHash) {
				// 该路径不存在
				return nil, ErrNotFound
			}
			currentHash = childHash

		default:
			return nil, errors.New("corrupted tree: unknown node type")
		}
	}

	return nil, ErrNotFound
}

// ============================================
// Update 操作
// ============================================

// Update 在新版本中批量更新 Key-Value
// 返回新的根哈希
func (jmt *JellyfishMerkleTree) Update(keys [][]byte, values [][]byte, newVersion Version) ([]byte, error) {
	if len(keys) != len(values) {
		return nil, errors.New("keys and values must have the same length")
	}

	jmt.mu.Lock()
	defer jmt.mu.Unlock()

	currentRoot := jmt.root

	// 逐个更新
	for i := 0; i < len(keys); i++ {
		var err error
		currentRoot, err = jmt.updateSingle(keys[i], values[i], currentRoot, newVersion)
		if err != nil {
			return nil, err
		}
	}

	// 保存历史根哈希
	jmt.rootHistory[newVersion] = currentRoot
	// 也存到持久化存储
	if err := jmt.store.Set(rootKey(newVersion), currentRoot, newVersion); err != nil {
		return nil, err
	}

	jmt.root = currentRoot
	jmt.version = newVersion

	return currentRoot, nil
}

// updateSingle 更新单个 Key-Value
func (jmt *JellyfishMerkleTree) updateSingle(key, value []byte, root []byte, version Version) ([]byte, error) {
	path := jmt.hasher.Path(key)
	valueHash := jmt.hasher.Digest(value)

	// 存储实际值
	if err := jmt.store.Set(valueKey(path), value, version); err != nil {
		return nil, err
	}

	// 如果是空树，直接创建叶子节点
	if jmt.hasher.IsPlaceholder(root) {
		leafHash, leafData := jmt.hasher.DigestLeafNode(path, valueHash)
		if err := jmt.store.Set(leafHash, leafData, version); err != nil {
			return nil, err
		}
		return leafHash, nil
	}

	// 遍历树找到插入点
	return jmt.insertIntoTree(path, valueHash, root, 0, version)
}

// insertIntoTree 递归插入节点
func (jmt *JellyfishMerkleTree) insertIntoTree(path, valueHash, nodeHash []byte, depth int, version Version) ([]byte, error) {
	if jmt.hasher.IsPlaceholder(nodeHash) {
		// 到达空节点，创建叶子
		leafHash, leafData := jmt.hasher.DigestLeafNode(path, valueHash)
		if err := jmt.store.Set(leafHash, leafData, version); err != nil {
			return nil, err
		}
		return leafHash, nil
	}

	// 获取当前节点
	nodeData, err := jmt.store.Get(nodeHash, version)
	if err != nil {
		// 如果当前版本不存在，尝试旧版本
		nodeData, err = jmt.store.Get(nodeHash, 0)
		if err != nil {
			return nil, err
		}
	}

	nodeType := jmt.hasher.GetNodeType(nodeData)

	switch nodeType {
	case NodeTypeLeaf:
		// 遇到叶子节点
		existingLeaf, err := jmt.hasher.ParseLeafNode(nodeData)
		if err != nil {
			return nil, err
		}

		if bytes.Equal(existingLeaf.KeyHash, path) {
			// 更新现有叶子
			newLeafHash, newLeafData := jmt.hasher.DigestLeafNode(path, valueHash)
			if err := jmt.store.Set(newLeafHash, newLeafData, version); err != nil {
				return nil, err
			}
			return newLeafHash, nil
		}

		// 需要分裂：创建内部节点
		return jmt.splitLeaf(existingLeaf, path, valueHash, depth, version)

	case NodeTypeInternal:
		// 继续向下遍历
		node, err := jmt.hasher.ParseInternalNode(nodeData)
		if err != nil {
			return nil, err
		}

		nibble := getNibbleAt(path, depth)
		childHash := node.GetChild(nibble)
		if childHash == nil {
			childHash = jmt.hasher.Placeholder()
		}

		// 递归更新子树
		newChildHash, err := jmt.insertIntoTree(path, valueHash, childHash, depth+1, version)
		if err != nil {
			return nil, err
		}

		// 创建新的内部节点
		newNode := &InternalNode{
			ChildBitmap: node.ChildBitmap,
			Children:    make([][]byte, len(node.Children)),
		}
		copy(newNode.Children, node.Children)
		newNode.SetChild(nibble, newChildHash)

		newNodeHash, newNodeData := jmt.hasher.DigestInternalNodeFromNode(newNode)
		if err := jmt.store.Set(newNodeHash, newNodeData, version); err != nil {
			return nil, err
		}
		return newNodeHash, nil

	default:
		return nil, errors.New("corrupted tree: unknown node type")
	}
}

// splitLeaf 分裂叶子节点
func (jmt *JellyfishMerkleTree) splitLeaf(existingLeaf *LeafNode, newPath, newValueHash []byte, depth int, version Version) ([]byte, error) {
	existingPath := existingLeaf.KeyHash

	// 找到分叉点
	commonPrefix := countCommonNibblePrefix(existingPath, newPath)

	// 创建两个叶子节点
	existingLeafHash, existingLeafData := jmt.hasher.DigestLeafNode(existingPath, existingLeaf.ValueHash)
	if err := jmt.store.Set(existingLeafHash, existingLeafData, version); err != nil {
		return nil, err
	}

	newLeafHash, newLeafData := jmt.hasher.DigestLeafNode(newPath, newValueHash)
	if err := jmt.store.Set(newLeafHash, newLeafData, version); err != nil {
		return nil, err
	}

	// 从分叉点向上构建内部节点
	existingNibble := getNibbleAt(existingPath, commonPrefix)
	newNibble := getNibbleAt(newPath, commonPrefix)

	// 创建分叉点的内部节点
	var children [16][]byte
	for i := byte(0); i < 16; i++ {
		children[i] = jmt.hasher.Placeholder()
	}
	children[existingNibble] = existingLeafHash
	children[newNibble] = newLeafHash

	node := InternalNodeFromChildren(children, jmt.hasher.Placeholder())
	nodeHash, nodeData := jmt.hasher.DigestInternalNodeFromNode(node)
	if err := jmt.store.Set(nodeHash, nodeData, version); err != nil {
		return nil, err
	}

	// 如果分叉点比当前深度更深，需要创建中间节点
	currentHash := nodeHash
	for d := commonPrefix - 1; d >= depth; d-- {
		nibble := getNibbleAt(newPath, d)

		var children [16][]byte
		for i := byte(0); i < 16; i++ {
			children[i] = jmt.hasher.Placeholder()
		}
		children[nibble] = currentHash

		node := InternalNodeFromChildren(children, jmt.hasher.Placeholder())
		nodeHash, nodeData := jmt.hasher.DigestInternalNodeFromNode(node)
		if err := jmt.store.Set(nodeHash, nodeData, version); err != nil {
			return nil, err
		}
		currentHash = nodeHash
	}

	return currentHash, nil
}

// ============================================
// Delete 操作
// ============================================

// Delete 删除指定 Key
func (jmt *JellyfishMerkleTree) Delete(key []byte, newVersion Version) ([]byte, error) {
	jmt.mu.Lock()
	defer jmt.mu.Unlock()

	path := jmt.hasher.Path(key)

	if jmt.hasher.IsPlaceholder(jmt.root) {
		return jmt.root, nil // 空树，无需删除
	}

	newRoot, err := jmt.deleteFromTree(path, jmt.root, 0, newVersion)
	if err != nil {
		return nil, err
	}

	// 标记值为已删除
	if err := jmt.store.Delete(valueKey(path), newVersion); err != nil {
		return nil, err
	}

	// 保存历史根哈希
	jmt.rootHistory[newVersion] = newRoot
	if err := jmt.store.Set(rootKey(newVersion), newRoot, newVersion); err != nil {
		return nil, err
	}

	jmt.root = newRoot
	jmt.version = newVersion

	return newRoot, nil
}

// deleteFromTree 递归删除节点
func (jmt *JellyfishMerkleTree) deleteFromTree(path, nodeHash []byte, depth int, version Version) ([]byte, error) {
	if jmt.hasher.IsPlaceholder(nodeHash) {
		return nodeHash, nil // Key 不存在
	}

	nodeData, err := jmt.store.Get(nodeHash, version)
	if err != nil {
		nodeData, err = jmt.store.Get(nodeHash, 0)
		if err != nil {
			return nil, err
		}
	}

	nodeType := jmt.hasher.GetNodeType(nodeData)

	switch nodeType {
	case NodeTypeLeaf:
		leaf, err := jmt.hasher.ParseLeafNode(nodeData)
		if err != nil {
			return nil, err
		}
		if bytes.Equal(leaf.KeyHash, path) {
			// 找到目标叶子，返回 placeholder
			return jmt.hasher.Placeholder(), nil
		}
		// 不是目标，保持不变
		return nodeHash, nil

	case NodeTypeInternal:
		node, err := jmt.hasher.ParseInternalNode(nodeData)
		if err != nil {
			return nil, err
		}

		nibble := getNibbleAt(path, depth)
		childHash := node.GetChild(nibble)
		if childHash == nil || jmt.hasher.IsPlaceholder(childHash) {
			return nodeHash, nil // Key 不存在
		}

		// 递归删除
		newChildHash, err := jmt.deleteFromTree(path, childHash, depth+1, version)
		if err != nil {
			return nil, err
		}

		// 更新内部节点
		newNode := &InternalNode{
			ChildBitmap: node.ChildBitmap,
			Children:    make([][]byte, len(node.Children)),
		}
		copy(newNode.Children, node.Children)

		if jmt.hasher.IsPlaceholder(newChildHash) {
			newNode.RemoveChild(nibble)
		} else {
			newNode.SetChild(nibble, newChildHash)
		}

		// 如果只剩一个子节点且是叶子，则提升
		if newNode.ChildCount() == 1 {
			children := newNode.GetAllChildren()
			onlyChildHash := children[0][1].([]byte)
			childData, err := jmt.store.Get(onlyChildHash, version)
			if err != nil {
				childData, err = jmt.store.Get(onlyChildHash, 0)
			}
			if err == nil && IsLeafNodeData(childData) {
				return onlyChildHash, nil
			}
		}

		if newNode.IsEmpty() {
			return jmt.hasher.Placeholder(), nil
		}

		newNodeHash, newNodeData := jmt.hasher.DigestInternalNodeFromNode(newNode)
		if err := jmt.store.Set(newNodeHash, newNodeData, version); err != nil {
			return nil, err
		}
		return newNodeHash, nil

	default:
		return nil, errors.New("corrupted tree: unknown node type")
	}
}

// ============================================
// 辅助函数
// ============================================

// valueKey 生成值的存储 Key
func valueKey(path []byte) []byte {
	// 使用前缀区分节点和值
	key := make([]byte, len(path)+1)
	key[0] = 'V' // Value prefix
	copy(key[1:], path)
	return key
}

// rootKey 生成根哈希的存储 Key
func rootKey(version Version) []byte {
	key := make([]byte, 9)
	key[0] = 'R' // Root prefix
	// Big-endian version
	key[1] = byte(version >> 56)
	key[2] = byte(version >> 48)
	key[3] = byte(version >> 40)
	key[4] = byte(version >> 32)
	key[5] = byte(version >> 24)
	key[6] = byte(version >> 16)
	key[7] = byte(version >> 8)
	key[8] = byte(version)
	return key
}

// GetRootHash 获取指定版本的根哈希
func (jmt *JellyfishMerkleTree) GetRootHash(version Version) ([]byte, error) {
	if version == 0 || version == jmt.version {
		return jmt.root, nil
	}
	// 实际实现需要从存储中获取历史根哈希
	return nil, ErrVersionNotFound
}

// Commit 提交当前状态 (可用于刷盘等操作)
func (jmt *JellyfishMerkleTree) Commit() error {
	// 当前实现中，所有写入都是即时的
	// 如果需要批量提交，可以在这里实现
	return nil
}
