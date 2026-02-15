package smt

import (
	"bytes"
	"errors"
	"hash"
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"
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

	// 节点缓存 (不可变节点)
	nodeCache *lru.Cache[string, []byte]
}

// NewJMT 创建新的空 JMT
func NewJMT(store VersionedStore, hasher hash.Hash) *JellyfishMerkleTree {
	jh := NewJMTHasher(hasher)
	cache, _ := lru.New[string, []byte](10000)
	return &JellyfishMerkleTree{
		hasher:      jh,
		store:       store,
		version:     0,
		root:        jh.Placeholder(),
		rootHistory: make(map[Version][]byte),
		nodeCache:   cache,
	}
}

// setCache 设置缓存
func (jmt *JellyfishMerkleTree) setCache(hash []byte, data []byte) {
	if jmt.nodeCache != nil {
		jmt.nodeCache.Add(string(hash), data)
	}
}

// getCache 获取缓存
func (jmt *JellyfishMerkleTree) getCache(hash []byte) []byte {
	if jmt.nodeCache != nil {
		if val, ok := jmt.nodeCache.Get(string(hash)); ok {
			return val
		}
	}
	return nil
}

// ImportJMT 从指定版本导入 JMT
func ImportJMT(store VersionedStore, hasher hash.Hash, version Version, root []byte) *JellyfishMerkleTree {
	jh := NewJMTHasher(hasher)
	history := make(map[Version][]byte)
	history[version] = root
	cache, _ := lru.New[string, []byte](10000)
	return &JellyfishMerkleTree{
		hasher:      jh,
		store:       store,
		version:     version,
		root:        root,
		rootHistory: history,
		nodeCache:   cache,
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

// CommitRoot 更新树的根哈希和版本号（通常在会话提交后调用）
func (jmt *JellyfishMerkleTree) CommitRoot(version Version, root []byte) {
	jmt.mu.Lock()
	defer jmt.mu.Unlock()
	jmt.version = version
	jmt.root = root
	if jmt.rootHistory == nil {
		jmt.rootHistory = make(map[Version][]byte)
	}
	jmt.rootHistory[version] = root
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

	return jmt.getForRoot(key, rootHash, version, nil)
}

// GetWithSession 在会话中获取指定版本的值
func (jmt *JellyfishMerkleTree) GetWithSession(sess VersionedStoreSession, key []byte, version Version) ([]byte, error) {
	jmt.mu.RLock()
	defer jmt.mu.RUnlock()

	if version == 0 {
		version = jmt.version
	}

	rootHash, err := jmt.getRootForVersionWithSession(sess, version)
	if err != nil {
		return nil, err
	}

	if jmt.hasher.IsPlaceholder(rootHash) {
		return nil, ErrNotFound
	}

	return jmt.getForRoot(key, rootHash, version, sess)
}

// getRootForVersionWithSession 获取指定版本的根哈希
func (jmt *JellyfishMerkleTree) getRootForVersionWithSession(sess VersionedStoreSession, version Version) ([]byte, error) {
	if version == jmt.version {
		return jmt.root, nil
	}
	if root, ok := jmt.rootHistory[version]; ok {
		return root, nil
	}

	// 尝试从存储中获取
	var rootData []byte
	var err error
	if sess != nil {
		rootData, err = sess.Get(rootKey(version), version)
		if err != nil {
			// Backward compatibility for legacy root key format ('R'+version).
			rootData, err = sess.Get(legacyRootKey(version), version)
		}
	} else {
		rootData, err = jmt.store.Get(rootKey(version), version)
		if err != nil {
			// Backward compatibility for legacy root key format ('R'+version).
			rootData, err = jmt.store.Get(legacyRootKey(version), version)
		}
	}

	if err != nil {
		return nil, ErrVersionNotFound
	}
	return rootData, nil
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
		// Backward compatibility for legacy root key format ('R'+version).
		rootData, err = jmt.store.Get(legacyRootKey(version), version)
	}
	if err != nil {
		return nil, ErrVersionNotFound
	}
	return rootData, nil
}

// getForRoot 在指定根下获取值
func (jmt *JellyfishMerkleTree) getForRoot(key []byte, root []byte, version Version, sess VersionedStoreSession) ([]byte, error) {
	if jmt.hasher.IsPlaceholder(root) {
		return nil, ErrNotFound
	}

	path := jmt.hasher.Path(key)
	currentHash := root

	// 遍历树
	for depth := 0; depth < jmt.hasher.MaxDepth(); depth++ {
		// 尝试从缓存获取
		nodeData := jmt.getCache(currentHash)
		var err error

		if nodeData == nil {
			// 从存储中获取当前节点
			if sess != nil {
				nodeData, err = sess.Get(currentHash, version)
			} else {
				nodeData, err = jmt.store.Get(currentHash, version)
			}

			if err != nil {
				if errors.Is(err, ErrNotFound) {
					return nil, ErrNotFound
				}
				return nil, err
			}
			// 写入缓存
			jmt.setCache(currentHash, nodeData)
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
				if sess != nil {
					return sess.Get(valueKey(path), version)
				}
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

	// 1. 逐个更新（保持在内存中累推新根）
	for i := 0; i < len(keys); i++ {
		var err error
		currentRoot, err = jmt.updateSingle(keys[i], values[i], currentRoot, newVersion, nil)
		if err != nil {
			return nil, err
		}
	}

	// 2. 更新全局状态历史
	jmt.rootHistory[newVersion] = currentRoot

	// 3. 将新根哈希持久化（使用 rootKey 规则）
	if err := jmt.store.Set(rootKey(newVersion), currentRoot, newVersion); err != nil {
		return nil, err
	}

	// 4. 原子更新当前内存根和版本号
	jmt.root = currentRoot
	jmt.version = newVersion

	return currentRoot, nil
}

// UpdateWithSession 在会话中批量更新 Key-Value
func (jmt *JellyfishMerkleTree) UpdateWithSession(sess VersionedStoreSession, keys [][]byte, values [][]byte, newVersion Version) ([]byte, error) {
	if len(keys) != len(values) {
		return nil, errors.New("keys and values must have the same length")
	}

	jmt.mu.Lock()
	defer jmt.mu.Unlock()

	currentRoot := jmt.root

	for i := 0; i < len(keys); i++ {
		var err error
		currentRoot, err = jmt.updateSingle(keys[i], values[i], currentRoot, newVersion, sess)
		if err != nil {
			return nil, err
		}
	}

	jmt.rootHistory[newVersion] = currentRoot
	if err := sess.Set(rootKey(newVersion), currentRoot, newVersion); err != nil {
		return nil, err
	}

	jmt.root = currentRoot
	jmt.version = newVersion

	return currentRoot, nil
}

// updateSingle 更新单个 Key-Value
func (jmt *JellyfishMerkleTree) updateSingle(key, value []byte, root []byte, version Version, sess VersionedStoreSession) ([]byte, error) {
	path := jmt.hasher.Path(key)
	valueHash := jmt.hasher.Digest(value)

	// 存储实际值
	var err error
	if sess != nil {
		err = sess.Set(valueKey(path), value, version)
	} else {
		err = jmt.store.Set(valueKey(path), value, version)
	}
	if err != nil {
		return nil, err
	}

	// 如果是空树，直接创建叶子节点
	if jmt.hasher.IsPlaceholder(root) {
		leafHash, leafData := jmt.hasher.DigestLeafNode(path, valueHash)
		if sess != nil {
			err = sess.Set(leafHash, leafData, version)
		} else {
			err = jmt.store.Set(leafHash, leafData, version)
		}
		if err != nil {
			return nil, err
		}
		return leafHash, nil
	}

	// 遍历树找到插入点
	return jmt.insertIntoTree(path, valueHash, root, 0, version, sess)
}

// insertIntoTree 递归插入节点
func (jmt *JellyfishMerkleTree) insertIntoTree(path, valueHash, nodeHash []byte, depth int, version Version, sess VersionedStoreSession) ([]byte, error) {
	if jmt.hasher.IsPlaceholder(nodeHash) {
		// 到达空节点，创建叶子
		leafHash, leafData := jmt.hasher.DigestLeafNode(path, valueHash)
		var err error
		if sess != nil {
			err = sess.Set(leafHash, leafData, version)
		} else {
			err = jmt.store.Set(leafHash, leafData, version)
		}
		if err != nil {
			return nil, err
		}
		return leafHash, nil
	}

	// 获取当前节点
	var nodeData []byte
	var err error
	if sess != nil {
		nodeData, err = sess.Get(nodeHash, version)
		if err != nil {
			// 如果当前版本不存在，尝试旧版本
			nodeData, err = sess.Get(nodeHash, 0)
		}
	} else {
		nodeData, err = jmt.store.Get(nodeHash, version)
		if err != nil {
			nodeData, err = jmt.store.Get(nodeHash, 0)
		}
	}

	if err != nil {
		return nil, err
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
			var err error
			if sess != nil {
				err = sess.Set(newLeafHash, newLeafData, version)
			} else {
				err = jmt.store.Set(newLeafHash, newLeafData, version)
			}
			if err != nil {
				return nil, err
			}
			return newLeafHash, nil
		}

		// 需要分裂：创建内部节点
		return jmt.splitLeaf(existingLeaf, path, valueHash, depth, version, sess)

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
		newChildHash, err := jmt.insertIntoTree(path, valueHash, childHash, depth+1, version, sess)
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
		if sess != nil {
			err = sess.Set(newNodeHash, newNodeData, version)
		} else {
			err = jmt.store.Set(newNodeHash, newNodeData, version)
		}
		if err != nil {
			return nil, err
		}
		return newNodeHash, nil

	default:
		return nil, errors.New("corrupted tree: unknown node type")
	}
}

// splitLeaf 分裂叶子节点
func (jmt *JellyfishMerkleTree) splitLeaf(existingLeaf *LeafNode, newPath, newValueHash []byte, depth int, version Version, sess VersionedStoreSession) ([]byte, error) {
	existingPath := existingLeaf.KeyHash

	// 找到分叉点
	commonPrefix := countCommonNibblePrefix(existingPath, newPath)

	// 创建两个叶子节点
	existingLeafHash, existingLeafData := jmt.hasher.DigestLeafNode(existingPath, existingLeaf.ValueHash)
	var err error
	if sess != nil {
		err = sess.Set(existingLeafHash, existingLeafData, version)
	} else {
		err = jmt.store.Set(existingLeafHash, existingLeafData, version)
	}
	if err != nil {
		return nil, err
	}

	newLeafHash, newLeafData := jmt.hasher.DigestLeafNode(newPath, newValueHash)
	if sess != nil {
		err = sess.Set(newLeafHash, newLeafData, version)
	} else {
		err = jmt.store.Set(newLeafHash, newLeafData, version)
	}
	if err != nil {
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
	if sess != nil {
		err = sess.Set(nodeHash, nodeData, version)
	} else {
		err = jmt.store.Set(nodeHash, nodeData, version)
	}
	if err != nil {
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
		if sess != nil {
			err = sess.Set(nodeHash, nodeData, version)
		} else {
			err = jmt.store.Set(nodeHash, nodeData, version)
		}
		if err != nil {
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

	newRoot, err := jmt.deleteFromTree(path, jmt.root, 0, newVersion, nil)
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

// DeleteWithSession 在会话中删除指定 Key
func (jmt *JellyfishMerkleTree) DeleteWithSession(sess VersionedStoreSession, key []byte, newVersion Version) ([]byte, error) {
	jmt.mu.Lock()
	defer jmt.mu.Unlock()

	path := jmt.hasher.Path(key)

	if jmt.hasher.IsPlaceholder(jmt.root) {
		return jmt.root, nil
	}

	newRoot, err := jmt.deleteFromTree(path, jmt.root, 0, newVersion, sess)
	if err != nil {
		return nil, err
	}

	if err := sess.Delete(valueKey(path), newVersion); err != nil {
		return nil, err
	}

	jmt.rootHistory[newVersion] = newRoot
	if err := sess.Set(rootKey(newVersion), newRoot, newVersion); err != nil {
		return nil, err
	}

	jmt.root = newRoot
	jmt.version = newVersion

	return newRoot, nil
}

// deleteBatchWithSession 批量删除多个 key，可选择是否在末尾持久化 root。
// 当后续还有更新要在同一 version 写入时，可设置 persistRoot=false 来避免 root 重复写。
func (jmt *JellyfishMerkleTree) deleteBatchWithSession(
	sess VersionedStoreSession,
	keys [][]byte,
	newVersion Version,
	persistRoot bool,
) ([]byte, error) {
	jmt.mu.Lock()
	defer jmt.mu.Unlock()

	currentRoot := jmt.root
	if len(keys) == 0 {
		if persistRoot {
			if err := sess.Set(rootKey(newVersion), currentRoot, newVersion); err != nil {
				return nil, err
			}
		}
		jmt.rootHistory[newVersion] = currentRoot
		jmt.root = currentRoot
		jmt.version = newVersion
		return currentRoot, nil
	}

	seenPath := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		path := jmt.hasher.Path(key)
		pathStr := string(path)
		if _, ok := seenPath[pathStr]; ok {
			continue
		}
		seenPath[pathStr] = struct{}{}

		if !jmt.hasher.IsPlaceholder(currentRoot) {
			newRoot, err := jmt.deleteFromTree(path, currentRoot, 0, newVersion, sess)
			if err != nil {
				return nil, err
			}
			currentRoot = newRoot
		}

		if err := sess.Delete(valueKey(path), newVersion); err != nil {
			return nil, err
		}
	}

	jmt.rootHistory[newVersion] = currentRoot
	if persistRoot {
		if err := sess.Set(rootKey(newVersion), currentRoot, newVersion); err != nil {
			return nil, err
		}
	}
	jmt.root = currentRoot
	jmt.version = newVersion

	return currentRoot, nil
}

// DeleteBatchWithSession 在同一会话内批量删除并持久化一次 root。
func (jmt *JellyfishMerkleTree) DeleteBatchWithSession(sess VersionedStoreSession, keys [][]byte, newVersion Version) ([]byte, error) {
	return jmt.deleteBatchWithSession(sess, keys, newVersion, true)
}

// deleteFromTree 递归删除节点
func (jmt *JellyfishMerkleTree) deleteFromTree(path, nodeHash []byte, depth int, version Version, sess VersionedStoreSession) ([]byte, error) {
	if jmt.hasher.IsPlaceholder(nodeHash) {
		return nodeHash, nil // Key 不存在
	}

	var nodeData []byte
	var err error
	if sess != nil {
		nodeData, err = sess.Get(nodeHash, version)
		if err != nil {
			nodeData, err = sess.Get(nodeHash, 0)
		}
	} else {
		nodeData, err = jmt.store.Get(nodeHash, version)
		if err != nil {
			nodeData, err = jmt.store.Get(nodeHash, 0)
		}
	}

	if err != nil {
		return nil, err
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
		newChildHash, err := jmt.deleteFromTree(path, childHash, depth+1, version, sess)
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
			var childDataInner []byte
			var errInner error
			if sess != nil {
				childDataInner, errInner = sess.Get(onlyChildHash, version)
				if errInner != nil {
					childDataInner, errInner = sess.Get(onlyChildHash, 0)
				}
			} else {
				childDataInner, errInner = jmt.store.Get(onlyChildHash, version)
				if errInner != nil {
					childDataInner, errInner = jmt.store.Get(onlyChildHash, 0)
				}
			}
			if errInner == nil && IsLeafNodeData(childDataInner) {
				return onlyChildHash, nil
			}
		}

		if newNode.IsEmpty() {
			return jmt.hasher.Placeholder(), nil
		}

		resultHash, newNodeData := jmt.hasher.DigestInternalNodeFromNode(newNode)
		var errSet error
		if sess != nil {
			errSet = sess.Set(resultHash, newNodeData, version)
		} else {
			errSet = jmt.store.Set(resultHash, newNodeData, version)
		}
		if errSet != nil {
			return nil, errSet
		}
		jmt.setCache(resultHash, newNodeData)
		return resultHash, nil

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
	key := make([]byte, 14)
	copy(key, "root:v")
	// Big-endian version
	key[6] = byte(version >> 56)
	key[7] = byte(version >> 48)
	key[8] = byte(version >> 40)
	key[9] = byte(version >> 32)
	key[10] = byte(version >> 24)
	key[11] = byte(version >> 16)
	key[12] = byte(version >> 8)
	key[13] = byte(version)
	return key
}

// legacyRootKey 兼容旧版本 root key 格式: 'R'+8-byte version。
func legacyRootKey(version Version) []byte {
	key := make([]byte, 9)
	key[0] = 'R'
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
