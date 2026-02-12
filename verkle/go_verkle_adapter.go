package verkle

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
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
	MaxMemoryDepth = 1
)

// VerkleTreeOptions 控制 VerkleTree 行为（用于调试/压测场景）
type VerkleTreeOptions struct {
	// DisableRootCommit 统一开关：
	// 1) 跳过根承诺计算（t.root.Commit）
	// 2) 跳过 Verkle 树写入（Insert/Delete/节点持久化）
	// 仅用于性能/内存定位。
	DisableRootCommit bool
}

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
	flushMu     sync.Mutex
	root        gverkle.VerkleNode
	store       VersionedStore
	version     Version
	rootHistory map[Version][]byte
	// disableRootCommit 为统一开关：跳过根承诺计算并跳过 Verkle 写路径。
	disableRootCommit bool
	pendingNodes      []pendingNodeData // 待写入的节点数据（主事务提交后写入）
}

// pendingNodeData 待写入的节点数据
type pendingNodeData struct {
	key     []byte
	data    []byte
	path    []byte
	version Version
}

// largeValueEntry 表示一个待写入的大值条目（vlarge:*）。
type largeValueEntry struct {
	key []byte
	val []byte
}

// processedEntry 表示已转换为 Verkle 插入格式的 KV。
type processedEntry struct {
	verkleKey [32]byte
	treeVal   []byte
}

// NewVerkleTree 创建新的 Verkle Tree 包装器 (v0.2.2 兼容)
func NewVerkleTree(store VersionedStore) *VerkleTree {
	return NewVerkleTreeWithOptions(store, VerkleTreeOptions{})
}

// NewVerkleTreeWithOptions 创建新的 Verkle Tree 包装器并应用可选配置。
func NewVerkleTreeWithOptions(store VersionedStore, opts VerkleTreeOptions) *VerkleTree {
	return &VerkleTree{
		root:              gverkle.New(),
		store:             store,
		version:           0,
		rootHistory:       make(map[Version][]byte),
		disableRootCommit: opts.DisableRootCommit,
	}
}

// nodeResolver 创建一个 NodeResolverFn 用于按需加载被 flush 的节点（使用独立会话，适用于无事务上下文场景）
func (t *VerkleTree) nodeResolver() gverkle.NodeResolverFn {
	return func(path []byte) ([]byte, error) {
		sess, err := t.store.NewSession()
		if err != nil {
			return nil, err
		}
		defer sess.Close()
		val, err := sess.GetKV(nodeKey(path))
		if err != nil {
			return nil, err
		}
		if val == nil {
			return nil, fmt.Errorf("verkle tree: node %x not found in storage: %w", path, ErrNotFound)
		}
		if len(val) < 65 {
			return nil, fmt.Errorf("verkle tree: node %x payload too short (%d)", path, len(val))
		}
		return val, nil
	}
}

