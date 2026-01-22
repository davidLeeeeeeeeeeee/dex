// statedb/update.go
package statedb

import (
	"encoding/binary"
	"strings"

	"github.com/dgraph-io/badger/v4"
)

// ---- 辅助方法：判断 key 是否需要同步到 StateDB ----
// isStatefulKey 判断 key 是否属于需要同步到 StateDB 的状态数据
// 支持的前缀：account、freeze、order、token、recharge_record、token_registry
// 注意：需要排除历史记录类的 key（如 freeze_history、order_index 等）
func (s *DB) isStatefulKey(key string) bool {
	// 先排除历史记录类的 key 和索引类的 key
	excludePrefixes := []string{
		"v1_freeze_history_",    // 冻结历史（不是状态数据）
		"v1_transfer_history_",  // 转账历史
		"v1_miner_history_",     // 矿工历史
		"v1_candidate_history_", // 候选人历史
		"v1_recharge_history_",  // 充值历史
		"v1_pair:",              // 订单价格索引（不是状态数据）
		"v1_order_index_",       // 订单索引（如果有的话，不是状态数据）
	}

	for _, prefix := range excludePrefixes {
		if strings.HasPrefix(key, prefix) {
			return false
		}
	}

	// 定义需要同步到 StateDB 的数据前缀
	statefulPrefixes := []string{
		"v1_account_",         // 账户数据
		"v1_freeze_",          // 冻结标记（但不包括 freeze_history）
		"v1_order_",           // 订单数据（但不包括 pair: 索引）
		"v1_token_",           // Token 数据（包括 v1_token_registry）
		"v1_recharge_record_", // 充值记录（但不包括 recharge_history）
	}

	for _, prefix := range statefulPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}

	return false
}

// ---- 外部调用：管理更新 ----
// 在 db/manage_account.go 里每次账户变更都调用stateDB.ApplyAccountUpdate(height, key, val)
func (s *DB) ApplyAccountUpdate(height uint64, kvs ...KVUpdate) error {
	E := epochOf(height, s.conf.EpochSize)
	s.mu.Lock()
	if s.curEpoch == 0 {
		s.curEpoch = E
	} else if E != s.curEpoch {
		// 跨 Epoch 了，先.flush 上个 Epoch
		s.mu.Unlock()
		if err := s.FlushAndRotate(E - 1); err != nil {
			return err
		}
		s.mu.Lock()
		s.curEpoch = E
	}

	// 如果启用了 WAL，先写 WAL 再写内存
	if s.conf.UseWAL {
		// 确保 WAL 已打开（如果是新 Epoch 或首次写入）
		if s.wal == nil {
			wal, err := OpenWAL(s.conf.DataDir, E)
			if err != nil {
				s.mu.Unlock()
				return err
			}
			s.wal = wal
		}

		// 构造 WAL 记录
		var walRecords []WalRecord
		for _, kv := range kvs {
			// ✨ 使用 isStatefulKey 支持多种数据类型
			if !s.isStatefulKey(kv.Key) {
				continue // 跳过非状态数据
			}
			op := byte(0) // SET
			if kv.Deleted {
				op = 1 // DEL
			}
			walRecords = append(walRecords, WalRecord{
				Epoch: E,
				Op:    op,
				Key:   []byte(kv.Key),
				Value: kv.Value,
			})
		}

		// 先写 WAL（持久化）
		if len(walRecords) > 0 {
			if err := s.wal.AppendBatch(walRecords); err != nil {
				s.mu.Unlock()
				return err
			}
		}
	}

	// 写入内存窗口
	for _, kv := range kvs {
		// ✨ 使用 isStatefulKey 支持多种数据类型
		if !s.isStatefulKey(kv.Key) {
			continue // 跳过非状态数据
		}
		sh := shardOf(kv.Key, s.conf.ShardHexWidth)
		s.mem.apply(height, kv.Key, kv.Value, kv.Deleted, sh)
	}
	s.mu.Unlock()

	// 记录 h<->seq（一次批量写）
	return s.bdb.Update(func(txn *badger.Txn) error {
		seq, _ := s.seq.Next()
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, seq)
		hb := make([]byte, 8)
		binary.BigEndian.PutUint64(hb, height)
		if err := txn.Set(kMetaH2Seq(height), buf); err != nil {
			return err
		}
		if err := txn.Set(kMetaSeq2H(seq), hb); err != nil {
			return err
		}
		return nil
	})
}

// ---- 结束一个 Epoch：把内存 diff 固化为 overlay，并建立快照元数据 ----
func (s *DB) FlushAndRotate(epochEnd uint64) error {
	E := epochOf(epochEnd, s.conf.EpochSize)
	// 为 overlay 写 txn（分 shard 批量）
	err := s.bdb.Update(func(txn *badger.Txn) error {
		for _, shard := range s.shards {
			s.mem.muByShard[shard].RLock()
			bucket := s.mem.byShard[shard]
			var cnt int64
			for k, e := range bucket {
				// ✨ 直接使用完整的 key，支持多种数据类型
				if e.del {
					if err := txn.Delete(kOvl(E, shard, k)); err != nil && err != badger.ErrKeyNotFound {
						s.mem.muByShard[shard].RUnlock()
						return err
					}
				} else {
					if err := txn.Set(kOvl(E, shard, k), e.latest); err != nil {
						s.mem.muByShard[shard].RUnlock()
						return err
					}
				}
				cnt++
			}
			// 写 overlay shard 计数
			cntb := make([]byte, 8)
			binary.BigEndian.PutUint64(cntb, uint64(cnt))
			if err := txn.Set(kIdxOvl(E, shard), cntb); err != nil {
				s.mem.muByShard[shard].RUnlock()
				return err
			}
			s.mem.muByShard[shard].RUnlock()
		}
		// 建立快照的 base 链接（叠加式：指向上一个快照）
		if E >= s.conf.EpochSize {
			prev := E - s.conf.EpochSize
			if err := txn.Set(kMetaSnapBase(E), u64ToBytes(prev)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	// 清空内存窗口
	s.mem.clearAll()

	// 如果启用了 WAL，关闭并删除当前 Epoch 的 WAL
	if s.conf.UseWAL && s.wal != nil {
		// 关闭当前 WAL
		if err := s.wal.Close(); err != nil {
			return err
		}
		s.wal = nil

		// 删除已经持久化的 WAL 文件
		if err := RemoveWAL(s.conf.DataDir, E); err != nil {
			// 删除失败不影响主流程，只记录错误
			// 生产环境中应该记录日志
			_ = err
		}
	}

	return nil
}
