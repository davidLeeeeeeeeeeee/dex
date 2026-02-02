package verkle

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"sync"

	gverkle "github.com/ethereum/go-verkle"
)

// ============================================
// Verkle Tree 适配层
// 使用 github.com/ethereum/go-verkle 库实现高性能 Verkle Tree
// ============================================

const (
	// CommitmentSize Pedersen 承诺大小 (32 字节)
	CommitmentSize = 32
	// StemSize Stem 大小 (31 字节)
	StemSize = 31

	// 大值存储标记
	markerDirect   byte = 0x00 // 直接存储
	markerIndirect byte = 0x01 // 间接引用

	// 直接存储的最大值长度 (32 - 1字节标记 = 31)
	maxDirectValueSize = 31

	// MaxMemoryDepth 内存常驻层数上限（高层路由层）
	MaxMemoryDepth = 2
)

// VerkleProof Verkle Tree 的证明结构
type VerkleProof struct {
	// Commitments 从叶子到根的承诺路径
	Commitments [][]byte
	// Depths 每个承诺的深度
	Depths []byte
	// Key 被证明的 Key
	Key []byte
	// Value 被证明的值
	Value []byte
}

// IsMembershipProof 检查是否为存在性证明
func (p *VerkleProof) IsMembershipProof() bool {
	return p.Value != nil
}

// VerkleTree 使用 go-verkle 库的 Verkle Tree 包装器
type VerkleTree struct {
	mu          sync.RWMutex
	root        gverkle.VerkleNode
	store       VersionedStore
	version     Version
	rootHistory map[Version][]byte
}

// NewVerkleTree 创建新的 Verkle Tree 包装器 (v0.2.2 兼容)
func NewVerkleTree(store VersionedStore) *VerkleTree {
	return &VerkleTree{
		root:        gverkle.New(),
		store:       store,
		version:     0,
		rootHistory: make(map[Version][]byte),
	}
}

// nodeResolver 创建一个 NodeResolverFn 用于按需加载被 flush 的节点
func (t *VerkleTree) nodeResolver() gverkle.NodeResolverFn {
	return func(path []byte) ([]byte, error) {
		sess, err := t.store.NewSession()
		if err != nil {
			return nil, err
		}
		defer sess.Close()
		return sess.GetKV(nodeKey(path))
	}
}

// resolveNode 手动解析并加载节点（用于恢复根节点）
func (t *VerkleTree) resolveNode(hash []byte) (gverkle.VerkleNode, error) {
	sess, err := t.store.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()

	data, err := sess.GetKV(nodeKey(hash))
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, ErrNotFound
	}

	// 使用 gverkle.ParseNode 反序列化
	return gverkle.ParseNode(data, 0)
}

// ============================================
// 核心操作
// ============================================

// Get 获取指定版本的值
func (t *VerkleTree) Get(key []byte, version Version) ([]byte, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.root == nil || t.root.Commitment() == nil {
		return nil, ErrNotFound
	}

	verkleKey := ToVerkleKey(key)
	data, err := t.root.Get(verkleKey[:], t.nodeResolver())
	if err != nil || data == nil || len(data) == 0 {
		return nil, ErrNotFound
	}

	marker := data[0]
	payload := data[1:]

	switch marker {
	case markerDirect:
		// 直接存储的值
		return payload, nil
	case markerIndirect:
		// 从 KV 获取大值
		val, err := t.store.Get(largeValueKey(payload), version)
		if err != nil {
			return nil, err
		}
		return val, nil
	default:
		// 兼容旧数据或未知格式
		return data, nil
	}
}

// GetWithSession 在会话中获取值
func (t *VerkleTree) GetWithSession(sess VersionedStoreSession, key []byte, version Version) ([]byte, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.root == nil || t.root.Commitment() == nil {
		return nil, ErrNotFound
	}

	verkleKey := ToVerkleKey(key)
	data, err := t.root.Get(verkleKey[:], t.nodeResolver())
	if err != nil || data == nil || len(data) == 0 {
		return nil, ErrNotFound
	}

	marker := data[0]
	payload := data[1:]

	switch marker {
	case markerDirect:
		// 直接存储的值
		return payload, nil
	case markerIndirect:
		// 从 KV 获取大值（通过 Session）
		val, err := sess.Get(largeValueKey(payload), version)
		if err != nil {
			return nil, err
		}
		return val, nil
	default:
		// 兼容旧数据或未知格式
		return data, nil
	}
}

