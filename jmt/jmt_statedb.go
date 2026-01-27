package smt

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"

	"github.com/dgraph-io/badger/v4"
)

// ============================================
// JMT StateDB 适配层
// 提供与现有 StateDB 兼容的接口
// ============================================

// KVUpdate 表示一个 KV 更新操作
// 与原 stateDB.KVUpdate 兼容
type KVUpdate struct {
	Key     string
	Value   []byte
	Deleted bool
}

// JMTConfig 是 JMTStateDB 的配置
type JMTConfig struct {
	DataDir string // BadgerDB 目录（如已有 DB 实例则忽略）
	Prefix  []byte // 命名空间前缀，默认 "jmt:"
}

// JMTStateDB 提供与现有 StateDB 兼容的接口
// 使用 JMT 作为底层存储
type JMTStateDB struct {
	tree         *JellyfishMerkleTree
	store        *VersionedBadgerStore
	db           *badger.DB
	ownsDB       bool // 是否由本实例管理 DB 生命周期
	prefix       []byte
	mu           sync.RWMutex
	pendingKeys  [][]byte // 当前批次的 keys
	pendingVals  [][]byte // 当前批次的 values
	batchVersion Version  // 当前批次的版本号
}

// NewJMTStateDB 创建 JMT 状态存储（自己管理 BadgerDB）
func NewJMTStateDB(cfg JMTConfig) (*JMTStateDB, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("DataDir is required")
	}

	opts := badger.DefaultOptions(cfg.DataDir).
		WithNumVersionsToKeep(10).
		WithSyncWrites(false).
		WithLogger(nil)

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open badger db: %w", err)
	}

	stateDB, err := NewJMTStateDBWithDB(db, cfg)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	stateDB.ownsDB = true
	return stateDB, nil
}

// NewJMTStateDBWithDB 使用已有的 BadgerDB 实例创建 JMT 状态存储
func NewJMTStateDBWithDB(db *badger.DB, cfg JMTConfig) (*JMTStateDB, error) {
	prefix := cfg.Prefix
	if len(prefix) == 0 {
		prefix = []byte("jmt:")
	}

	store := NewVersionedBadgerStore(db, prefix)
	tree := NewJMT(store, sha256.New())

	// 尝试恢复最新版本
	if err := recoverLatestVersion(db, prefix, tree); err != nil {
		// 恢复失败不是致命错误，树会从空状态开始
		// 这在首次启动时是正常的
	}

	return &JMTStateDB{
		tree:   tree,
		store:  store,
		db:     db,
		ownsDB: false,
		prefix: prefix,
	}, nil
}

// recoverLatestVersion 从 BadgerDB 恢复最新版本和根哈希
func recoverLatestVersion(db *badger.DB, prefix []byte, tree *JellyfishMerkleTree) error {
	// 查找最新的根哈希记录
	// Key 格式: [prefix]root:v[version]
	rootPrefix := append(append([]byte(nil), prefix...), []byte("root:v")...)

	var latestVersion Version
	var latestRoot []byte

	err := db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Reverse = true
		opts.Prefix = rootPrefix

		it := txn.NewIterator(opts)
		defer it.Close()

		// Seek 到最大可能的版本
		seekKey := append(append([]byte(nil), rootPrefix...), 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF)
		it.Seek(seekKey)

		if it.Valid() {
			item := it.Item()
			key := item.Key()

			// 解析版本号
			if len(key) >= len(rootPrefix)+8 {
				versionBytes := key[len(key)-8:]
				latestVersion = Version(uint64(versionBytes[0])<<56 |
					uint64(versionBytes[1])<<48 |
					uint64(versionBytes[2])<<40 |
					uint64(versionBytes[3])<<32 |
					uint64(versionBytes[4])<<24 |
					uint64(versionBytes[5])<<16 |
					uint64(versionBytes[6])<<8 |
					uint64(versionBytes[7]))

				return item.Value(func(val []byte) error {
					latestRoot = append([]byte(nil), val...)
					return nil
				})
			}
		}
		return ErrNotFound
	})

	if err != nil {
		return err
	}

	// 恢复树的状态
	tree.mu.Lock()
	tree.version = latestVersion
	tree.root = latestRoot
	tree.rootHistory[latestVersion] = latestRoot
	tree.mu.Unlock()

	return nil
}

// ============================================
// 读取接口
// ============================================

// Get 获取最新版本的值
// 返回值：value, exists, error
func (s *JMTStateDB) Get(key string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	value, err := s.tree.Get([]byte(key), 0) // version=0 表示最新
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return value, true, nil
}

// GetAtVersion 获取指定版本的值
func (s *JMTStateDB) GetAtVersion(key string, version uint64) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.tree.Get([]byte(key), Version(version))
}

// Exists 检查 key 是否存在
func (s *JMTStateDB) Exists(key string) (bool, error) {
	_, exists, err := s.Get(key)
	return exists, err
}

// ============================================
// 写入接口
// ============================================

