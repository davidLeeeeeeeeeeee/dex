package verkle

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ============================================
// 版本化存储接口 (复用 JMT 设计)
// ============================================

// Version 表示树的版本号，通常对应区块高度
type Version uint64

// ErrNotFound 当 Key 不存在时返回
var ErrNotFound = errors.New("key not found")

// ErrVersionNotFound 当指定版本不存在时返回
var ErrVersionNotFound = errors.New("version not found")

// VersionedStore 是支持版本化的 KV 存储接口
type VersionedStore interface {
	// Get 获取指定版本的值
	Get(key []byte, version Version) ([]byte, error)

	// Set 在指定版本写入值
	Set(key []byte, value []byte, version Version) error

	// Delete 标记指定版本的 Key 为已删除
	Delete(key []byte, version Version) error

	// GetLatestVersion 获取指定 Key 的最新版本号
	GetLatestVersion(key []byte) (Version, error)

	// Prune 清理指定版本之前的所有历史数据
	Prune(version Version) error

	// Close 关闭存储
	Close() error

	// NewSession 创建一个新的存储会话
	NewSession() (VersionedStoreSession, error)
}

// VersionedStoreSession 是支持会话的存储接口
type VersionedStoreSession interface {
	// Get 获取指定版本的值
	Get(key []byte, version Version) ([]byte, error)

	// Set 在指定版本写入值
	Set(key []byte, value []byte, version Version) error

	// Delete 标记指定版本的 Key 为已删除
	Delete(key []byte, version Version) error

	// GetKV 直接获取原始 KV 数据
	GetKV(key []byte) ([]byte, error)

	// Commit 提交会话中的所有更改
	Commit() error

	// Rollback 撤销会话中的所有更改
	Rollback() error

	// Close 关闭会话
	Close() error
}

// ============================================
// 内存实现 (用于测试)
// ============================================

type versionedEntry struct {
	version Version
	value   []byte
	deleted bool
}

// SimpleVersionedMap 是一个简单的内存版本化存储实现
type SimpleVersionedMap struct {
	data map[string][]versionedEntry
}

// NewSimpleVersionedMap 创建新的内存版本化存储
func NewSimpleVersionedMap() *SimpleVersionedMap {
	return &SimpleVersionedMap{
		data: make(map[string][]versionedEntry),
	}
}

// Get 获取指定版本的值
func (m *SimpleVersionedMap) Get(key []byte, version Version) ([]byte, error) {
	keyStr := string(key)
	entries, ok := m.data[keyStr]
	if !ok || len(entries) == 0 {
		return nil, ErrNotFound
	}

	if version == 0 {
		latest := entries[len(entries)-1]
		if latest.deleted {
			return nil, ErrNotFound
		}
		return latest.value, nil
	}

	var found *versionedEntry
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].version <= version {
			found = &entries[i]
			break
		}
	}

	if found == nil {
		return nil, ErrVersionNotFound
	}
	if found.deleted {
		return nil, ErrNotFound
	}
	return found.value, nil
}

// Set 在指定版本写入值
func (m *SimpleVersionedMap) Set(key []byte, value []byte, version Version) error {
	keyStr := string(key)
	entry := versionedEntry{
		version: version,
		value:   append([]byte(nil), value...),
		deleted: false,
	}

	entries := m.data[keyStr]
	for i, e := range entries {
		if e.version == version {
			entries[i] = entry
			m.data[keyStr] = entries
			return nil
		}
	}

	entries = append(entries, entry)
	for i := len(entries) - 1; i > 0; i-- {
		if entries[i].version < entries[i-1].version {
			entries[i], entries[i-1] = entries[i-1], entries[i]
		} else {
			break
		}
	}
	m.data[keyStr] = entries
	return nil
}

// Delete 标记指定版本的 Key 为已删除
func (m *SimpleVersionedMap) Delete(key []byte, version Version) error {
	keyStr := string(key)
	entry := versionedEntry{
		version: version,
		value:   nil,
		deleted: true,
	}

	entries := m.data[keyStr]
	entries = append(entries, entry)
	for i := len(entries) - 1; i > 0; i-- {
		if entries[i].version < entries[i-1].version {
			entries[i], entries[i-1] = entries[i-1], entries[i]
		} else {
			break
		}
	}
	m.data[keyStr] = entries
	return nil
}

// GetLatestVersion 获取指定 Key 的最新版本号
func (m *SimpleVersionedMap) GetLatestVersion(key []byte) (Version, error) {
	keyStr := string(key)
	entries, ok := m.data[keyStr]
	if !ok || len(entries) == 0 {
		return 0, ErrNotFound
	}
	return entries[len(entries)-1].version, nil
}

// Prune 清理指定版本之前的所有历史数据
func (m *SimpleVersionedMap) Prune(version Version) error {
	for keyStr, entries := range m.data {
		var kept []versionedEntry
		for _, e := range entries {
			if e.version >= version {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(m.data, keyStr)
		} else {
			m.data[keyStr] = kept
		}
	}
	return nil
}

// Close 关闭存储
func (m *SimpleVersionedMap) Close() error {
	m.data = nil
	return nil
}

// NewSession 为内存实现创建新会话
func (m *SimpleVersionedMap) NewSession() (VersionedStoreSession, error) {
	return &simpleSession{m: m}, nil
}

type simpleSession struct {
	m *SimpleVersionedMap
}

func (s *simpleSession) Get(key []byte, version Version) ([]byte, error) {
	return s.m.Get(key, version)
}
func (s *simpleSession) Set(key []byte, value []byte, version Version) error {
	return s.m.Set(key, value, version)
}
func (s *simpleSession) Delete(key []byte, version Version) error { return s.m.Delete(key, version) }
func (s *simpleSession) GetKV(key []byte) ([]byte, error) {
	return s.m.Get(key, 0)
}
func (s *simpleSession) Commit() error   { return nil }
func (s *simpleSession) Rollback() error { return nil }
func (s *simpleSession) Close() error    { return nil }

// ============================================
// 版本化 Key 编码
// ============================================

// EncodeVersionedKey 将原始 Key 和版本号编码为版本化 Key
func EncodeVersionedKey(key []byte, version Version) []byte {
	result := make([]byte, len(key)+8)
	copy(result, key)
	binary.BigEndian.PutUint64(result[len(key):], uint64(version))
	return result
}

// DecodeVersionedKey 从版本化 Key 中解码原始 Key 和版本号
func DecodeVersionedKey(versionedKey []byte) (key []byte, version Version, err error) {
	if len(versionedKey) < 8 {
		return nil, 0, fmt.Errorf("versioned key too short: %d bytes", len(versionedKey))
	}
	keyLen := len(versionedKey) - 8
	key = versionedKey[:keyLen]
	version = Version(binary.BigEndian.Uint64(versionedKey[keyLen:]))
	return key, version, nil
}
