package verkle

import (
	"bytes"
	"errors"
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"
)

// ============================================
// Verkle Tree 核心实现
// 256 叉 + Pedersen 承诺
// ============================================

// VerkleTree 256 叉版本化 Verkle 树
type VerkleTree struct {
	store     VersionedStore
	committer *PedersenCommitter

	// 当前版本 (通常等于最新区块高度)
	version Version
	// 当前根承诺
	root []byte
	// 历史根承诺缓存 (version -> rootCommitment)
	rootHistory map[Version][]byte

	mu sync.RWMutex

	// 节点缓存 (commitment -> serialized node data)
	nodeCache *lru.Cache[string, []byte]
}

// NewVerkleTree 创建新的空 Verkle 树
func NewVerkleTree(store VersionedStore) *VerkleTree {
	committer := NewPedersenCommitter()
	cache, _ := lru.New[string, []byte](10000)
	return &VerkleTree{
		store:       store,
		committer:   committer,
		version:     0,
		root:        committer.ZeroCommitment(),
		rootHistory: make(map[Version][]byte),
		nodeCache:   cache,
	}
}

// ImportVerkleTree 从指定版本导入 Verkle 树
func ImportVerkleTree(store VersionedStore, version Version, root []byte) *VerkleTree {
	committer := NewPedersenCommitter()
	history := make(map[Version][]byte)
	history[version] = root
	cache, _ := lru.New[string, []byte](10000)
	return &VerkleTree{
		store:       store,
		committer:   committer,
		version:     version,
		root:        root,
		rootHistory: history,
		nodeCache:   cache,
	}
}

// ============================================
// 基本访问器
// ============================================

// Root 获取当前根承诺
func (t *VerkleTree) Root() []byte {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.root
}

// Version 获取当前版本
func (t *VerkleTree) Version() Version {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.version
}

// CommitRoot 更新树的根承诺和版本号
func (t *VerkleTree) CommitRoot(version Version, root []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.version = version
	t.root = root
	if t.rootHistory == nil {
		t.rootHistory = make(map[Version][]byte)
	}
	t.rootHistory[version] = root
}

// Committer 获取承诺器
func (t *VerkleTree) Committer() *PedersenCommitter {
	return t.committer
}

// ============================================
// 缓存操作
// ============================================

func (t *VerkleTree) setCache(commitment []byte, data []byte) {
	if t.nodeCache != nil {
		t.nodeCache.Add(string(commitment), data)
	}
}

func (t *VerkleTree) getCache(commitment []byte) []byte {
	if t.nodeCache != nil {
		if val, ok := t.nodeCache.Get(string(commitment)); ok {
			return val
		}
	}
	return nil
}

// ============================================
// Get 操作
// ============================================

// Get 获取指定版本的值
// version=0 表示当前版本
func (t *VerkleTree) Get(key []byte, version Version) ([]byte, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if version == 0 {
		version = t.version
	}

	rootCommitment, err := t.getRootForVersion(version)
	if err != nil {
		return nil, err
	}

	if t.isZeroCommitment(rootCommitment) {
		return nil, ErrNotFound
	}

	// 转换 key 为 32 字节
	verkleKey := ToVerkleKey(key)
	stem := GetStemFromKey(verkleKey)
	suffix := GetSuffixFromKey(verkleKey)

	return t.getForRoot(stem, suffix, rootCommitment, version, nil)
}

// GetWithSession 在会话中获取值
func (t *VerkleTree) GetWithSession(sess VersionedStoreSession, key []byte, version Version) ([]byte, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if version == 0 {
		version = t.version
	}

	rootCommitment, err := t.getRootForVersionWithSession(sess, version)
	if err != nil {
		return nil, err
	}

	if t.isZeroCommitment(rootCommitment) {
		return nil, ErrNotFound
	}

	verkleKey := ToVerkleKey(key)
	stem := GetStemFromKey(verkleKey)
	suffix := GetSuffixFromKey(verkleKey)

	return t.getForRoot(stem, suffix, rootCommitment, version, sess)
}

// getRootForVersion 获取指定版本的根承诺
func (t *VerkleTree) getRootForVersion(version Version) ([]byte, error) {
	if version == t.version {
		return t.root, nil
	}
	if root, ok := t.rootHistory[version]; ok {
		return root, nil
	}
	rootData, err := t.store.Get(rootKey(version), version)
	if err != nil {
		return nil, ErrVersionNotFound
	}
	return rootData, nil
}