// nodeResolverWithSession 创建一个使用指定会话的 NodeResolverFn（适用于事务内操作，可读取未提交数据）
func (t *VerkleTree) nodeResolverWithSession(sess VersionedStoreSession) gverkle.NodeResolverFn {
	return func(path []byte) ([]byte, error) {
		val, err := sess.GetKV(nodeKey(path))
		if err != nil {
			return nil, err
		}
		if val == nil {
			return nil, fmt.Errorf("verkle tree: node %x not found in storage: %w", path, ErrNotFound)
		}
		if len(val) < 65 {
			return nil, fmt.Errorf("verkle tree: node %x payload too short (%d)", path, len(val))
		}
		return val, nil
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
	// 增强防御：如果数据长度不足 65 字节（完整节点至少 65 字节），视为节点不存在
	if data == nil || len(data) < 65 {
		return nil, ErrNotFound
	}

	// 使用 gverkle.ParseNode 反序列化
	return gverkle.ParseNode(data, 0)
}

// resolveNodeWithSession 使用给定的 Session 解析节点（避免 Session 隔离导致数据不可见）
func (t *VerkleTree) resolveNodeWithSession(sess VersionedStoreSession, hash []byte) (gverkle.VerkleNode, error) {
	data, err := sess.GetKV(nodeKey(hash))
	if err != nil {
		return nil, err
	}
	if data == nil || len(data) < 65 {
		return nil, ErrNotFound
	}
	return gverkle.ParseNode(data, 0)
}

// ============================================
// 核心操作
// ============================================

// computeRootCommitmentLocked 计算（或读取）当前根承诺。
// 调用方必须持有 t.mu（读锁或写锁均可）。
func (t *VerkleTree) computeRootCommitmentLocked() []byte {
	if t.root == nil {
		return make([]byte, CommitmentSize)
	}
	if !t.disableRootCommit {
		t.root.Commit()
	}
	point := t.root.Commitment()
	if point == nil {
		return make([]byte, CommitmentSize)
	}
	commitment := point.Bytes()
	root := make([]byte, CommitmentSize)
	copy(root, commitment[:])
	return root
}

// WriteDisabled 返回当前是否禁用 Verkle 写路径。
func (t *VerkleTree) WriteDisabled() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.disableRootCommit
}

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
	data, err := t.root.Get(verkleKey[:], t.nodeResolverWithSession(sess))
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

	// 统一开关：禁用 Verkle 写路径（仅更新版本/根缓存）
	if t.disableRootCommit {
		rootCommitment := t.computeRootCommitmentLocked()
		t.version = newVersion
		t.rootHistory[newVersion] = rootCommitment
		return rootCommitment, nil
	}

	for i := 0; i < len(keys); i++ {
		verkleKey := ToVerkleKey(keys[i])
		if err := t.root.Insert(verkleKey[:], values[i], t.nodeResolver()); err != nil {
			return nil, err
		}
	}

	// 计算并返回根承诺
	rootCommitment := t.computeRootCommitmentLocked()

	t.version = newVersion
	t.rootHistory[newVersion] = rootCommitment

	// 保存到存储
	if err := t.store.Set(rootKey(newVersion), rootCommitment, newVersion); err != nil {
		return nil, err
	}

	return rootCommitment, nil
}

// prepareProcessedEntries 预处理输入键值：
// 1) 将任意 key 转为 32 字节 Verkle key
// 2) 将 value 转为树内存储格式（直存或间接引用）
// 3) 收集去重后的大值写入列表（同一块内相同大值只写一次）
func (t *VerkleTree) prepareProcessedEntries(keys [][]byte, values [][]byte) ([]processedEntry, []largeValueEntry, error) {
	if len(keys) != len(values) {
		return nil, nil, errors.New("keys and values must have the same length")
	}

	processedEntries := make([]processedEntry, 0, len(keys))
	largeValues := make([]largeValueEntry, 0)
	seenLargeValueKey := make(map[string]struct{}, len(keys))

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
			lvKey := largeValueKey(hashPart)

			// 块内去重：相同 hash 的大值只写一次。
			if _, ok := seenLargeValueKey[string(lvKey)]; !ok {
				largeValues = append(largeValues, largeValueEntry{
					key: lvKey,
					val: val,
				})
				seenLargeValueKey[string(lvKey)] = struct{}{}
			}

			treeVal = make([]byte, 32)
			treeVal[0] = markerIndirect
			copy(treeVal[1:], hashPart)
		}

		processedEntries = append(processedEntries, processedEntry{
			verkleKey: verkleKey,
			treeVal:   treeVal,
		})
	}

	return processedEntries, largeValues, nil
}

// writeLargeValues 分批写入大值（独立事务）。
func (t *VerkleTree) writeLargeValues(largeValues []largeValueEntry, newVersion Version) error {
	if len(largeValues) == 0 {
		return nil
	}

	const largeValueBatchSize = 100
	for i := 0; i < len(largeValues); i += largeValueBatchSize {
		end := i + largeValueBatchSize
		if end > len(largeValues) {
			end = len(largeValues)
		}
		batch := largeValues[i:end]

		batchSess, err := t.store.NewSession()
		if err != nil {
			return fmt.Errorf("failed to create large value batch session: %w", err)
		}

		for _, lv := range batch {
			if err := batchSess.Set(lv.key, lv.val, newVersion); err != nil {
				batchSess.Close()
				return fmt.Errorf("failed to set large value: %w", err)
			}
		}

		if err := batchSess.Commit(); err != nil {
			batchSess.Close()
			return fmt.Errorf("failed to commit large value batch %d-%d: %w", i, end, err)
		}
		batchSess.Close()
	}

	return nil
}

