package indexdb

import (
	"dex/db"
	"fmt"
	"os"
	"sync"

	"github.com/cockroachdb/pebble"
)

// IndexDB 是 Explorer 专用的索引数据库
type IndexDB struct {
	db     *pebble.DB
	path   string
	mu     sync.RWMutex
	nodeDB *db.Manager // 节点数据库引用（用于查询余额快照）
}

// New 创建新的索引数据库
func New(path string) (*IndexDB, error) {
	opts := &pebble.Options{
		MaxConcurrentCompactions: func() int { return 2 },
	}
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("failed to create indexdb dir: %w", err)
	}
	d, err := pebble.Open(path, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open indexdb: %w", err)
	}
	return &IndexDB{db: d, path: path}, nil
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
	return idb.db.Set([]byte(key), []byte(value), pebble.Sync)
}

// Get 获取值
func (idb *IndexDB) Get(key string) (string, error) {
	raw, closer, err := idb.db.Get([]byte(key))
	if err != nil {
		if err == pebble.ErrNotFound {
			return "", nil
		}
		return "", err
	}
	val := string(raw)
	closer.Close()
	return val, nil
}

// Delete 删除键
func (idb *IndexDB) Delete(key string) error {
	return idb.db.Delete([]byte(key), pebble.Sync)
}

// prefixUpperBound 计算前缀的上界
func prefixUpperBound(prefix []byte) []byte {
	upper := make([]byte, len(prefix))
	copy(upper, prefix)
	for i := len(upper) - 1; i >= 0; i-- {
		upper[i]++
		if upper[i] != 0 {
			return upper
		}
	}
	return nil
}

// ScanPrefix 扫描指定前缀的所有键值
func (idb *IndexDB) ScanPrefix(prefix string, limit int) ([]KV, error) {
	var results []KV
	p := []byte(prefix)
	iter, err := idb.db.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: prefixUpperBound(p)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	count := 0
	for iter.SeekGE(p); iter.Valid(); iter.Next() {
		if limit > 0 && count >= limit {
			break
		}
		key := string(iter.Key())
		val := string(iter.Value())
		results = append(results, KV{Key: key, Value: val})
		count++
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return results, nil
}

// ScanPrefixReverse 反向扫描指定前缀的所有键值（用于按时间倒序）
func (idb *IndexDB) ScanPrefixReverse(prefix string, limit int) ([]KV, error) {
	var results []KV
	p := []byte(prefix)
	upper := prefixUpperBound(p)
	iter, err := idb.db.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	count := 0
	for iter.SeekLT(upper); iter.Valid(); iter.Prev() {
		if limit > 0 && count >= limit {
			break
		}
		key := string(iter.Key())
		val := string(iter.Value())
		results = append(results, KV{Key: key, Value: val})
		count++
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return results, nil
}

// KV 键值对
type KV struct {
	Key   string
	Value string
}
