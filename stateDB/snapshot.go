// statedb/snapshot.go
package statedb

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"

	"github.com/dgraph-io/badger/v4"
)

// ---- 在线"全量快照"构建（可选：用于第一个快照或周期性瘦身）----
// 从主库（或你现有 account 源）全量扫描 account 键，分片写入 s1|snap|E|...
// 这里演示用 stateDB 自己来构造；你可替换为从主 DB 的迭代器输出
func (s *DB) BuildFullSnapshot(ctx context.Context, E uint64, iterAccount func(yield func(key string, val []byte) error) error) error {
	// iterAccount 由你传入：遍历出所有 v1_account_* 的 key,value
	return s.bdb.Update(func(txn *badger.Txn) error {
		// 清理旧的同 E 的 snap 索引（若存在）
		for _, shard := range s.shards {
			if err := txn.Delete(kIdxSnap(E, shard)); err != nil && err != badger.ErrKeyNotFound {
				return err
			}
		}
		countByShard := make(map[string]uint64, len(s.shards))
		err := iterAccount(func(key string, val []byte) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !bytes.HasPrefix([]byte(key), []byte(s.conf.AccountNSPrefix)) {
				return nil
			}
			addr := key[len(s.conf.AccountNSPrefix):]
			sh := shardOf(key, s.conf.ShardHexWidth)
			if err := txn.Set(kSnap(E, sh, addr), val); err != nil {
				return err
			}
			countByShard[sh]++
			return nil
		})
		if err != nil {
			return err
		}
		// 写每个 shard 的计数
		for sh, c := range countByShard {
			cb := make([]byte, 8)
			binary.BigEndian.PutUint64(cb, c)
			if err := txn.Set(kIdxSnap(E, sh), cb); err != nil {
				return err
			}
		}
		// 标记 base=E（全量快照的 base 指向自己）
		if err := txn.Set(kMetaSnapBase(E), u64ToBytes(E)); err != nil {
			return err
		}
		return nil
	})
}

// ---- 查询/服务端接口（给同步调用方用）----

// 列出一个 Epoch 的所有 shard（以及每个 shard 的数量）
func (s *DB) ListSnapshotShards(E uint64) ([]ShardInfo, error) {
	out := make([]ShardInfo, 0, len(s.shards))
	err := s.bdb.View(func(txn *badger.Txn) error {
		for _, sh := range s.shards {
			var c uint64
			if it, err := txn.Get(kIdxSnap(E, sh)); err == nil {
				_ = it.Value(func(v []byte) error {
					c = binary.BigEndian.Uint64(v)
					return nil
				})
			}
			var oc uint64
			if it, err := txn.Get(kIdxOvl(E, sh)); err == nil {
				_ = it.Value(func(v []byte) error {
					oc = binary.BigEndian.Uint64(v)
					return nil
				})
			}
			out = append(out, ShardInfo{
				Shard: sh, Count: int64(c + oc),
			})
		}
		return nil
	})
	return out, err
}

// 拉取某 Epoch 的某个 shard 的一页（合并 snap+overlay，overlay 覆盖 snap）
func (s *DB) PageSnapshotShard(E uint64, shard string, page, pageSize int, pageToken string) (Page, error) {
	if pageSize <= 0 {
		pageSize = s.conf.PageSize
	}
	startAfter := decodeToken(pageToken)

	// 先拉 snap，再用 ovl 覆盖；为了简洁这里做两次遍历并在内存 merge
	type item struct {
		key string
		val []byte
		del bool
	}
	merged := make(map[string]item, pageSize*2)

	err := s.bdb.View(func(txn *badger.Txn) error {
		// snap
		prefix := []byte(fmt.Sprintf("s1|snap|%020d|%s|", E, shard))
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		for it.Seek(append(prefix, []byte(startAfter)...)); it.ValidForPrefix(prefix); it.Next() {
			itemk := it.Item()
			k := itemk.KeyCopy(nil)
			// 解析 addr
			parts := bytes.SplitN(k, []byte("|"), 5)
			if len(parts) < 5 {
				continue
			}
			addr := string(parts[4])
			err := itemk.Value(func(v []byte) error {
				merged[addr] = item{key: s.conf.AccountNSPrefix + addr, val: append([]byte(nil), v...), del: false}
				return nil
			})
			if err != nil {
				return err
			}
			if len(merged) >= pageSize {
				break
			}
		}
		// overlay（覆盖）
		oprefix := []byte(fmt.Sprintf("s1|ovl|%020d|%s|", E, shard))
		oit := txn.NewIterator(badger.DefaultIteratorOptions)
		defer oit.Close()
		for oit.Seek(append(oprefix, []byte(startAfter)...)); oit.ValidForPrefix(oprefix); oit.Next() {
			itemk := oit.Item()
			k := itemk.KeyCopy(nil)
			parts := bytes.SplitN(k, []byte("|"), 5)
			if len(parts) < 5 {
				continue
			}
			addr := string(parts[4])
			err := itemk.Value(func(v []byte) error {
				if len(v) == 0 {
					merged[addr] = item{key: s.conf.AccountNSPrefix + addr, val: nil, del: true}
				} else {
					merged[addr] = item{key: s.conf.AccountNSPrefix + addr, val: append([]byte(nil), v...), del: false}
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Page{}, err
	}

	// 简化：不做稳定排序，按 map 顺序输出（生产请排序 addr）
	out := Page{Items: make([]KVUpdate, 0, len(merged))}
	last := ""
	for addr, it := range merged {
		out.Items = append(out.Items, KVUpdate{Key: it.key, Value: it.val, Deleted: it.del})
		last = addr
		if len(out.Items) >= pageSize {
			break
		}
	}
	if last != "" {
		out.NextPageToken = encodeToken(last)
	}
	// TotalPages 为估算（如需精确可读 idx 汇总 c+oc 再 /pageSize 向上取整）
	return out, nil
}

