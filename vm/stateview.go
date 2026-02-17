package vm

import (
	"sort"
	"strings"
	"sync"
)

// ========== StateView内部类型 ==========

// ovVal overlay中的值
type ovVal struct {
	val   []byte
	exist bool // false表示已删除
}

// change 变更记录，用于回滚
type change struct {
	key     string
	prev    ovVal
	hasPrev bool
}

// ========== StateView实现 ==========

// overlayStateView StateView的内存实现
type overlayStateView struct {
	mu        sync.RWMutex
	read      ReadThroughFn
	scan      ScanFn
	overlay   map[string]ovVal
	changelog []change
}

// NewStateView 创建新的StateView
func NewStateView(read ReadThroughFn, scan ScanFn) StateView {
	return &overlayStateView{
		read:      read,
		scan:      scan,
		overlay:   make(map[string]ovVal, 1024),
		changelog: make([]change, 0, 1024),
	}
}

func (s *overlayStateView) Get(key string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if v, ok := s.overlay[key]; ok {
		if !v.exist { // 已被标记删除
			return nil, false, nil
		}
		// 返回副本，避免外部修改
		result := make([]byte, len(v.val))
		copy(result, v.val)
		return result, true, nil
	}

	// 读穿到底层存储
	val, err := s.read(key)
	if err != nil {
		return nil, false, err
	}
	if val == nil {
		return nil, false, nil
	}
	return val, true, nil
}

func (s *overlayStateView) Set(key string, val []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	prev, has := s.overlay[key]
	s.changelog = append(s.changelog, change{key: key, prev: prev, hasPrev: has})
	// 复制值，避免外部修改影响内部状态
	valCopy := make([]byte, len(val))
	copy(valCopy, val)
	s.overlay[key] = ovVal{val: valCopy, exist: true}
}

// Scan scans all keys with the given prefix
func (s *overlayStateView) Scan(prefix string) (map[string][]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string][]byte)

	// First, get base results from underlying storage if scan function is available
	if s.scan != nil {
		base, err := s.scan(prefix)
		if err != nil {
			return nil, err
		}
		for k, v := range base {
			valCopy := make([]byte, len(v))
			copy(valCopy, v)
			result[k] = valCopy
		}
	}

	// Merge overlay entries
	for k, v := range s.overlay {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if !v.exist {
			delete(result, k)
			continue
		}
		valCopy := make([]byte, len(v.val))
		copy(valCopy, v.val)
		result[k] = valCopy
	}

	return result, nil
}

func (s *overlayStateView) Del(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	prev, has := s.overlay[key]
	s.changelog = append(s.changelog, change{key: key, prev: prev, hasPrev: has})
	s.overlay[key] = ovVal{val: nil, exist: false}
}

func (s *overlayStateView) Snapshot() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.changelog)
}

func (s *overlayStateView) Revert(snap int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if snap < 0 || snap > len(s.changelog) {
		return ErrInvalidSnapshot
	}

	// 回滚到snap之前的状态
	for i := len(s.changelog) - 1; i >= snap; i-- {
		c := s.changelog[i]
		if c.hasPrev {
			s.overlay[c.key] = c.prev
		} else {
			delete(s.overlay, c.key)
		}
	}
	s.changelog = s.changelog[:snap]
	return nil
}

func (s *overlayStateView) Diff() []WriteOp {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 收集所有 keys 并排序，确保确定性遍历顺序
	keys := make([]string, 0, len(s.overlay))
	for k := range s.overlay {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	diff := make([]WriteOp, 0, len(s.overlay))
	for _, k := range keys {
		v := s.overlay[k]
		// 不需要再次复制，因为 Set 已经复制过了
		diff = append(diff, WriteOp{
			Key:   k,
			Value: v.val,
			Del:   !v.exist,
		})
	}
	return diff
}
