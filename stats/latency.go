package stats

import (
	"sort"
	"sync"
	"time"
)

// LatencySummary 单个指标的延迟分位统计
type LatencySummary struct {
	Count uint64        `json:"count"`
	P50   time.Duration `json:"p50"`
	P95   time.Duration `json:"p95"`
	P99   time.Duration `json:"p99"`
	Max   time.Duration `json:"max"`
}

type latencyMetric struct {
	samples []int64 // 纳秒，环形缓冲区
	nextIdx int
	filled  bool
	count   uint64
	maxNs   int64
}

// LatencyRecorder 固定容量延迟记录器（支持分位数）
type LatencyRecorder struct {
	mu       sync.RWMutex
	capacity int
	metrics  map[string]*latencyMetric
}

func NewLatencyRecorder(capacity int) *LatencyRecorder {
	if capacity <= 0 {
		capacity = 2048
	}
	return &LatencyRecorder{
		capacity: capacity,
		metrics:  make(map[string]*latencyMetric),
	}
}

func (r *LatencyRecorder) Record(name string, d time.Duration) {
	if r == nil || name == "" {
		return
	}
	ns := d.Nanoseconds()
	if ns < 0 {
		ns = 0
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	m, ok := r.metrics[name]
	if !ok {
		m = &latencyMetric{
			samples: make([]int64, r.capacity),
		}
		r.metrics[name] = m
	}

	m.samples[m.nextIdx] = ns
	m.nextIdx = (m.nextIdx + 1) % len(m.samples)
	if m.nextIdx == 0 {
		m.filled = true
	}
	m.count++
	if ns > m.maxNs {
		m.maxNs = ns
	}
}

// Snapshot 获取分位统计；reset=true 时清空样本与计数（用于区间监控）。
func (r *LatencyRecorder) Snapshot(reset bool) map[string]LatencySummary {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	result := make(map[string]LatencySummary, len(r.metrics))
	for name, m := range r.metrics {
		n := m.nextIdx
		if m.filled {
			n = len(m.samples)
		}
		if n == 0 {
			if reset {
				m.count = 0
				m.maxNs = 0
			}
			continue
		}

		values := make([]int64, n)
		copy(values, m.samples[:n])
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })

		summary := LatencySummary{
			Count: m.count,
			P50:   time.Duration(percentileValue(values, 0.50)),
			P95:   time.Duration(percentileValue(values, 0.95)),
			P99:   time.Duration(percentileValue(values, 0.99)),
			Max:   time.Duration(m.maxNs),
		}
		result[name] = summary

		if reset {
			m.nextIdx = 0
			m.filled = false
			m.count = 0
			m.maxNs = 0
		}
	}
	return result
}

func percentileValue(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(float64(len(sorted)-1) * p)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
