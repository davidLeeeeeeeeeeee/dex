package smt

import (
	"errors"
	"sync"

	"github.com/dgraph-io/badger/v4"
)

// ============================================
// JMT 并行更新实现
// 利用 16 叉树的天然分区结构实现多核并行
// ============================================

// BucketUpdate 表示一个 Bucket 内的待更新数据
type BucketUpdate struct {
	Nibble byte     // Bucket 编号 (0-15)
	Keys   [][]byte // 属于此 Bucket 的 Keys
	Values [][]byte // 对应的 Values
	Paths  [][]byte // 预计算的 Key 哈希路径
}

// BucketResult 表示一个 Bucket 更新的结果
type BucketResult struct {
	Nibble    byte   // Bucket 编号
	ChildHash []byte // 更新后的子树根哈希
	Err       error  // 错误（如果有）
	// 收集的写操作，待主线程统一提交
	Writes []WriteOp
	// 本地节点缓存：存储当前 Bucket 计算过程中创建的节点
	// key 为节点哈希的字符串形式
	LocalNodes map[string][]byte
	// 本地读取缓存：存储从存储中读取的已有节点，避免重复读取和全局锁竞争
	ReadCache map[string][]byte
}

// WriteOp 表示一个待写入的操作
type WriteOp struct {
	Key     []byte
	Value   []byte
	Version Version
}

// ParallelUpdateConfig 并行更新配置
type ParallelUpdateConfig struct {
	MaxWorkers          int // 最大并发数，默认 16
	MinBatchForParallel int // 触发并行的最小批次大小，默认 100
}

// DefaultParallelConfig 返回默认的并行配置
func DefaultParallelConfig() ParallelUpdateConfig {
	return ParallelUpdateConfig{
		MaxWorkers:          16,
		MinBatchForParallel: 100,
	}
}

