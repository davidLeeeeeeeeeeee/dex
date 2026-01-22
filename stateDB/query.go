// statedb/query.go
package statedb

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
