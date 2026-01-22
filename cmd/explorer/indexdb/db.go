package indexdb

import (
	"fmt"
	"sync"

	"github.com/dgraph-io/badger/v4"
)

// IndexDB 是 Explorer 专用的索引数据库
type IndexDB struct {
	db   *badger.DB
	path string
	mu   sync.RWMutex
}

// New 创建新的索引数据库
func New(path string) (*IndexDB, error) {
	opts := badger.DefaultOptions(path)
	opts.Logger = nil // 禁用 badger 日志

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open indexdb: %w", err)
	}

	return &IndexDB{
		db:   db,
		path: path,
	}, nil
}

// Close 关闭数据库
func (idb *IndexDB) Close() error {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	if idb.db != nil {
		return idb.db.Close()
	}
	return nil
}

// Set 设置键值
func (idb *IndexDB) Set(key, value string) error {
	return idb.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(key), []byte(value))
	})
}

// Get 获取值
func (idb *IndexDB) Get(key string) (string, error) {
	var val string
	err := idb.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			val = string(v)
			return nil
		})
	})
	if err == badger.ErrKeyNotFound {
		return "", nil
	}
	return val, err
}

// Delete 删除键
func (idb *IndexDB) Delete(key string) error {
	return idb.db.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(key))
	})
}

// ScanPrefix 扫描指定前缀的所有键值
func (idb *IndexDB) ScanPrefix(prefix string, limit int) ([]KV, error) {
	var results []KV
	err := idb.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefix)
		it := txn.NewIterator(opts)
		defer it.Close()

		count := 0
		for it.Seek([]byte(prefix)); it.ValidForPrefix([]byte(prefix)); it.Next() {
			if limit > 0 && count >= limit {
				break
			}
			item := it.Item()
			key := string(item.Key())
			var val string
			err := item.Value(func(v []byte) error {
				val = string(v)
				return nil
			})
			if err != nil {
				return err
			}
			results = append(results, KV{Key: key, Value: val})
			count++
		}
		return nil
	})
	return results, err
}

// ScanPrefixReverse 反向扫描指定前缀的所有键值（用于按时间倒序）
func (idb *IndexDB) ScanPrefixReverse(prefix string, limit int) ([]KV, error) {
	var results []KV
	err := idb.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefix)
		opts.Reverse = true
		it := txn.NewIterator(opts)
		defer it.Close()

		// 找到前缀的最后一个可能的 key
		seekKey := append([]byte(prefix), 0xFF)
		count := 0
		for it.Seek(seekKey); it.ValidForPrefix([]byte(prefix)); it.Next() {
			if limit > 0 && count >= limit {
				break
			}
			item := it.Item()
			key := string(item.Key())
			var val string
			err := item.Value(func(v []byte) error {
				val = string(v)
				return nil
			})
			if err != nil {
				return err
			}
			results = append(results, KV{Key: key, Value: val})
			count++
		}
		return nil
	})
	return results, err
}

// KV 键值对
type KV struct {
	Key   string
	Value string
}