// ParallelUpdate 并行批量更新
// 将 keys 按首个 Nibble 分组，每个 Bucket 由独立 goroutine 处理
func (jmt *JellyfishMerkleTree) ParallelUpdate(
	sess VersionedStoreSession,
	keys [][]byte,
	values [][]byte,
	newVersion Version,
	cfg ParallelUpdateConfig,
) ([]byte, error) {
	if len(keys) != len(values) {
		return nil, errors.New("keys and values must have the same length")
	}

	// 小批次回退到串行更新
	if len(keys) < cfg.MinBatchForParallel {
		return jmt.UpdateWithSession(sess, keys, values, newVersion)
	}

	jmt.mu.Lock()
	defer jmt.mu.Unlock()

	// 1. 按 Nibble 分组
	buckets := jmt.groupByNibble(keys, values)

	// 2. 获取当前根节点
	var currentRoot *InternalNode
	if !jmt.hasher.IsPlaceholder(jmt.root) {
		rootData, err := jmt.getNodeDataLocked(sess, jmt.root, newVersion)
		if err != nil {
			return nil, err
		}
		if IsInternalNodeData(rootData) {
			currentRoot, err = jmt.hasher.ParseInternalNode(rootData)
			if err != nil {
				return nil, err
			}
		}
	}

	// 3. 统计活跃 Bucket 数量
	activeBuckets := 0
	for i := byte(0); i < 16; i++ {
		if len(buckets[i].Keys) > 0 {
			activeBuckets++
		}
	}

	// 如果活跃 Bucket 太少，不值得并行
	if activeBuckets <= 2 {
		return jmt.updateWithSessionInternalLocked(sess, keys, values, newVersion)
	}

	// 4. 并行处理每个 Bucket（只做计算，不写存储）
	var wg sync.WaitGroup
	results := make([]BucketResult, 16)

	for i := byte(0); i < 16; i++ {
		bucket := buckets[i]
		if len(bucket.Keys) == 0 {
			// 无更新的 Bucket 保持原有子节点
			if currentRoot != nil {
				results[i] = BucketResult{
					Nibble:    i,
					ChildHash: currentRoot.GetChild(i),
				}
			} else {
				results[i] = BucketResult{
					Nibble:    i,
					ChildHash: jmt.hasher.Placeholder(),
				}
			}
			continue
		}

		// 获取当前子树根
		var childHash []byte
		if currentRoot != nil {
			childHash = currentRoot.GetChild(i)
		}
		if childHash == nil {
			childHash = jmt.hasher.Placeholder()
		}

		wg.Add(1)
		go func(nibble byte, b BucketUpdate, currentChildHash []byte) {
			defer wg.Done()

			// 🚀 核心优化：每个 Worker 仅开启一个读事务，跨越整个子树的处理过程
			// 避免了数万次 db.View() 的重复开启/关闭开销
			var result BucketResult
			badgerStore, ok := jmt.store.(*VersionedBadgerStore)
			if ok {
				err := badgerStore.db.View(func(txn *badger.Txn) error {
					reader := func(key []byte, ver Version) ([]byte, error) {
						return badgerStore.GetWithTxn(txn, key, ver)
					}
					result = jmt.computeBucketUpdate(b, currentChildHash, newVersion, reader)
					return nil
				})
				if err != nil {
					result.Err = err
				}
			} else {
				// 降级方案：如果不是 BadgerStore，则使用通用读取器（虽然依然会有开销）
				reader := func(key []byte, ver Version) ([]byte, error) {
					return jmt.store.Get(key, ver)
				}
				result = jmt.computeBucketUpdate(b, currentChildHash, newVersion, reader)
			}
			results[nibble] = result
		}(i, bucket, childHash)
	}

	wg.Wait()

	// 5. 检查错误 & 收集并去重所有写操作
	// 由于 JMT 是内容寻址，同一个 Batch 中可能会产生多次相同的中间节点写入
	// 去重可以极大地缩减 BadgerDB 事务大小
	uniqueWrites := make(map[string]WriteOp)
	for i := byte(0); i < 16; i++ {
		if results[i].Err != nil {
			return nil, results[i].Err
		}
		for _, w := range results[i].Writes {
			uniqueWrites[string(w.Key)] = w
		}
	}

	// 6. 合并结果到新的根节点
	newRoot, rootWrites, err := jmt.mergeResultsCollect(results, newVersion)
	if err != nil {
		return nil, err
	}
	for _, w := range rootWrites {
		uniqueWrites[string(w.Key)] = w
	}

	// 7. 顺序提交去重后的写操作到 Session
	// fmt.Printf("Batch size %d: committing %d unique nodes\n", len(keys), len(uniqueWrites))
	for _, w := range uniqueWrites {
		if err := sess.Set(w.Key, w.Value, w.Version); err != nil {
			return nil, err
		}
	}

	// 8. 保存根哈希
	if err := sess.Set(rootKey(newVersion), newRoot, newVersion); err != nil {
		return nil, err
	}

	// 9. 更新树状态
	jmt.rootHistory[newVersion] = newRoot
	jmt.root = newRoot
	jmt.version = newVersion

	return newRoot, nil
}

// groupByNibble 按首个 Nibble 分组
func (jmt *JellyfishMerkleTree) groupByNibble(keys, values [][]byte) [16]BucketUpdate {
	var buckets [16]BucketUpdate
	for i := byte(0); i < 16; i++ {
		buckets[i] = BucketUpdate{Nibble: i}
	}

	for i, key := range keys {
		path := jmt.hasher.Path(key)
		nibble := getNibbleAt(path, 0)

		buckets[nibble].Keys = append(buckets[nibble].Keys, key)
		buckets[nibble].Values = append(buckets[nibble].Values, values[i])
		buckets[nibble].Paths = append(buckets[nibble].Paths, path)
	}

	return buckets
}

