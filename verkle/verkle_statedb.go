package verkle

import (
	"dex/config"
	"errors"
	"fmt"
	"sync"

	"github.com/dgraph-io/badger/v4"
)

// ============================================
// Verkle StateDB 适配层
// 提供与 JMT StateDB 兼容的接口
// ============================================

// KVUpdate 表示一个 KV 更新操作
type KVUpdate struct {
	Key     string
	Value   []byte
	Deleted bool
}

// VerkleConfig Verkle StateDB 配置
type VerkleConfig struct {
	DataDir string // BadgerDB 目录
	Prefix  []byte // 命名空间前缀，默认 "verkle:"
}

// VerkleStateDB 提供与 JMT StateDB 兼容的接口
type VerkleStateDB struct {
	tree   *VerkleTree
	store  *VersionedBadgerStore
	db     *badger.DB
	ownsDB bool // 是否由本实例管理 DB 生命周期
	prefix []byte
	mu     sync.RWMutex
}

// NewVerkleStateDB 创建 Verkle 状态存储（自己管理 BadgerDB）
func NewVerkleStateDB(cfg VerkleConfig) (*VerkleStateDB, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("DataDir is required")
	}

	cfgDefault := config.DefaultConfig()
	opts := badger.DefaultOptions(cfg.DataDir).
		WithNumVersionsToKeep(10).
		WithSyncWrites(false).
		WithLogger(nil)

	opts.ValueLogFileSize = cfgDefault.Database.ValueLogFileSize
	opts.BaseTableSize = cfgDefault.Database.BaseTableSize
	opts.MemTableSize = cfgDefault.Database.MemTableSize

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open badger db: %w", err)
	}

	stateDB, err := NewVerkleStateDBWithDB(db, cfg)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	stateDB.ownsDB = true
	return stateDB, nil
}

// NewVerkleStateDBWithDB 使用已有的 BadgerDB 实例创建 Verkle 状态存储
func NewVerkleStateDBWithDB(db *badger.DB, cfg VerkleConfig) (*VerkleStateDB, error) {
	prefix := cfg.Prefix
	if len(prefix) == 0 {
		prefix = []byte("verkle:")
	}

	store := NewVersionedBadgerStore(db, prefix)
	tree := NewVerkleTree(store)

	// 尝试恢复最新版本
	if err := recoverLatestVersion(db, prefix, tree); err != nil {
		// 恢复失败不是致命错误，树会从空状态开始
	}

	return &VerkleStateDB{
		tree:   tree,
		store:  store,
		db:     db,
		ownsDB: false,
		prefix: prefix,
	}, nil
}

// recoverLatestVersion 从 BadgerDB 恢复最新版本和根承诺
func recoverLatestVersion(db *badger.DB, prefix []byte, tree *VerkleTree) error {
	rootPrefix := append(append([]byte(nil), prefix...), []byte("root:v")...)

	var latestVersion Version
	var latestRoot []byte

	err := db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Reverse = true
		opts.Prefix = rootPrefix

		it := txn.NewIterator(opts)
		defer it.Close()

		seekKey := append(append([]byte(nil), rootPrefix...), 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF)
		it.Seek(seekKey)

		if it.Valid() {
			item := it.Item()
			key := item.Key()

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

	tree.CommitRoot(latestVersion, latestRoot)
	return nil
}

// ============================================
// 读取接口
// ============================================

// Get 获取最新版本的值
func (s *VerkleStateDB) Get(key string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	value, err := s.tree.Get([]byte(key), 0)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return value, true, nil
}

// GetAtVersion 获取指定版本的值
func (s *VerkleStateDB) GetAtVersion(key string, version uint64) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.tree.Get([]byte(key), Version(version))
}

// Exists 检查 key 是否存在
func (s *VerkleStateDB) Exists(key string) (bool, error) {
	_, exists, err := s.Get(key)
	return exists, err
}

// ============================================
// 写入接口
// ============================================

// ApplyAccountUpdate 批量更新状态
func (s *VerkleStateDB) ApplyAccountUpdate(height uint64, kvs ...KVUpdate) error {
	sess, err := s.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	if err := sess.ApplyUpdate(height, kvs...); err != nil {
		return err
	}
	return sess.Commit()
}

