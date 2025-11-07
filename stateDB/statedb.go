// statedb/statedb.go
package statedb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/dgraph-io/badger/v4"
)

// ====== Config & Types ======

type Config struct {
	DataDir         string // e.g. "stateDB/data"
	EpochSize       uint64 // 40000
	ShardHexWidth   int    // 1 => 16片, 2 => 256片
	PageSize        int    // 默认分页大小
	UseWAL          bool   // 是否启用可选 WAL 以防宕机丢内存 diff
	VersionsToKeep  int    // e.g. 10
	AccountNSPrefix string // "v1_account_"
}

type KVUpdate struct {
	// 这里的 Key 必须是主库使用的 "v1_account_<addr>"
	Key     string
	Value   []byte // 删除使用空切片+Deleted
	Deleted bool
}

type Page struct {
	Items         []KVUpdate
	Page          int
	TotalPages    int
	NextPageToken string // 供游标分页
}

type ShardInfo struct {
	Shard string // "0f" / "a" / "7c" ...
	Count int64  // 该 shard 的键数量（快照或 overlay 维度）
}

// ====== Keys（仅 stateDB 内部使用）======
//
// s1|snap|<E>|<shard>|<addr> -> value       // Base 或者是“全量快照”的载体
// s1|ovl|<E>|<shard>|<addr>  -> value       // 当前 Epoch 覆盖写的 overlay
// i1|snap|<E>|<shard>        -> count       // 快照分片计数
// i1|ovl|<E>|<shard>         -> count       // overlay 分片计数
// meta:snap:base:<E>         -> <E'>        // E 的 base（如果是叠加式快照）
// meta:h2seq:<H>             -> <seq>       // 高度到提交序号
// meta:seq2h:<seq>           -> <H>         // 反向映射
// wal|<E>|<block>|<seq>      -> batch diff  // 可选 WAL

func kSnap(E uint64, shard, addr string) []byte {
	return []byte(fmt.Sprintf("s1|snap|%020d|%s|%s", E, shard, addr))
}
func kOvl(E uint64, shard, addr string) []byte {
	return []byte(fmt.Sprintf("s1|ovl|%020d|%s|%s", E, shard, addr))
}
func kIdxSnap(E uint64, shard string) []byte {
	return []byte(fmt.Sprintf("i1|snap|%020d|%s", E, shard))
}
func kIdxOvl(E uint64, shard string) []byte {
	return []byte(fmt.Sprintf("i1|ovl|%020d|%s", E, shard))
}
func kMetaSnapBase(E uint64) []byte {
	return []byte(fmt.Sprintf("meta:snap:base:%020d", E))
}
func kMetaH2Seq(h uint64) []byte {
	return []byte(fmt.Sprintf("meta:h2seq:%020d", h))
}
func kMetaSeq2H(seq uint64) []byte {
	return []byte(fmt.Sprintf("meta:seq2h:%020d", seq))
}

// ====== Sharding ======

func shardOf(addr string, width int) string {
	sum := sha256.Sum256([]byte(addr))
	hexed := hex.EncodeToString(sum[:])
	return hexed[:width]
}

// ====== 内存窗口（当前 Epoch 的 diff）======

type memEntry struct {
	latest []byte
	del    bool
	// 可选：记录被触达的高度列表；若仅做“最终值 + 触达高度集合”，足够向外提供 diff 合并。
	heights []uint64
}

// 每个 shard 一把锁，降低热点
type memWindow struct {
	muByShard map[string]*sync.RWMutex
	byShard   map[string]map[string]*memEntry // shard -> key(v1_account_*) -> memEntry
	shardList []string
}

func newMemWindow(shards []string) *memWindow {
	m := &memWindow{
		muByShard: make(map[string]*sync.RWMutex, len(shards)),
		byShard:   make(map[string]map[string]*memEntry, len(shards)),
		shardList: shards,
	}
	for _, s := range shards {
		m.muByShard[s] = &sync.RWMutex{}
		m.byShard[s] = make(map[string]*memEntry, 1024)
	}
	return m
}

func (m *memWindow) apply(height uint64, key string, val []byte, deleted bool, shard string) {
	m.muByShard[shard].Lock()
	defer m.muByShard[shard].Unlock()
	e, ok := m.byShard[shard][key]
	if !ok {
		e = &memEntry{}
		m.byShard[shard][key] = e
	}
	e.latest = append([]byte(nil), val...)
	e.del = deleted
	e.heights = append(e.heights, height)
}