// computeBucketUpdate 计算单个 Bucket 的更新（纯计算，不写存储）
// 此方法可被多个 goroutine 安全并发调用
func (jmt *JellyfishMerkleTree) computeBucketUpdate(
	bucket BucketUpdate,
	currentChildHash []byte,
	version Version,
	reader func([]byte, Version) ([]byte, error),
) BucketResult {
	result := BucketResult{
		Nibble:     bucket.Nibble,
		Writes:     make([]WriteOp, 0, len(bucket.Keys)*3), // 预分配
		LocalNodes: make(map[string][]byte),                // 本地节点缓存
		ReadCache:  make(map[string][]byte),                // 本地读取缓存
	}

	// 逐个更新子树中的 Key
	childHash := currentChildHash
	for i := 0; i < len(bucket.Keys); i++ {
		path := bucket.Paths[i]
		value := bucket.Values[i]
		valueHash := jmt.hasher.Digest(value)

		// 收集值写操作
		result.Writes = append(result.Writes, WriteOp{
			Key:     valueKey(path),
			Value:   value,
			Version: version,
		})

		// 从 depth=1 开始计算更新
		var err error
		childHash, err = jmt.computeSubtreeUpdate(&result, path, valueHash, childHash, 1, version, reader)
		if err != nil {
			result.Err = err
			return result
		}
	}

	result.ChildHash = childHash
	return result
}

// computeSubtreeUpdate 计算子树更新（纯计算，收集写操作）
func (jmt *JellyfishMerkleTree) computeSubtreeUpdate(
	result *BucketResult,
	path, valueHash, nodeHash []byte,
	depth int,
	version Version,
	reader func([]byte, Version) ([]byte, error),
) ([]byte, error) {
	if jmt.hasher.IsPlaceholder(nodeHash) {
		// 到达空节点，创建叶子
		leafHash, leafData := jmt.hasher.DigestLeafNode(path, valueHash)
		result.Writes = append(result.Writes, WriteOp{
			Key:     leafHash,
			Value:   leafData,
			Version: version,
		})
		// 存入本地缓存
		result.LocalNodes[string(leafHash)] = leafData
		return leafHash, nil
	}

	// 1. 先从本地写入缓存查找（同一 Bucket 内刚创建的节点）
	nodeData, found := result.LocalNodes[string(nodeHash)]

	// 2. 再从本地读取缓存查找（避免全局锁竞争）
	if !found {
		nodeData, found = result.ReadCache[string(nodeHash)]
	}

	// 3. 最后从存储读取（使用 Worker 复用的事务读取器）
	if !found {
		var err error
		nodeData, err = reader(nodeHash, version)
		if err != nil {
			nodeData, err = reader(nodeHash, 0)
			if err != nil {
				return nil, err
			}
		}
		// 写入本地读取缓存，供同一 Bucket 的后续 Key 使用
		if nodeData != nil {
			result.ReadCache[string(nodeHash)] = nodeData
		}
	}

	nodeType := jmt.hasher.GetNodeType(nodeData)

	switch nodeType {
	case NodeTypeLeaf:
		existingLeaf, err := jmt.hasher.ParseLeafNode(nodeData)
		if err != nil {
			return nil, err
		}

		if bytesEqual(existingLeaf.KeyHash, path) {
			// 更新现有叶子
			newLeafHash, newLeafData := jmt.hasher.DigestLeafNode(path, valueHash)
			result.Writes = append(result.Writes, WriteOp{
				Key:     newLeafHash,
				Value:   newLeafData,
				Version: version,
			})
			result.LocalNodes[string(newLeafHash)] = newLeafData
			return newLeafHash, nil
		}

		// 需要分裂
		return jmt.computeLeafSplit(result, existingLeaf, path, valueHash, depth, version)

	case NodeTypeInternal:
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
		newChildHash, err := jmt.computeSubtreeUpdate(result, path, valueHash, childHash, depth+1, version, reader)
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
		result.Writes = append(result.Writes, WriteOp{
			Key:     newNodeHash,
			Value:   newNodeData,
			Version: version,
		})
		result.LocalNodes[string(newNodeHash)] = newNodeData
		return newNodeHash, nil

	default:
		return nil, errors.New("corrupted tree: unknown node type")
	}
}

