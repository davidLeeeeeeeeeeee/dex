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