// updateWithSessionProcessed 在会话中应用已预处理好的树写入。
func (t *VerkleTree) updateWithSessionProcessed(sess VersionedStoreSession, processedEntries []processedEntry, newVersion Version) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// 统一开关：禁用 Verkle 写路径（仅更新版本/根缓存）
	if t.disableRootCommit {
		rootCommitment := t.computeRootCommitmentLocked()
		t.version = newVersion
		t.rootHistory[newVersion] = rootCommitment
		t.pruneHistory(newVersion)
		return rootCommitment, nil
	}

	// 更新树结构（内存操作）
	for _, entry := range processedEntries {
		if err := t.root.Insert(entry.verkleKey[:], entry.treeVal, t.nodeResolverWithSession(sess)); err != nil {
			return nil, err
		}
	}

	// 计算根承诺
	rootCommitment := t.computeRootCommitmentLocked()

	t.version = newVersion
	t.rootHistory[newVersion] = rootCommitment

	// 保存根承诺和根节点（主事务）
	if err := sess.Set(rootKey(newVersion), rootCommitment, newVersion); err != nil {
		return nil, err
	}

	rootData, err := t.root.Serialize()
	if err == nil && len(rootData) >= 65 {
		if err := sess.Set(nodeKey(rootCommitment), rootData, newVersion); err != nil {
			return nil, err
		}
	}

	// 收集并释放深层节点内存，持久化由主事务提交后 FlushPendingNodes 完成。
	t.collectAndFlushToMemory(newVersion)
	t.pruneHistory(newVersion)
	return rootCommitment, nil
}