// computeLeafSplit 计算叶子分裂（纯计算）
func (jmt *JellyfishMerkleTree) computeLeafSplit(
	result *BucketResult,
	existingLeaf *LeafNode,
	newPath, newValueHash []byte,
	depth int,
	version Version,
) ([]byte, error) {
	existingPath := existingLeaf.KeyHash

	// 找到分叉点
	commonPrefix := countCommonNibblePrefix(existingPath, newPath)

	// 创建两个叶子节点
	existingLeafHash, existingLeafData := jmt.hasher.DigestLeafNode(existingPath, existingLeaf.ValueHash)
	result.Writes = append(result.Writes, WriteOp{
		Key:     existingLeafHash,
		Value:   existingLeafData,
		Version: version,
	})
	result.LocalNodes[string(existingLeafHash)] = existingLeafData

	newLeafHash, newLeafData := jmt.hasher.DigestLeafNode(newPath, newValueHash)
	result.Writes = append(result.Writes, WriteOp{
		Key:     newLeafHash,
		Value:   newLeafData,
		Version: version,
	})
	result.LocalNodes[string(newLeafHash)] = newLeafData

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
	result.Writes = append(result.Writes, WriteOp{
		Key:     nodeHash,
		Value:   nodeData,
		Version: version,
	})
	result.LocalNodes[string(nodeHash)] = nodeData

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
		result.Writes = append(result.Writes, WriteOp{
			Key:     nodeHash,
			Value:   nodeData,
			Version: version,
		})
		result.LocalNodes[string(nodeHash)] = nodeData
		currentHash = nodeHash
	}

	return currentHash, nil
}

// mergeResultsCollect 合并所有 Bucket 结果到新的根节点（收集写操作）
func (jmt *JellyfishMerkleTree) mergeResultsCollect(
	results []BucketResult,
	version Version,
) ([]byte, []WriteOp, error) {
	// 构建新的根内部节点
	var children [16][]byte
	allPlaceholder := true

	for i := byte(0); i < 16; i++ {
		children[i] = results[i].ChildHash
		if children[i] == nil {
			children[i] = jmt.hasher.Placeholder()
		}
		if !jmt.hasher.IsPlaceholder(children[i]) {
			allPlaceholder = false
		}
	}

	if allPlaceholder {
		return jmt.hasher.Placeholder(), nil, nil
	}

	node := InternalNodeFromChildren(children, jmt.hasher.Placeholder())
	nodeHash, nodeData := jmt.hasher.DigestInternalNodeFromNode(node)

	writes := []WriteOp{{
		Key:     nodeHash,
		Value:   nodeData,
		Version: version,
	}}

	return nodeHash, writes, nil
}

// getNodeDataLocked 获取节点数据（已持有锁时调用）
func (jmt *JellyfishMerkleTree) getNodeDataLocked(sess VersionedStoreSession, hash []byte, version Version) ([]byte, error) {
	// 尝试缓存
	if data := jmt.getCache(hash); data != nil {
		return data, nil
	}

	// 从存储获取
	var data []byte
	var err error
	if sess != nil {
		data, err = sess.Get(hash, version)
		if err != nil {
			data, err = sess.Get(hash, 0)
		}
	} else {
		data, err = jmt.store.Get(hash, version)
		if err != nil {
			data, err = jmt.store.Get(hash, 0)
		}
	}

	if err != nil {
		return nil, err
	}

	jmt.setCache(hash, data)
	return data, nil
}

// updateWithSessionInternalLocked 内部串行更新方法（已持有锁时调用）
func (jmt *JellyfishMerkleTree) updateWithSessionInternalLocked(
	sess VersionedStoreSession,
	keys [][]byte,
	values [][]byte,
	newVersion Version,
) ([]byte, error) {
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

// bytesEqual 安全的字节比较
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