func (t *VerkleTree) getRootForVersionWithSession(sess VersionedStoreSession, version Version) ([]byte, error) {
	if version == t.version {
		return t.root, nil
	}
	if root, ok := t.rootHistory[version]; ok {
		return root, nil
	}
	var rootData []byte
	var err error
	if sess != nil {
		rootData, err = sess.Get(rootKey(version), version)
	} else {
		rootData, err = t.store.Get(rootKey(version), version)
	}
	if err != nil {
		return nil, ErrVersionNotFound
	}
	return rootData, nil
}

// getForRoot 在指定根下获取值
func (t *VerkleTree) getForRoot(stem [StemSize]byte, suffix byte, rootCommitment []byte, version Version, sess VersionedStoreSession) ([]byte, error) {
	if t.isZeroCommitment(rootCommitment) {
		return nil, ErrNotFound
	}

	// 遍历树：最多 31 层内部节点 + 1 层叶子
	currentCommitment := rootCommitment

	for depth := 0; depth < StemSize; depth++ {
		// 获取节点数据
		nodeData := t.getCache(currentCommitment)
		var err error

		if nodeData == nil {
			if sess != nil {
				nodeData, err = sess.Get(currentCommitment, version)
			} else {
				nodeData, err = t.store.Get(currentCommitment, version)
			}
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					return nil, ErrNotFound
				}
				return nil, err
			}
			t.setCache(currentCommitment, nodeData)
		}

		nodeType := GetNodeType(nodeData)

		switch nodeType {
		case NodeTypeLeaf:
			leaf, err := DecodeLeafNode(nodeData)
			if err != nil {
				return nil, err
			}
			// 检查 stem 是否匹配
			if leaf.Stem != stem {
				return nil, ErrNotFound
			}
			// 获取值
			value := leaf.GetValue(suffix)
			if value == nil {
				return nil, ErrNotFound
			}
			return value, nil

		case NodeTypeInternal:
			node, err := DecodeInternalNode256(nodeData)
			if err != nil {
				return nil, err
			}
			// 使用 stem[depth] 作为索引
			childCommitment := node.GetChild(stem[depth])
			if childCommitment == nil || t.isZeroCommitment(childCommitment) {
				return nil, ErrNotFound
			}
			currentCommitment = childCommitment

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
func (t *VerkleTree) Update(keys [][]byte, values [][]byte, newVersion Version) ([]byte, error) {
	if len(keys) != len(values) {
		return nil, errors.New("keys and values must have the same length")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	currentRoot := t.root

	for i := 0; i < len(keys); i++ {
		var err error
		currentRoot, err = t.updateSingle(keys[i], values[i], currentRoot, newVersion, nil)
		if err != nil {
			return nil, err
		}
	}

	// 保存根承诺
	t.rootHistory[newVersion] = currentRoot
	if err := t.store.Set(rootKey(newVersion), currentRoot, newVersion); err != nil {
		return nil, err
	}

	t.root = currentRoot
	t.version = newVersion

	return currentRoot, nil
}

// UpdateWithSession 在会话中批量更新
func (t *VerkleTree) UpdateWithSession(sess VersionedStoreSession, keys [][]byte, values [][]byte, newVersion Version) ([]byte, error) {
	if len(keys) != len(values) {
		return nil, errors.New("keys and values must have the same length")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	currentRoot := t.root

	for i := 0; i < len(keys); i++ {
		var err error
		currentRoot, err = t.updateSingle(keys[i], values[i], currentRoot, newVersion, sess)
		if err != nil {
			return nil, err
		}
	}

	t.rootHistory[newVersion] = currentRoot
	if err := sess.Set(rootKey(newVersion), currentRoot, newVersion); err != nil {
		return nil, err
	}

	t.root = currentRoot
	t.version = newVersion

	return currentRoot, nil
}

// updateSingle 更新单个 Key-Value
func (t *VerkleTree) updateSingle(key, value []byte, rootCommitment []byte, version Version, sess VersionedStoreSession) ([]byte, error) {
	verkleKey := ToVerkleKey(key)
	stem := GetStemFromKey(verkleKey)
	suffix := GetSuffixFromKey(verkleKey)

	// 如果是空树，直接创建叶子节点
	if t.isZeroCommitment(rootCommitment) {
		leaf := NewLeafNode(stem)
		leaf.SetValue(suffix, value)

		// 计算叶子承诺
		leafCommitment, err := t.computeLeafCommitment(leaf)
		if err != nil {
			return nil, err
		}
		leaf.Commitment = leafCommitment

		// 保存叶子节点
		leafData := EncodeLeafNode(leaf)
		if sess != nil {
			err = sess.Set(leafCommitment, leafData, version)
		} else {
			err = t.store.Set(leafCommitment, leafData, version)
		}
		if err != nil {
			return nil, err
		}
		t.setCache(leafCommitment, leafData)

		return leafCommitment, nil
	}

	// 遍历树找到插入点
	return t.insertIntoTree(stem, suffix, value, rootCommitment, 0, version, sess)
}

// insertIntoTree 递归插入节点
func (t *VerkleTree) insertIntoTree(stem [StemSize]byte, suffix byte, value []byte, nodeCommitment []byte, depth int, version Version, sess VersionedStoreSession) ([]byte, error) {
	if t.isZeroCommitment(nodeCommitment) {
		// 空节点，创建叶子
		leaf := NewLeafNode(stem)
		leaf.SetValue(suffix, value)

		leafCommitment, err := t.computeLeafCommitment(leaf)
		if err != nil {
			return nil, err
		}
		leaf.Commitment = leafCommitment

		leafData := EncodeLeafNode(leaf)
		if sess != nil {
			err = sess.Set(leafCommitment, leafData, version)
		} else {
			err = t.store.Set(leafCommitment, leafData, version)
		}
		if err != nil {
			return nil, err
		}
		t.setCache(leafCommitment, leafData)

		return leafCommitment, nil
	}

	// 获取当前节点
	nodeData := t.getCache(nodeCommitment)
	var err error
	if nodeData == nil {
		if sess != nil {
			nodeData, err = sess.Get(nodeCommitment, version)
			if err != nil {
				nodeData, err = sess.Get(nodeCommitment, 0)
			}
		} else {
			nodeData, err = t.store.Get(nodeCommitment, version)
			if err != nil {
				nodeData, err = t.store.Get(nodeCommitment, 0)
			}
		}
	}
	if err != nil {
		return nil, err
	}

	nodeType := GetNodeType(nodeData)

	switch nodeType {
	case NodeTypeLeaf:
		existingLeaf, err := DecodeLeafNode(nodeData)
		if err != nil {
			return nil, err
		}

		if existingLeaf.Stem == stem {
			// 相同 stem，更新值
			existingLeaf.SetValue(suffix, value)

			newCommitment, err := t.computeLeafCommitment(existingLeaf)
			if err != nil {
				return nil, err
			}
			existingLeaf.Commitment = newCommitment

			newData := EncodeLeafNode(existingLeaf)
			if sess != nil {
				err = sess.Set(newCommitment, newData, version)
			} else {
				err = t.store.Set(newCommitment, newData, version)
			}
			if err != nil {
				return nil, err
			}
			t.setCache(newCommitment, newData)

			return newCommitment, nil
		}

		// 不同 stem，需要分裂
		return t.splitLeaf(existingLeaf, stem, suffix, value, depth, version, sess)

	case NodeTypeInternal:
		node, err := DecodeInternalNode256(nodeData)
		if err != nil {
			return nil, err
		}

		childIndex := stem[depth]
		childCommitment := node.GetChild(childIndex)
		if childCommitment == nil {
			childCommitment = t.committer.ZeroCommitment()
		}

		// 递归更新子树
		newChildCommitment, err := t.insertIntoTree(stem, suffix, value, childCommitment, depth+1, version, sess)
		if err != nil {
			return nil, err
		}

		// 更新内部节点
		node.SetChild(childIndex, newChildCommitment)

		// 重新计算承诺
		newNodeCommitment, err := t.computeInternalCommitment(node)
		if err != nil {
			return nil, err
		}
		node.Commitment = newNodeCommitment

		newNodeData := EncodeInternalNode256(node)
		if sess != nil {
			err = sess.Set(newNodeCommitment, newNodeData, version)
		} else {
			err = t.store.Set(newNodeCommitment, newNodeData, version)
		}
		if err != nil {
			return nil, err
		}
		t.setCache(newNodeCommitment, newNodeData)

		return newNodeCommitment, nil

	default:
		return nil, errors.New("corrupted tree: unknown node type")
	}
}

// splitLeaf 分裂叶子节点
func (t *VerkleTree) splitLeaf(existingLeaf *LeafNode, newStem [StemSize]byte, newSuffix byte, newValue []byte, depth int, version Version, sess VersionedStoreSession) ([]byte, error) {
	// 找到分叉点
	forkDepth := depth
	for forkDepth < StemSize && existingLeaf.Stem[forkDepth] == newStem[forkDepth] {
		forkDepth++
	}

	// 创建新叶子节点
	newLeaf := NewLeafNode(newStem)
	newLeaf.SetValue(newSuffix, newValue)
	newLeafCommitment, err := t.computeLeafCommitment(newLeaf)
	if err != nil {
		return nil, err
	}
	newLeaf.Commitment = newLeafCommitment

	newLeafData := EncodeLeafNode(newLeaf)
	if sess != nil {
		err = sess.Set(newLeafCommitment, newLeafData, version)
	} else {
		err = t.store.Set(newLeafCommitment, newLeafData, version)
	}
	if err != nil {
		return nil, err
	}
	t.setCache(newLeafCommitment, newLeafData)

	// 现有叶子也需要重新保存
	existingCommitment, err := t.computeLeafCommitment(existingLeaf)
	if err != nil {
		return nil, err
	}
	existingLeaf.Commitment = existingCommitment

	existingData := EncodeLeafNode(existingLeaf)
	if sess != nil {
		err = sess.Set(existingCommitment, existingData, version)
	} else {
		err = t.store.Set(existingCommitment, existingData, version)
	}
	if err != nil {
		return nil, err
	}
	t.setCache(existingCommitment, existingData)

	// 创建分叉点内部节点
	forkNode := NewInternalNode256()
	forkNode.SetChild(existingLeaf.Stem[forkDepth], existingCommitment)
	forkNode.SetChild(newStem[forkDepth], newLeafCommitment)

	forkCommitment, err := t.computeInternalCommitment(forkNode)
	if err != nil {
		return nil, err
	}
	forkNode.Commitment = forkCommitment

	forkData := EncodeInternalNode256(forkNode)
	if sess != nil {
		err = sess.Set(forkCommitment, forkData, version)
	} else {
		err = t.store.Set(forkCommitment, forkData, version)
	}
	if err != nil {
		return nil, err
	}
	t.setCache(forkCommitment, forkData)

	// 向上构建中间内部节点
	currentCommitment := forkCommitment
	for d := forkDepth - 1; d >= depth; d-- {
		middleNode := NewInternalNode256()
		middleNode.SetChild(newStem[d], currentCommitment)

		middleCommitment, err := t.computeInternalCommitment(middleNode)
		if err != nil {
			return nil, err
		}
		middleNode.Commitment = middleCommitment

		middleData := EncodeInternalNode256(middleNode)
		if sess != nil {
			err = sess.Set(middleCommitment, middleData, version)
		} else {
			err = t.store.Set(middleCommitment, middleData, version)
		}
		if err != nil {
			return nil, err
		}
		t.setCache(middleCommitment, middleData)

		currentCommitment = middleCommitment
	}

	return currentCommitment, nil
}

// ============================================
// Delete 操作
// ============================================

// Delete 删除指定 Key
func (t *VerkleTree) Delete(key []byte, newVersion Version) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.isZeroCommitment(t.root) {
		return t.root, nil
	}

	verkleKey := ToVerkleKey(key)
	stem := GetStemFromKey(verkleKey)
	suffix := GetSuffixFromKey(verkleKey)

	newRoot, err := t.deleteFromTree(stem, suffix, t.root, 0, newVersion, nil)
	if err != nil {
		return nil, err
	}

	t.rootHistory[newVersion] = newRoot
	if err := t.store.Set(rootKey(newVersion), newRoot, newVersion); err != nil {
		return nil, err
	}

	t.root = newRoot
	t.version = newVersion

	return newRoot, nil
}

// DeleteWithSession 在会话中删除
func (t *VerkleTree) DeleteWithSession(sess VersionedStoreSession, key []byte, newVersion Version) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.isZeroCommitment(t.root) {
		return t.root, nil
	}

	verkleKey := ToVerkleKey(key)
	stem := GetStemFromKey(verkleKey)
	suffix := GetSuffixFromKey(verkleKey)

	newRoot, err := t.deleteFromTree(stem, suffix, t.root, 0, newVersion, sess)
	if err != nil {
		return nil, err
	}

	t.rootHistory[newVersion] = newRoot
	if err := sess.Set(rootKey(newVersion), newRoot, newVersion); err != nil {
		return nil, err
	}

	t.root = newRoot
	t.version = newVersion

	return newRoot, nil
}

// deleteFromTree 递归删除
func (t *VerkleTree) deleteFromTree(stem [StemSize]byte, suffix byte, nodeCommitment []byte, depth int, version Version, sess VersionedStoreSession) ([]byte, error) {
	if t.isZeroCommitment(nodeCommitment) {
		return nodeCommitment, nil
	}

	var nodeData []byte
	var err error
	if sess != nil {
		nodeData, err = sess.Get(nodeCommitment, version)
		if err != nil {
			nodeData, err = sess.Get(nodeCommitment, 0)
		}
	} else {
		nodeData, err = t.store.Get(nodeCommitment, version)
		if err != nil {
			nodeData, err = t.store.Get(nodeCommitment, 0)
		}
	}
	if err != nil {
		return nil, err
	}

	nodeType := GetNodeType(nodeData)

	switch nodeType {
	case NodeTypeLeaf:
		leaf, err := DecodeLeafNode(nodeData)
		if err != nil {
			return nil, err
		}
		if leaf.Stem != stem {
			return nodeCommitment, nil
		}

		leaf.DeleteValue(suffix)

		if leaf.IsEmpty() {
			return t.committer.ZeroCommitment(), nil
		}

		newCommitment, err := t.computeLeafCommitment(leaf)
		if err != nil {
			return nil, err
		}
		leaf.Commitment = newCommitment

		newData := EncodeLeafNode(leaf)
		if sess != nil {
			err = sess.Set(newCommitment, newData, version)
		} else {
			err = t.store.Set(newCommitment, newData, version)
		}
		if err != nil {
			return nil, err
		}
		t.setCache(newCommitment, newData)

		return newCommitment, nil

	case NodeTypeInternal:
		node, err := DecodeInternalNode256(nodeData)
		if err != nil {
			return nil, err
		}

		childIndex := stem[depth]
		childCommitment := node.GetChild(childIndex)
		if childCommitment == nil || t.isZeroCommitment(childCommitment) {
			return nodeCommitment, nil
		}

		newChildCommitment, err := t.deleteFromTree(stem, suffix, childCommitment, depth+1, version, sess)
		if err != nil {
			return nil, err
		}

		if t.isZeroCommitment(newChildCommitment) {
			node.RemoveChild(childIndex)
		} else {
			node.SetChild(childIndex, newChildCommitment)
		}

		if node.IsEmpty() {
			return t.committer.ZeroCommitment(), nil
		}

		newCommitment, err := t.computeInternalCommitment(node)
		if err != nil {
			return nil, err
		}
		node.Commitment = newCommitment

		newData := EncodeInternalNode256(node)
		if sess != nil {
			err = sess.Set(newCommitment, newData, version)
		} else {
			err = t.store.Set(newCommitment, newData, version)
		}
		if err != nil {
			return nil, err
		}
		t.setCache(newCommitment, newData)

		return newCommitment, nil

	default:
		return nil, errors.New("corrupted tree: unknown node type")
	}
}

// ============================================
// 承诺计算
// ============================================

// computeLeafCommitment 计算叶子节点承诺（使用并行版本）
func (t *VerkleTree) computeLeafCommitment(leaf *LeafNode) ([]byte, error) {
	// 将值转换为向量
	var values [256][]byte
	for i := 0; i < 256; i++ {
		if leaf.Values[i] != nil {
			values[i] = leaf.Values[i]
		}
	}

	// 添加 stem 作为额外输入（确保不同 stem 产生不同承诺）
	// 这里简化处理：将 stem 哈希混入第一个值
	stemHash := ToVerkleKey(leaf.Stem[:])
	if values[0] == nil {
		values[0] = stemHash[:]
	} else {
		// XOR with stem
		result := make([]byte, 32)
		for i := 0; i < 32 && i < len(values[0]); i++ {
			result[i] = values[0][i] ^ stemHash[i]
		}
		values[0] = result
	}

	return t.committer.CommitToVectorParallel(values)
}

// computeInternalCommitment 计算内部节点承诺（使用并行版本）
func (t *VerkleTree) computeInternalCommitment(node *InternalNode256) ([]byte, error) {
	var values [256][]byte
	for i := 0; i < 256; i++ {
		if node.HasChild(byte(i)) {
			values[i] = node.GetChild(byte(i))
		}
	}
	return t.committer.CommitToVectorParallel(values)
}

// ============================================
// 辅助函数
// ============================================

// isZeroCommitment 检查是否为零承诺
func (t *VerkleTree) isZeroCommitment(commitment []byte) bool {
	zero := t.committer.ZeroCommitment()
	return bytes.Equal(commitment, zero)
}

// rootKey 生成根承诺的存储 Key
func rootKey(version Version) []byte {
	key := make([]byte, 9)
	key[0] = 'R' // Root prefix
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

// GetRootHash 获取指定版本的根承诺
func (t *VerkleTree) GetRootHash(version Version) ([]byte, error) {
	if version == 0 || version == t.version {
		return t.root, nil
	}
	if root, ok := t.rootHistory[version]; ok {
		return root, nil
	}
	return nil, ErrVersionNotFound
}

// Commit 提交当前状态
func (t *VerkleTree) Commit() error {
	return nil
}
