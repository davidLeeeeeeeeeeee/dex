package stats

import (
	"sync"
)

type Stats struct {
	statsLock     sync.RWMutex
	apiCallCounts map[string]uint64
}

func NewStats() *Stats {
	return &Stats{
		apiCallCounts: make(map[string]uint64),
	}
}

// 记录API调用
func (h *Stats) RecordAPICall(apiName string) {
	h.statsLock.Lock()
	defer h.statsLock.Unlock()

	if h.apiCallCounts == nil {
		h.apiCallCounts = make(map[string]uint64)
	}
	h.apiCallCounts[apiName]++
}

// 获取API调用统计
func (h *Stats) GetAPICallStats() map[string]uint64 {
	h.statsLock.RLock()
	defer h.statsLock.RUnlock()

	// 复制统计数据
	stats := make(map[string]uint64)
	for api, count := range h.apiCallCounts {
		stats[api] = count
	}
	return stats
}