// ============================================
// VerkleStateDBSession 会话实现
// ============================================

type VerkleStateDBSession struct {
	db       *VerkleStateDB
	sess     VersionedStoreSession
	lastRoot []byte
}

func (s *VerkleStateDBSession) Get(key string) ([]byte, bool, error) {
	val, err := s.db.tree.GetWithSession(s.sess, []byte(key), 0)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return val, true, nil
}

func (s *VerkleStateDBSession) GetKV(key string) ([]byte, error) {
	return s.sess.GetKV([]byte(key))
}

func (s *VerkleStateDBSession) ApplyUpdate(height uint64, kvs ...KVUpdate) error {
	keys := make([][]byte, 0, len(kvs))
	vals := make([][]byte, 0, len(kvs))

	for _, kv := range kvs {
		if kv.Deleted {
			_, err := s.db.tree.DeleteWithSession(s.sess, []byte(kv.Key), Version(height))
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			continue
		}
		keys = append(keys, []byte(kv.Key))
		vals = append(vals, kv.Value)
	}

	if len(keys) > 0 {
		newRoot, err := s.db.tree.UpdateWithSession(s.sess, keys, vals, Version(height))
		if err != nil {
			return err
		}
		s.lastRoot = newRoot
	} else {
		s.lastRoot = s.db.tree.Root()
	}

	// 保存根承诺到 BadgerDB
	err := s.sess.Set(s.db.rootKey(Version(height)), s.lastRoot, Version(height))
	if err != nil {
		return fmt.Errorf("failed to save root hash in session: %w", err)
	}

	return nil
}

func (s *VerkleStateDBSession) Commit() error {
	return s.sess.Commit()
}

func (s *VerkleStateDBSession) Rollback() error {
	return s.sess.Rollback()
}

func (s *VerkleStateDBSession) Close() error {
	return s.sess.Close()
}

// NewSession 创建新的状态会话
func (s *VerkleStateDB) NewSession() (*VerkleStateDBSession, error) {
	storeSess, err := s.store.NewSession()
	if err != nil {
		return nil, err
	}
	return &VerkleStateDBSession{
		db:       s,
		sess:     storeSess,
		lastRoot: s.tree.Root(),
	}, nil
}

// Root 返回当前会话的状态根
func (s *VerkleStateDBSession) Root() []byte {
	return s.lastRoot
}

// ============================================
// 辅助方法
// ============================================

func (s *VerkleStateDB) rootKey(version Version) []byte {
	result := make([]byte, len(s.prefix)+6+8)
	copy(result, s.prefix)
	copy(result[len(s.prefix):], "root:v")
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
func (s *VerkleStateDB) Root() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tree.Root()
}

// CommitRoot 更新根承诺
func (s *VerkleStateDB) CommitRoot(version uint64, root []byte) {
	s.tree.CommitRoot(Version(version), root)
}

// Version 获取当前版本
func (s *VerkleStateDB) Version() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return uint64(s.tree.Version())
}

// GetRootHash 获取指定版本的根承诺
func (s *VerkleStateDB) GetRootHash(version uint64) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tree.GetRootHash(Version(version))
}

// ============================================
// Proof 接口
// ============================================

// Prove 生成指定 key 的 Verkle Proof
func (s *VerkleStateDB) Prove(key string) (*VerkleProof, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tree.Prove([]byte(key))
}

// VerifyProof 验证 Verkle Proof
func (s *VerkleStateDB) VerifyProof(proof *VerkleProof, root []byte) bool {
	return VerifyVerkleProof(proof, root)
}

// ============================================
// 管理接口
// ============================================

// Prune 清理指定版本之前的历史数据
func (s *VerkleStateDB) Prune(version uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.Prune(Version(version))
}

// Close 关闭存储
func (s *VerkleStateDB) Close() error {
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

// IterateLatestSnapshot 遍历最新状态的所有数据
func (s *VerkleStateDB) IterateLatestSnapshot(fn func(key string, value []byte) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	valuePrefix := append(append([]byte(nil), s.prefix...), []byte("value:")...)

	return s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = valuePrefix

		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			err := item.Value(func(val []byte) error {
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

// FlushAndRotate 兼容接口
func (s *VerkleStateDB) FlushAndRotate(epochEnd uint64) error {
	return nil
}