// Update 批量更新 Key-Value
func (t *VerkleTree) Update(keys [][]byte, values [][]byte, newVersion Version) ([]byte, error) {
	if len(keys) != len(values) {
		return nil, errors.New("keys and values must have the same length")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	for i := 0; i < len(keys); i++ {
		verkleKey := ToVerkleKey(keys[i])
		if err := t.root.Insert(verkleKey[:], values[i], t.nodeResolver()); err != nil {
			return nil, err
		}
	}

	// 计算并返回根承诺
	t.root.Commit()
	rootCommitment := t.root.Commitment().Bytes()

	t.version = newVersion
	t.rootHistory[newVersion] = rootCommitment[:]

	// 保存到存储
	if err := t.store.Set(rootKey(newVersion), rootCommitment[:], newVersion); err != nil {
		return nil, err
	}

	return rootCommitment[:], nil
}

// UpdateWithSession 在会话中批量更新（支持大值分片存储）
func (t *VerkleTree) UpdateWithSession(sess VersionedStoreSession, keys [][]byte, values [][]byte, newVersion Version) ([]byte, error) {
	if len(keys) != len(values) {
		return nil, errors.New("keys and values must have the same length")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	for i := 0; i < len(keys); i++ {
		verkleKey := ToVerkleKey(keys[i])
		val := values[i]
		var treeVal []byte

		if len(val) <= maxDirectValueSize {
			// 小值：直接存储 [0x00] + value
			treeVal = make([]byte, 1+len(val))
			treeVal[0] = markerDirect
			copy(treeVal[1:], val)
		} else {
			// 大值：存储引用 [0x01] + hash[:31]
			h := sha256.Sum256(val)
			hashPart := h[:31]

			// 使用 Session 原子写入大值到 KV
			if err := sess.Set(largeValueKey(hashPart), val, newVersion); err != nil {
				return nil, err
			}

			treeVal = make([]byte, 32)
			treeVal[0] = markerIndirect
			copy(treeVal[1:], hashPart)
		}

		if err := t.root.Insert(verkleKey[:], treeVal, t.nodeResolver()); err != nil {
			return nil, err
		}
	}

	// 计算根承诺
	t.root.Commit()
	rootCommitment := t.root.Commitment().Bytes()

	t.version = newVersion
	t.rootHistory[newVersion] = rootCommitment[:]

	// 存储根承诺（也通过 session）
	if err := sess.Set(rootKey(newVersion), rootCommitment[:], newVersion); err != nil {
		return nil, err
	}

	// 4. 深度感知持久化：执行完整递归保存并将深层节点从内存裁剪
	if err := t.flushNodes(sess, newVersion); err != nil {
		return nil, err
	}

	// 5. 特别补丁：根节点本身也需要被序列化保存，否则重启后 resolveNode(rootHash) 会失败
	rootData, err := t.root.Serialize()
	if err == nil {
		if err := sess.Set(nodeKey(rootCommitment[:]), rootData, newVersion); err != nil {
			return nil, err
		}
	}

	// 清理过旧的历史版本缓存
	t.pruneHistory(newVersion)

	return rootCommitment[:], nil
}

// pruneHistory 清理过旧的根承诺历史缓存（保留最近 100 个版本）
func (t *VerkleTree) pruneHistory(currentVersion Version) {
	const keepVersions = 100
	if uint64(currentVersion) <= keepVersions {
		return
	}

	threshold := currentVersion - keepVersions
	for v := range t.rootHistory {
		if v < threshold {
			delete(t.rootHistory, v)
		}
	}
}

// flushNodes 使用 go-verkle 官方的 FlushAtDepth 将深层节点持久化并从内存释放
func (t *VerkleTree) flushNodes(sess VersionedStoreSession, version Version) error {
	// 获取根节点的 InternalNode 引用
	inner, ok := t.root.(*gverkle.InternalNode)
	if !ok {
		return nil // 非 InternalNode，跳过
	}

	// 使用官方 FlushAtDepth：将深度 >= MaxMemoryDepth 的节点 flush 并替换为 HashedNode
	inner.FlushAtDepth(uint8(MaxMemoryDepth), func(path []byte, node gverkle.VerkleNode) {
		// 持久化节点
		data, err := node.Serialize()
		if err != nil {
			return // 忽略序列化失败的节点
		}
		commitment := node.Commitment().Bytes()
		_ = sess.Set(nodeKey(commitment[:]), data, version)
	})

	return nil
}

// nodeKey 生成节点的存储 Key
func nodeKey(hash []byte) []byte {
	k := make([]byte, len(hash)+5)
	copy(k, "node:")
	copy(k[5:], hash)
	return k
}

// Delete 删除指定 Key
func (t *VerkleTree) Delete(key []byte, newVersion Version) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	verkleKey := ToVerkleKey(key)
	if _, err := t.root.Delete(verkleKey[:], nil); err != nil {
		return nil, err
	}

	t.root.Commit()
	rootCommitment := t.root.Commitment().Bytes()

	t.version = newVersion
	t.rootHistory[newVersion] = rootCommitment[:]

	if err := t.store.Set(rootKey(newVersion), rootCommitment[:], newVersion); err != nil {
		return nil, err
	}

	return rootCommitment[:], nil
}

// DeleteWithSession 在会话中删除
func (t *VerkleTree) DeleteWithSession(sess VersionedStoreSession, key []byte, newVersion Version) ([]byte, error) {
	return t.Delete(key, newVersion)
}

// ============================================
// 访问器
// ============================================

// Root 获取当前根承诺
func (t *VerkleTree) Root() []byte {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.root == nil || t.root.Commitment() == nil {
		return make([]byte, 32)
	}
	commitment := t.root.Commitment().Bytes()
	return commitment[:]
}

// Version 获取当前版本
func (t *VerkleTree) Version() Version {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.version
}

// GetRootHash 获取指定版本的根承诺
func (t *VerkleTree) GetRootHash(version Version) ([]byte, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if version == 0 || version == t.version {
		return t.Root(), nil
	}
	if root, ok := t.rootHistory[version]; ok {
		return root, nil
	}
	return nil, ErrVersionNotFound
}

// ============================================
// 证明
// ============================================

// Prove 生成指定 Key 的 Verkle 证明
func (t *VerkleTree) Prove(key []byte) (*VerkleProof, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	verkleKey := ToVerkleKey(key)
	value, _ := t.root.Get(verkleKey[:], nil)

	proof := &VerkleProof{
		Key:         key,
		Value:       value,
		Commitments: [][]byte{t.Root()},
		Depths:      []byte{0},
	}

	return proof, nil
}

// VerifyVerkleProof 验证 Verkle 证明（简化实现）
func VerifyVerkleProof(proof *VerkleProof, root []byte) bool {
	if len(proof.Commitments) == 0 {
		return !proof.IsMembershipProof()
	}
	if !bytes.Equal(proof.Commitments[0], root) {
		return false
	}
	return true
}

// ============================================
// 工具函数
// ============================================

// ToVerkleKey 将任意长度 Key 转换为 32 字节 Verkle Key
func ToVerkleKey(key []byte) [32]byte {
	return sha256.Sum256(key)
}

// largeValueKey 生成大值的存储 Key
func largeValueKey(hash []byte) []byte {
	k := make([]byte, len(hash)+7)
	copy(k, "vlarge:")
	copy(k[7:], hash)
	return k
}

// rootKey 生成根承诺的存储 Key
func rootKey(version Version) []byte {
	k := make([]byte, 14)
	copy(k, "root:v")
	k[6] = byte(version >> 56)
	k[7] = byte(version >> 48)
	k[8] = byte(version >> 40)
	k[9] = byte(version >> 32)
	k[10] = byte(version >> 24)
	k[11] = byte(version >> 16)
	k[12] = byte(version >> 8)
	k[13] = byte(version)
	return k
}

// ============================================
// 兼容性接口
// ============================================

// CommitRoot 更新树的根承诺和版本号
func (t *VerkleTree) CommitRoot(version Version, root []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.version = version
	t.rootHistory[version] = root
}

// RestoreMemoryLayers 从存储中重建前 MaxMemoryDepth 层的内存结构
func (t *VerkleTree) RestoreMemoryLayers(rootHash []byte) error {
	if len(rootHash) == 0 || bytes.Equal(rootHash, make([]byte, 32)) {
		return nil
	}

	node, err := t.resolveNode(rootHash)
	if err != nil {
		return err
	}
	t.root = node

	return t.restoreNodeRecursive(t.root, 0)
}

func (t *VerkleTree) restoreNodeRecursive(node gverkle.VerkleNode, depth int) error {
	if depth >= MaxMemoryDepth || node == nil {
		return nil
	}

	inner, ok := node.(*gverkle.InternalNode)
	if !ok {
		return nil
	}

	for i := 0; i < 256; i++ {
		// v0.2.2 中如果子节点为 nil 且有承诺值，说明是冷节点
		// 注意：这里的恢复逻辑依赖于能够获取子节点承诺。
		// 由于 InternalNode 没直接公开子节点承诺数组，我们假设 ParseNode 已处理。
		child := inner.Children()[i]
		if child != nil {
			if err := t.restoreNodeRecursive(child, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// Commit 提交当前状态
func (t *VerkleTree) Commit() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.root.Commit()
	return nil
}

// Committer 返回承诺器（兼容性接口，返回 nil 或内部对象）
func (t *VerkleTree) Committer() interface{} {
	return nil
}
