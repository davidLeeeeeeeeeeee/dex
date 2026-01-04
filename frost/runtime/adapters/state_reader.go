// frost/runtime/adapters/state_reader.go
// ChainStateReader 适配器实现（基于 StateDB/DB）
package adapters

import (
	"dex/frost/runtime"
	"strings"
)

// DBGetter 数据库读取接口（由外部实现）
type DBGetter interface {
	Get(key string) ([]byte, error)
	// Scan 扫描指定前缀的所有键值对
	Scan(prefix string) (map[string][]byte, error)
}

// StateDBReader 基于 StateDB/DB 的 ChainStateReader 实现
type StateDBReader struct {
	db DBGetter
}

// NewStateDBReader 创建新的 StateDBReader
func NewStateDBReader(db DBGetter) *StateDBReader {
	return &StateDBReader{db: db}
}

// Get 读取指定 key 的值
func (r *StateDBReader) Get(key string) ([]byte, bool, error) {
	data, err := r.db.Get(key)
	if err != nil {
		// 区分 key 不存在和读取错误
		if isNotFoundError(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if len(data) == 0 {
		return nil, false, nil
	}
	return data, true, nil
}

// Scan 扫描指定前缀的所有键值对
func (r *StateDBReader) Scan(prefix string, fn func(k string, v []byte) bool) error {
	results, err := r.db.Scan(prefix)
	if err != nil {
		return err
	}
	for k, v := range results {
		if !fn(k, v) {
			break
		}
	}
	return nil
}

// isNotFoundError 判断是否是 key 不存在错误
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "not found") ||
		strings.Contains(errStr, "key not found") ||
		strings.Contains(errStr, "does not exist")
}

// Ensure StateDBReader implements runtime.ChainStateReader
var _ runtime.ChainStateReader = (*StateDBReader)(nil)

// FakeStateReader 用于测试的 fake StateReader
type FakeStateReader struct {
	data map[string][]byte
}

// NewFakeStateReader 创建新的 FakeStateReader
func NewFakeStateReader() *FakeStateReader {
	return &FakeStateReader{
		data: make(map[string][]byte),
	}
}

// Set 设置键值对（测试用）
func (r *FakeStateReader) Set(key string, value []byte) {
	r.data[key] = value
}

// Get 读取指定 key 的值
func (r *FakeStateReader) Get(key string) ([]byte, bool, error) {
	v, ok := r.data[key]
	if !ok || len(v) == 0 {
		return nil, false, nil
	}
	return v, true, nil
}

// Scan 扫描指定前缀的所有键值对
func (r *FakeStateReader) Scan(prefix string, fn func(k string, v []byte) bool) error {
	for k, v := range r.data {
		if strings.HasPrefix(k, prefix) {
			if !fn(k, v) {
				break
			}
		}
	}
	return nil
}

// Ensure FakeStateReader implements runtime.ChainStateReader
var _ runtime.ChainStateReader = (*FakeStateReader)(nil)

