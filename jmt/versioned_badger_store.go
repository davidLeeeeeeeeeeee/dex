package smt

import (
	"encoding/binary"
	"errors"

	"github.com/dgraph-io/badger/v4"
)

// ============================================
// BadgerDB 版本化存储适配器
// ============================================

// VersionedBadgerStore 实现 VersionedStore 接口，使用 BadgerDB 作为后端
type VersionedBadgerStore struct {
	db     *badger.DB
	prefix []byte // 命名空间前缀
}

// NewVersionedBadgerStore 创建新的 BadgerDB 版本化存储
func NewVersionedBadgerStore(db *badger.DB, prefix []byte) *VersionedBadgerStore {
	return &VersionedBadgerStore{
		db:     db,
		prefix: prefix,
	}
}

// NewSession 创建一个新的存储会话
func (s *VersionedBadgerStore) NewSession() (VersionedStoreSession, error) {
	return &BadgerSession{
		s:   s,
		txn: s.db.NewTransaction(true),
	}, nil
}

// Get 获取指定版本的值
func (s *VersionedBadgerStore) Get(key []byte, version Version) ([]byte, error) {
	var result []byte
	err := s.db.View(func(txn *badger.Txn) error {
		val, err := s.getInternal(txn, key, version)
		if err != nil {
			return err
		}
		result = val
		return nil
	})

	if err != nil {
		return nil, s.mapError(err)
	}

	return result, nil
}

func (s *VersionedBadgerStore) mapError(err error) error {
	if errors.Is(err, badger.ErrKeyNotFound) {
		return ErrNotFound
	}
	return err
}

func (s *VersionedBadgerStore) getInternal(txn *badger.Txn, key []byte, version Version) ([]byte, error) {
	var result []byte
	var err error
	if version == 0 {
		err = s.getLatest(txn, key, &result)
	} else {
		err = s.getAtVersion(txn, key, version, &result)
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

// getLatest 获取最新版本的值
func (s *VersionedBadgerStore) getLatest(txn *badger.Txn, key []byte, result *[]byte) error {
	// 1. 尝试从元数据中获取最新版本号（O(1) 路径）
	latestVer, err := s.getLatestVersionInternal(txn, key)
	if err == nil {
		return s.getExactVersion(txn, key, latestVer, result)
	}

	// 2. 只有元数据不存在时，才回退到重量级的迭代器路径（保持向后兼容）
	fullKey := s.encodeKey(key)
	opts := badger.DefaultIteratorOptions
	opts.Reverse = true
	opts.Prefix = fullKey

	it := txn.NewIterator(opts)
	defer it.Close()

	seekKey := s.encodeVersionedKey(key, ^Version(0))
	it.Seek(seekKey)

	if !it.Valid() {
		return ErrNotFound
	}

	item := it.Item()
	return item.Value(func(val []byte) error {
		if len(val) == 1 && val[0] == 0xFF {
			return ErrNotFound
		}
		*result = append([]byte(nil), val...)
		return nil
	})
}

// getExactVersion 获取精确版本，不带回溯
func (s *VersionedBadgerStore) getExactVersion(txn *badger.Txn, key []byte, version Version, result *[]byte) error {
	versionedKey := s.encodeVersionedKey(key, version)
	item, err := txn.Get(versionedKey)
	if err != nil {
		return err
	}
	return item.Value(func(val []byte) error {
		if len(val) == 1 && val[0] == 0xFF {
			return ErrNotFound
		}
		*result = append([]byte(nil), val...)
		return nil
	})
}

// getAtVersion 获取指定版本或之前最近版本的值
func (s *VersionedBadgerStore) getAtVersion(txn *badger.Txn, key []byte, version Version, result *[]byte) error {
	// 尝试精确版本
	err := s.getExactVersion(txn, key, version, result)
	if err == nil {
		return nil
	}
	if !errors.Is(err, badger.ErrKeyNotFound) {
		return err
	}

	// 迭代器回溯
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

	item := it.Item()
	return item.Value(func(val []byte) error {
		if len(val) == 1 && val[0] == 0xFF {
			return ErrNotFound
		}
		*result = append([]byte(nil), val...)
		return nil
	})
}

// Set 设置指定版本的值
func (s *VersionedBadgerStore) Set(key []byte, value []byte, version Version) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return s.setInternal(txn, key, value, version)
	})
}

