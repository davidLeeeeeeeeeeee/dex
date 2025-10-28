package vm

import (
	"container/list"
	"sync"
)

// ========== LRU缓存实现 ==========

type lruCache struct {
	mu    sync.RWMutex
	cap   int
	ll    *list.List
	items map[string]*list.Element
}

type lruItem struct {
	key string
	val *SpecResult
}

// NewSpecExecLRU 创建LRU缓存
func NewSpecExecLRU(capacity int) SpecExecCache {
	if capacity <= 0 {
		capacity = 1024
	}
	return &lruCache{
		cap:   capacity,
		ll:    list.New(),
		items: make(map[string]*list.Element, capacity),
	}
}

// Get 获取缓存项
func (c *lruCache) Get(key string) (*SpecResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.items[key]; ok {
		c.ll.MoveToFront(e)
		return e.Value.(*lruItem).val, true
	}
	return nil, false
}

// Put 添加缓存项
func (c *lruCache) Put(res *SpecResult) {
	if res == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// 如果已存在，更新并移到前面
	if e, ok := c.items[res.BlockID]; ok {
		e.Value.(*lruItem).val = res
		c.ll.MoveToFront(e)
		return
	}

	// 添加新项
	e := c.ll.PushFront(&lruItem{key: res.BlockID, val: res})
	c.items[res.BlockID] = e

	// 如果超过容量，删除最久未使用的项
	if c.ll.Len() > c.cap {
		last := c.ll.Back()
		if last != nil {
			it := last.Value.(*lruItem)
			delete(c.items, it.key)
			c.ll.Remove(last)
		}
	}
}

// EvictBelow 清理低于指定高度的缓存项
func (c *lruCache) EvictBelow(height uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 创建待删除列表，避免在遍历中修改map
	toDelete := make([]string, 0)
	for k, e := range c.items {
		if e.Value.(*lruItem).val.Height < height {
			toDelete = append(toDelete, k)
		}
	}

	// 执行删除
	for _, k := range toDelete {
		if e, ok := c.items[k]; ok {
			delete(c.items, k)
			c.ll.Remove(e)
		}
	}
}

// Size 返回缓存大小
func (c *lruCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ll.Len()
}

// Clear 清空缓存
func (c *lruCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[string]*list.Element, c.cap)
	c.ll.Init()
}