// UpdateWithSession 在会话中批量更新（支持大值分片存储）
// 使用分批写入策略：先写大值，再在主事务中更新树和根。
func (t *VerkleTree) UpdateWithSession(sess VersionedStoreSession, keys [][]byte, values [][]byte, newVersion Version) ([]byte, error) {
	processedEntries, largeValues, err := t.prepareProcessedEntries(keys, values)
	if err != nil {
		return nil, err
	}
	if err := t.writeLargeValues(largeValues, newVersion); err != nil {
		return nil, err
	}
	return t.updateWithSessionProcessed(sess, processedEntries, newVersion)
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

// collectAndFlushToMemory 收集深层节点并释放内存（转换为 HashedNode）
// 节点数据存储在 pendingNodes 中，在主事务提交后通过 FlushPendingNodes 写入
func (t *VerkleTree) collectAndFlushToMemory(version Version) {
	inner, ok := t.root.(*gverkle.InternalNode)
	if !ok {
		return
	}

	// 注意：不再清空 pendingNodes，而是追加，由 FlushPendingNodes 在写入后清空
	// t.pendingNodes = nil

	// 使用 FlushAtDepth 将 MaxMemoryDepth 层以下的节点释放内存
	// FlushAtDepth 会自动将这些节点替换为 HashedNode
	inner.FlushAtDepth(uint8(MaxMemoryDepth), nil, func(path []byte, node gverkle.VerkleNode) {
		data, err := node.Serialize()
		if err != nil || len(data) < 65 {
			return
		}
		point := node.Commitment()
		if point == nil {
			return
		}
		commitment := point.Bytes()
		t.pendingNodes = append(t.pendingNodes, pendingNodeData{
			key:     commitment[:],
			data:    data,
			path:    append([]byte(nil), path...), // 复制 path 避免闭包问题
			version: version,
		})
	})
}

// FlushPendingNodes 使用 WriteBatch 原子写入待处理的节点数据
// WriteBatch 没有事务大小限制，可以一次性写入所有节点
func (t *VerkleTree) FlushPendingNodes() error {
	t.flushMu.Lock()
	defer t.flushMu.Unlock()

	t.mu.Lock()
	if len(t.pendingNodes) == 0 {
		t.mu.Unlock()
		return nil
	}
	nodes := append([]pendingNodeData(nil), t.pendingNodes...)
	t.mu.Unlock()

	if len(nodes) == 0 {
		return nil
	}

	// 获取底层 BadgerStore 以使用 WriteBatch
	badgerStore, ok := t.store.(*VersionedBadgerStore)
	var flushErr error
	if !ok {
		// 如果不是 BadgerStore，回退到简单实现（测试用）
		flushErr = t.flushPendingNodesSimple(nodes)
	} else {
		// 收集所有要写入的条目
		entries := make([]BatchEntry, 0, len(nodes)*2)
		for _, nd := range nodes {
			// 使用 commitment 作为 key
			entries = append(entries, BatchEntry{
				Key:     nodeKey(nd.key),
				Value:   nd.data,
				Version: nd.version,
			})

			// 同时使用 path 作为 key
			if len(nd.path) > 0 {
				entries = append(entries, BatchEntry{
					Key:     nodeKey(nd.path),
					Value:   nd.data,
					Version: nd.version,
				})
			}
		}
		// 原子写入所有节点数据
		flushErr = badgerStore.WriteBatch(entries)
	}
	if flushErr != nil {
		return flushErr
	}

	// 仅在写入成功后移除本次快照中的 pending，失败时保留以便后续重试
	t.mu.Lock()
	if len(t.pendingNodes) <= len(nodes) {
		t.pendingNodes = nil
	} else {
		t.pendingNodes = append([]pendingNodeData(nil), t.pendingNodes[len(nodes):]...)
	}
	t.mu.Unlock()
	return nil
}

// flushPendingNodesSimple 简单实现（用于非 BadgerStore 的测试场景）
func (t *VerkleTree) flushPendingNodesSimple(nodes []pendingNodeData) error {
	sess, err := t.store.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	for _, nd := range nodes {
		if err := sess.Set(nodeKey(nd.key), nd.data, nd.version); err != nil {
			return err
		}
		if len(nd.path) > 0 {
			if err := sess.Set(nodeKey(nd.path), nd.data, nd.version); err != nil {
				return err
			}
		}
	}

	return sess.Commit()
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

	// 统一开关：禁用 Verkle 写路径（仅更新版本/根缓存）
	if t.disableRootCommit {
		rootCommitment := t.computeRootCommitmentLocked()
		t.version = newVersion
		t.rootHistory[newVersion] = rootCommitment
		return rootCommitment, nil
	}

	verkleKey := ToVerkleKey(key)
	if _, err := t.root.Delete(verkleKey[:], nil); err != nil {
		return nil, err
	}

	rootCommitment := t.computeRootCommitmentLocked()

	t.version = newVersion
	t.rootHistory[newVersion] = rootCommitment

	if err := t.store.Set(rootKey(newVersion), rootCommitment, newVersion); err != nil {
		return nil, err
	}

	return rootCommitment, nil
}

// DeleteWithSession 在会话中删除
func (t *VerkleTree) DeleteWithSession(sess VersionedStoreSession, key []byte, newVersion Version) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// 统一开关：禁用 Verkle 写路径（仅更新版本/根缓存）
	if t.disableRootCommit {
		rootCommitment := t.computeRootCommitmentLocked()
		t.version = newVersion
		t.rootHistory[newVersion] = rootCommitment
		t.pruneHistory(newVersion)
		return rootCommitment, nil
	}

	verkleKey := ToVerkleKey(key)
	if _, err := t.root.Delete(verkleKey[:], t.nodeResolverWithSession(sess)); err != nil {
		return nil, err
	}

	rootCommitment := t.computeRootCommitmentLocked()

	t.version = newVersion
	t.rootHistory[newVersion] = rootCommitment

	if err := sess.Set(rootKey(newVersion), rootCommitment, newVersion); err != nil {
		return nil, err
	}

	rootData, err := t.root.Serialize()
	if err == nil && len(rootData) >= 65 {
		if err := sess.Set(nodeKey(rootCommitment), rootData, newVersion); err != nil {
			return nil, err
		}
	}

	t.collectAndFlushToMemory(newVersion)
	t.pruneHistory(newVersion)

	return rootCommitment, nil
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

// SyncFromStateRoot 确保当前树状态与指定的状态根一致
// 如果不一致，则从存储中恢复内存层结构
// 这对于多节点场景至关重要：不同节点的内存树可能演化不同步
func (t *VerkleTree) SyncFromStateRoot(expectedRoot []byte) error {
	if len(expectedRoot) == 0 || bytes.Equal(expectedRoot, make([]byte, 32)) {
		// 空根或零根，无需同步
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	currentRoot := t.root.Commitment().Bytes()
	if bytes.Equal(currentRoot[:], expectedRoot) {
		// 根已一致，无需恢复
		return nil
	}

	// 根不一致，需要从持久化存储恢复
	node, err := t.resolveNode(expectedRoot)
	if err != nil {
		// 关键：如果无法恢复父状态根（例如在多节点首次执行时），
		// 则重置树为空状态，允许从头构建。
		// 这确保了多节点场景下的一致性：每个节点从相同的空状态开始。
		fmt.Printf("[Verkle] Notice: resetting tree to empty state (cannot resolve root %x: %v)\n", expectedRoot[:8], err)
		t.root = gverkle.New()
		t.version = 0
		return nil // 不返回错误，允许继续执行
	}

	t.root = node

	// 递归恢复前 MaxMemoryDepth 层
	if restoreErr := t.restoreNodeRecursive(t.root, 0); restoreErr != nil {
		// 恢复内存层失败，同样重置为空状态
		fmt.Printf("[Verkle] Notice: resetting tree after restore failure: %v\n", restoreErr)
		t.root = gverkle.New()
		t.version = 0
	}

	return nil
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

// SyncFromStateRootWithSession 使用给定的 Session 确保当前树状态与指定的状态根一致
// 这解决了 Session 隔离导致的节点数据不可见问题
func (t *VerkleTree) SyncFromStateRootWithSession(sess VersionedStoreSession, expectedRoot []byte) error {
	if len(expectedRoot) == 0 || bytes.Equal(expectedRoot, make([]byte, 32)) {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	currentRoot := t.root.Commitment().Bytes()
	if bytes.Equal(currentRoot[:], expectedRoot) {
		return nil
	}

	// 使用传入的 Session 解析节点，避免创建新 Session 导致的数据隔离问题
	node, err := t.resolveNodeWithSession(sess, expectedRoot)
	if err != nil {
		fmt.Printf("[Verkle] Notice: resetting tree to empty state (cannot resolve root %x: %v)\n", expectedRoot[:8], err)
		t.root = gverkle.New()
		t.version = 0
		return nil
	}

	t.root = node

	if restoreErr := t.restoreNodeRecursiveWithSession(sess, t.root, 0); restoreErr != nil {
		fmt.Printf("[Verkle] Notice: resetting tree after restore failure: %v\n", restoreErr)
		t.root = gverkle.New()
		t.version = 0
	}

	return nil
}

// restoreNodeRecursiveWithSession 使用给定的 Session 递归恢复内存层
func (t *VerkleTree) restoreNodeRecursiveWithSession(sess VersionedStoreSession, node gverkle.VerkleNode, depth int) error {
	if depth >= MaxMemoryDepth || node == nil {
		return nil
	}

	inner, ok := node.(*gverkle.InternalNode)
	if !ok {
		return nil
	}

	for i := 0; i < 256; i++ {
		child := inner.Children()[i]
		if child != nil {
			if err := t.restoreNodeRecursiveWithSession(sess, child, depth+1); err != nil {
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
	if !t.disableRootCommit {
		t.root.Commit()
	}
	return nil
}

// Committer 返回承诺器（兼容性接口，返回 nil 或内部对象）
func (t *VerkleTree) Committer() interface{} {
	return nil
}