// Delete 删除指定版本
func (s *VersionedBadgerStore) Delete(key []byte, version Version) error {
	return s.db.Update(func(txn *badger.Txn) error {
		return s.deleteInternal(txn, key, version)
	})
}

func (s *VersionedBadgerStore) setInternal(txn *badger.Txn, key []byte, value []byte, version Version) error {
	versionedKey := s.encodeVersionedKey(key, version)
	if err := txn.Set(versionedKey, value); err != nil {
		return err
	}
	// 更新“最新版本”元数据
	return s.updateLatestVersionMetadata(txn, key, version)
}

func (s *VersionedBadgerStore) deleteInternal(txn *badger.Txn, key []byte, version Version) error {
	versionedKey := s.encodeVersionedKey(key, version)
	if err := txn.Set(versionedKey, []byte{0xFF}); err != nil {
		return err
	}
	return s.updateLatestVersionMetadata(txn, key, version)
}

func (s *VersionedBadgerStore) updateLatestVersionMetadata(txn *badger.Txn, key []byte, version Version) error {
	// 仅当新版本大于等于已知最新版本时才更新
	currentLatest, err := s.getLatestVersionInternal(txn, key)
	if err == nil && version < currentLatest {
		return nil
	}

	metaKey := s.encodeLatestMetaKey(key)
	verBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(verBuf, uint64(version))
	return txn.Set(metaKey, verBuf)
}

// GetLatestVersion 获取 Key 的最新版本号
func (s *VersionedBadgerStore) GetLatestVersion(key []byte) (Version, error) {
	var version Version
	err := s.db.View(func(txn *badger.Txn) error {
		v, err := s.getLatestVersionInternal(txn, key)
		if err != nil {
			return err
		}
		version = v
		return nil
	})
	return version, s.mapError(err)
}

func (s *VersionedBadgerStore) getLatestVersionInternal(txn *badger.Txn, key []byte) (Version, error) {
	metaKey := s.encodeLatestMetaKey(key)
	item, err := txn.Get(metaKey)
	if err != nil {
		return 0, err
	}
	var ver Version
	err = item.Value(func(val []byte) error {
		if len(val) != 8 {
			return errors.New("invalid meta version data")
		}
		ver = Version(binary.BigEndian.Uint64(val))
		return nil
	})
	return ver, err
}

// Prune 删除指定版本之前的所有旧版本
func (s *VersionedBadgerStore) Prune(version Version) error {
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

// encodeLatestMetaKey 编码“最新版本”元数据的 Key
// 格式: [prefix]L[originalKey]
func (s *VersionedBadgerStore) encodeLatestMetaKey(key []byte) []byte {
	result := make([]byte, len(s.prefix)+1+len(key))
	copy(result, s.prefix)
	result[len(s.prefix)] = 'L' // Metaverse Latest prefix
	copy(result[len(s.prefix)+1:], key)
	return result
}

// ============================================
// BadgerSession 实现
// ============================================

type BadgerSession struct {
	s   *VersionedBadgerStore
	txn *badger.Txn
}

func (b *BadgerSession) Get(key []byte, version Version) ([]byte, error) {
	return b.s.getInternal(b.txn, key, version)
}

func (b *BadgerSession) Set(key []byte, value []byte, version Version) error {
	return b.s.setInternal(b.txn, key, value, version)
}

func (b *BadgerSession) Delete(key []byte, version Version) error {
	return b.s.deleteInternal(b.txn, key, version)
}

func (b *BadgerSession) GetKV(key []byte) ([]byte, error) {
	fullKey := b.s.encodeKey(key)
	item, err := b.txn.Get(fullKey)
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil, nil // 保持接口一致，没找到返回 nil, nil
		}
		return nil, err
	}
	var val []byte
	err = item.Value(func(v []byte) error {
		val = append([]byte(nil), v...)
		return nil
	})
	return val, err
}

func (b *BadgerSession) Commit() error {
	return b.txn.Commit()
}

func (b *BadgerSession) Rollback() error {
	b.txn.Discard()
	return nil
}

func (b *BadgerSession) Close() error {
	b.txn.Discard()
	return nil
}