// ApplyAccountUpdate 批量更新状态（区块级别）
// height 作为版本号，所有更新在同一版本中原子提交
func (s *JMTStateDB) ApplyAccountUpdate(height uint64, kvs ...KVUpdate) error {
	if len(kvs) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	version := Version(height)

	// 分离更新和删除操作
	updateKeys := make([][]byte, 0, len(kvs))
	updateValues := make([][]byte, 0, len(kvs))
	deleteKeys := make([][]byte, 0)

	for _, kv := range kvs {
		if kv.Deleted {
			deleteKeys = append(deleteKeys, []byte(kv.Key))
		} else {
			updateKeys = append(updateKeys, []byte(kv.Key))
			updateValues = append(updateValues, kv.Value)
		}
	}

	var root []byte
	var err error

	// 先执行更新操作
	if len(updateKeys) > 0 {
		root, err = s.tree.Update(updateKeys, updateValues, version)
		if err != nil {
			return fmt.Errorf("JMT update failed: %w", err)
		}
	}

	// 再执行删除操作
	for _, key := range deleteKeys {
		root, err = s.tree.Delete(key, version)
		if err != nil {
			// 删除不存在的 key 不应报错
			if !errors.Is(err, ErrNotFound) {
				return fmt.Errorf("JMT delete failed: %w", err)
			}
		}
	}

	// 保存根哈希到 BadgerDB（如果有任何更新或删除）
	if len(updateKeys) > 0 || len(deleteKeys) > 0 {
		root = s.tree.Root() // 获取最新的根
		if err := s.saveRootHash(version, root); err != nil {
			return fmt.Errorf("failed to save root hash: %w", err)
		}
	}

	return nil
}

// saveRootHash 保存版本的根哈希
func (s *JMTStateDB) saveRootHash(version Version, root []byte) error {
	return s.db.Update(func(txn *badger.Txn) error {
		// Key 格式: [prefix]root:v[8-byte version]
		key := s.rootKey(version)
		return txn.Set(key, root)
	})
}

// rootKey 生成根哈希的存储 Key
func (s *JMTStateDB) rootKey(version Version) []byte {
	result := make([]byte, len(s.prefix)+6+8)
	copy(result, s.prefix)
	copy(result[len(s.prefix):], "root:v")
	// Big-endian 编码版本号
	vOffset := len(s.prefix) + 6
	result[vOffset] = byte(version >> 56)
	result[vOffset+1] = byte(version >> 48)
	result[vOffset+2] = byte(version >> 40)
	result[vOffset+3] = byte(version >> 32)
	result[vOffset+4] = byte(version >> 24)
	result[vOffset+5] = byte(version >> 16)
	result[vOffset+6] = byte(version >> 8)
	result[vOffset+7] = byte(version)
	return result
}

// ============================================
// 状态访问器
// ============================================

// Root 获取当前状态根
func (s *JMTStateDB) Root() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tree.Root()
}

// Version 获取当前版本（区块高度）
func (s *JMTStateDB) Version() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return uint64(s.tree.Version())
}

// GetRootHash 获取指定版本的根哈希
func (s *JMTStateDB) GetRootHash(version uint64) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tree.GetRootHash(Version(version))
}

// ============================================
// Proof 接口
// ============================================

// Prove 生成指定 key 的 Merkle Proof
func (s *JMTStateDB) Prove(key string) (*JMTProof, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tree.Prove([]byte(key))
}

// VerifyProof 验证 Merkle Proof
func (s *JMTStateDB) VerifyProof(proof *JMTProof, root []byte) bool {
	return VerifyJMTProof(proof, root, sha256.New())
}

// ============================================
// 管理接口
// ============================================

// Prune 清理指定版本之前的历史数据
func (s *JMTStateDB) Prune(version uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.Prune(Version(version))
}

// Close 关闭存储
func (s *JMTStateDB) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.store.Close(); err != nil {
		return err
	}

	if s.ownsDB && s.db != nil {
		return s.db.Close()
	}
	return nil
}

// ============================================
// 辅助方法
// ============================================

// IterateLatestSnapshot 遍历最新状态的所有数据
// 用于轻节点同步
func (s *JMTStateDB) IterateLatestSnapshot(fn func(key string, value []byte) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 遍历 BadgerDB 中的所有 value key
	// JMT 的值存储格式: [prefix]value:[key hash]
	valuePrefix := append(append([]byte(nil), s.prefix...), []byte("value:")...)

	return s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = valuePrefix

		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			err := item.Value(func(val []byte) error {
				// 从值中提取原始 key 和 value
				// 注意：这里简化实现，实际可能需要更复杂的解析
				key := string(item.Key()[len(valuePrefix):])
				return fn(key, val)
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// FlushAndRotate 兼容接口（JMT 不需要 Epoch 切换）
// 每次 Update 已经持久化，此方法为空操作
func (s *JMTStateDB) FlushAndRotate(epochEnd uint64) error {
	// JMT 不需要 Epoch 切换，每次 Update 已经持久化
	return nil
}