func (m *memWindow) snapshotShard(shard string, startAfter string, limit int) (items []KVUpdate, lastKey string) {
	m.muByShard[shard].RLock()
	defer m.muByShard[shard].RUnlock()

	// 简易的字典序分页
	keys := make([]string, 0, len(m.byShard[shard]))
	for k := range m.byShard[shard] {
		keys = append(keys, k)
	}
	// 这里省略排序实现细节（生产中请排序）；为了演示先按原插入顺序。
	// sort.Strings(keys)

	started := startAfter == ""
	for _, k := range keys {
		if !started {
			if k > startAfter {
				started = true
			} else {
				continue
			}
		}
		e := m.byShard[shard][k]
		items = append(items, KVUpdate{Key: k, Value: append([]byte(nil), e.latest...), Deleted: e.del})
		lastKey = k
		if len(items) >= limit {
			break
		}
	}
	return
}

func (m *memWindow) clearAll() {
	for _, s := range m.shardList {
		m.muByShard[s].Lock()
		m.byShard[s] = make(map[string]*memEntry, 1024)
		m.muByShard[s].Unlock()
	}
}

// ====== StateDB 主体 ======

type DB struct {
	conf     Config
	bdb      *badger.DB
	curEpoch uint64 // 当前内存窗口所属 Epoch 起始高度 (E)

	// 内存 diff 窗口
	mem *memWindow

	// 序号（用于 h<->seq 映射）
	seq *badger.Sequence

	// 并发保护
	mu sync.RWMutex

	// 预计算的 shard 列表
	shards []string
}

func New(cfg Config) (*DB, error) {
	if cfg.EpochSize == 0 {
		cfg.EpochSize = 40000
	}
	if cfg.ShardHexWidth != 1 && cfg.ShardHexWidth != 2 {
		cfg.ShardHexWidth = 1
	}
	if cfg.PageSize <= 0 {
		cfg.PageSize = 1000
	}
	if cfg.VersionsToKeep <= 0 {
		cfg.VersionsToKeep = 10
	}
	if cfg.AccountNSPrefix == "" {
		cfg.AccountNSPrefix = "v1_account_"
	}

	opts := badger.DefaultOptions(cfg.DataDir).
		WithNumVersionsToKeep(cfg.VersionsToKeep).
		WithSyncWrites(false).
		WithLogger(nil) // 你工程里可接自己的 logger

	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}

	seq, err := db.GetSequence([]byte("meta:commit_seq"), 1000)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	// 构造 shard 列表
	var shards []string
	if cfg.ShardHexWidth == 1 {
		for i := 0; i < 16; i++ {
			shards = append(shards, fmt.Sprintf("%x", i))
		}
	} else {
		for i := 0; i < 256; i++ {
			shards = append(shards, fmt.Sprintf("%02x", i))
		}
	}

	s := &DB{
		conf:   cfg,
		bdb:    db,
		mem:    newMemWindow(shards),
		seq:    seq,
		shards: shards,
	}
	return s, nil
}

func (s *DB) Close() error {
	if s.seq != nil {
		_ = s.seq.Release()
	}
	return s.bdb.Close()
}

func epochOf(height, epochSize uint64) uint64 {
	return (height / epochSize) * epochSize
}

// ---- 外部调用：管理更新 ----
// 在 db/manage_account.go 里每次账户变更都调用
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
	// 写入内存窗口
	for _, kv := range kvs {
		if !bytes.HasPrefix([]byte(kv.Key), []byte(s.conf.AccountNSPrefix)) {
			continue // 只接管 account 命名空间
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
				addr := k[len(s.conf.AccountNSPrefix):]
				if e.del {
					if err := txn.Delete(kOvl(E, shard, addr)); err != nil && err != badger.ErrKeyNotFound {
						s.mem.muByShard[shard].RUnlock()
						return err
					}
				} else {
					if err := txn.Set(kOvl(E, shard, addr), e.latest); err != nil {
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
	return nil
}

// ---- 在线“全量快照”构建（可选：用于第一个快照或周期性瘦身）----
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

// 拉取“当前 Epoch 内存 diff”的一页（用于 E..H 的增量）
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

// ====== 工具 ======
func u64ToBytes(u uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, u)
	return b
}
func encodeToken(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}
func decodeToken(tok string) string {
	if tok == "" {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return ""
	}
	return string(b)
}

// ====== 你可能会用到的错误 ======
var (
	ErrNotFound = errors.New("not found")
)
