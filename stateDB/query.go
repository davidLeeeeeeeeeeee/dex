// statedb/query.go
package statedb

import (
	"github.com/dgraph-io/badger/v2"
)

// Get 从 StateDB 读取单个 key 的值
// 查找顺序：1. 内存窗口 -> 2. 当前 Epoch overlay -> 3. 快照链
// 返回值：value, exists, error
func (s *DB) Get(key string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sh := shardOf(key, s.conf.ShardHexWidth)

	// 1. 先查内存窗口（最新的未持久化数据）
	if entry, ok := s.mem.get(sh, key); ok {
		if entry.del {
			return nil, false, nil // 已删除
		}
		return entry.latest, true, nil
	}

	// 2. 查当前 Epoch 的 overlay 和快照链
	var result []byte
	var found bool

	err := s.bdb.View(func(txn *badger.Txn) error {
		// 从当前 Epoch 开始，沿着快照链向前查找
		E := s.curEpoch
		for {
			// 先查 overlay
			ovlKey := kOvl(E, sh, key)
			if item, err := txn.Get(ovlKey); err == nil {
				return item.Value(func(v []byte) error {
					if len(v) == 0 {
						// 删除标记
						return nil
					}
					result = append([]byte(nil), v...)
					found = true
					return nil
				})
			}

			// 再查 snap
			snapKey := kSnap(E, sh, key)
			if item, err := txn.Get(snapKey); err == nil {
				return item.Value(func(v []byte) error {
					result = append([]byte(nil), v...)
					found = true
					return nil
				})
			}

			// 查找 base（上一个快照的 Epoch）
			baseKey := kMetaSnapBase(E)
			baseItem, err := txn.Get(baseKey)
			if err != nil {
				// 没有更早的快照了
				break
			}
			var baseE uint64
			if err := baseItem.Value(func(v []byte) error {
				baseE = bytesToU64(v)
				return nil
			}); err != nil {
				break
			}
			if baseE >= E {
				// 防止无限循环
				break
			}
			E = baseE
		}
		return nil
	})

	return result, found, err
}

// Exists 检查 key 是否存在
func (s *DB) Exists(key string) (bool, error) {
	_, exists, err := s.Get(key)
	return exists, err
}

// 拉取"当前 Epoch 内存 diff"的一页（用于 E..H 的增量）
func (s *DB) PageCurrentDiff(shard string, pageSize int, pageToken string) (Page, error) {
	if pageSize <= 0 {
		pageSize = s.conf.PageSize
	}
	startAfter := decodeToken(pageToken)
	items, lastKey := s.mem.snapshotShard(shard, startAfter, pageSize)
	p := Page{Items: items}
	if lastKey != "" {
		p.NextPageToken = encodeToken(lastKey)
	}
	return p, nil
}

// IterateLatestSnapshot 遍历最新快照的所有数据
// 用于轻节点同步：从 StateDB 获取所有状态数据（账户、订单、Token 等）
// 注意：这个方法遍历内存窗口的数据，不遍历快照数据
// 因为快照数据只包含账户数据（v1_account_*），而内存窗口包含所有状态数据
func (s *DB) IterateLatestSnapshot(fn func(key string, value []byte) error) error {
	// 遍历内存中的所有数据
	for _, shard := range s.shards {
		items, _ := s.mem.snapshotShard(shard, "", 100000)
		for _, item := range items {
			if !item.Deleted {
				if err := fn(item.Key, item.Value); err != nil {
					return err
				}
			}
		}
	}

	return nil
}
