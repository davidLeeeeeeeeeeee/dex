// statedb/mem_window.go
package statedb

import "sync"

// ====== 内存窗口（当前 Epoch 的 diff）======

type memEntry struct {
	latest []byte
	del    bool
	// 可选：记录被触达的高度列表；若仅做"最终值 + 触达高度集合"，足够向外提供 diff 合并。
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
