package smt

import (
	"encoding/binary"
	"errors"
	"sync"

	"github.com/dgraph-io/badger/v4"
)

// ============================================
// BadgerDB 版本化存储适配器
// ============================================

// VersionedBadgerStore 实现 VersionedStore 接口，使用 BadgerDB 作为后端
type VersionedBadgerStore struct {
	db     *badger.DB
	prefix []byte // 命名空间前缀
	mu     sync.RWMutex
}

// NewVersionedBadgerStore 创建新的 BadgerDB 版本化存储
func NewVersionedBadgerStore(db *badger.DB, prefix []byte) *VersionedBadgerStore {
	return &VersionedBadgerStore{
		db:     db,
		prefix: prefix,
	}
}

// Get 获取指定版本的值
// version=0 表示最新版本
func (s *VersionedBadgerStore) Get(key []byte, version Version) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []byte

	err := s.db.View(func(txn *badger.Txn) error {
		if version == 0 {
			// 查询最新版本
			return s.getLatest(txn, key, &result)
		}
		// 查询指定版本
		return s.getAtVersion(txn, key, version, &result)
	})

	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	return result, nil
}

// getLatest 获取最新版本的值
func (s *VersionedBadgerStore) getLatest(txn *badger.Txn, key []byte, result *[]byte) error {
	fullKey := s.encodeKey(key)

	// 使用前缀迭代器查找最新版本
	opts := badger.DefaultIteratorOptions
	opts.Reverse = true
	opts.Prefix = fullKey

	it := txn.NewIterator(opts)
	defer it.Close()

	// Seek 到 key 的末尾位置
	seekKey := s.encodeVersionedKey(key, ^Version(0))
	it.Seek(seekKey)

	if !it.Valid() {
		return ErrNotFound
	}

	item := it.Item()
	itemKey := item.Key()

	// 验证是否匹配
	if len(itemKey) < len(fullKey) {
		return ErrNotFound
	}

	return item.Value(func(val []byte) error {
		// 检查是否为删除标记
		if len(val) == 1 && val[0] == 0xFF {
			return ErrNotFound
		}
		*result = make([]byte, len(val))
		copy(*result, val)
		return nil
	})
}

// getAtVersion 获取指定版本或之前最近版本的值
func (s *VersionedBadgerStore) getAtVersion(txn *badger.Txn, key []byte, version Version, result *[]byte) error {
	// 先尝试精确版本
	versionedKey := s.encodeVersionedKey(key, version)
	item, err := txn.Get(versionedKey)
	if err == nil {
		return item.Value(func(val []byte) error {
			// 检查是否为删除标记
			if len(val) == 1 && val[0] == 0xFF {
				return ErrNotFound
			}
			*result = make([]byte, len(val))
			copy(*result, val)
			return nil
		})
	}

	if !errors.Is(err, badger.ErrKeyNotFound) {
		return err
	}

	// 使用迭代器查找该版本或之前的最近版本
	opts := badger.DefaultIteratorOptions
	opts.Reverse = true
	opts.Prefix = s.encodeKey(key)

	it := txn.NewIterator(opts)
	defer it.Close()

	seekKey := s.encodeVersionedKey(key, version)
	it.Seek(seekKey)

	if !it.Valid() {
		return ErrVersionNotFound
	}

	item = it.Item()
	return item.Value(func(val []byte) error {
		if len(val) == 1 && val[0] == 0xFF {
			return ErrNotFound
		}
		*result = make([]byte, len(val))
		copy(*result, val)
		return nil
	})
}

// Set 设置指定版本的值
func (s *VersionedBadgerStore) Set(key []byte, value []byte, version Version) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(txn *badger.Txn) error {
		versionedKey := s.encodeVersionedKey(key, version)
		return txn.Set(versionedKey, value)
	})
}

// Delete 删除指定版本（使用删除标记）
func (s *VersionedBadgerStore) Delete(key []byte, version Version) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(txn *badger.Txn) error {
		versionedKey := s.encodeVersionedKey(key, version)
		// 使用删除标记而不是真正删除，以支持历史版本查询
		return txn.Set(versionedKey, []byte{0xFF})
	})
}

// GetLatestVersion 获取 Key 的最新版本号
func (s *VersionedBadgerStore) GetLatestVersion(key []byte) (Version, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var version Version

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Reverse = true
		opts.Prefix = s.encodeKey(key)

		it := txn.NewIterator(opts)
		defer it.Close()

		seekKey := s.encodeVersionedKey(key, ^Version(0))
		it.Seek(seekKey)

		if !it.Valid() {
			return ErrNotFound
		}

		// 解析版本号
		itemKey := it.Item().Key()
		if len(itemKey) < 8 {
			return errors.New("invalid key format")
		}
		version = Version(binary.BigEndian.Uint64(itemKey[len(itemKey)-8:]))
		return nil
	})

	return version, err
}

// Prune 删除指定版本之前的所有旧版本
func (s *VersionedBadgerStore) Prune(version Version) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var keysToDelete [][]byte

	// 收集需要删除的键
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = s.prefix

		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			key := item.Key()

			if len(key) < 8 {
				continue
			}

			// 解析版本号
			keyVersion := Version(binary.BigEndian.Uint64(key[len(key)-8:]))
			if keyVersion < version {
				keyCopy := make([]byte, len(key))
				copy(keyCopy, key)
				keysToDelete = append(keysToDelete, keyCopy)
			}
		}
		return nil
	})

	if err != nil {
		return err
	}

	// 批量删除
	wb := s.db.NewWriteBatch()
	defer wb.Cancel()

	for _, key := range keysToDelete {
		if err := wb.Delete(key); err != nil {
			return err
		}
	}

	return wb.Flush()
}

// Close 关闭存储（不关闭 BadgerDB 本身）
func (s *VersionedBadgerStore) Close() error {
	// 不关闭 db，由外部管理
	return nil
}

// ============================================
// 内部辅助函数
// ============================================

// encodeKey 编码基本 Key（添加前缀）
func (s *VersionedBadgerStore) encodeKey(key []byte) []byte {
	result := make([]byte, len(s.prefix)+len(key))
	copy(result, s.prefix)
	copy(result[len(s.prefix):], key)
	return result
}

// encodeVersionedKey 编码带版本的 Key
// 格式: [prefix][originalKey][8-byte big-endian version]
func (s *VersionedBadgerStore) encodeVersionedKey(key []byte, version Version) []byte {
	result := make([]byte, len(s.prefix)+len(key)+8)
	copy(result, s.prefix)
	copy(result[len(s.prefix):], key)
	binary.BigEndian.PutUint64(result[len(s.prefix)+len(key):], uint64(version))
	return result
}
