package consensus

import (
	"dex/interfaces"
	"dex/types"
	"sync"
)

/* ----------------- 结构定义 ----------------- */

type PreferenceSwitch struct {
	BlockID    string // 被切换的区块ID
	Confidence int    // 切换时的置信度
	Winner     string
	Alpha      int
}

type NodeStats struct {
	Mu               sync.Mutex
	QueriesSent      uint32
	QueriesReceived  uint32
	ChitsResponded   uint32
	QueriesPerHeight map[uint64]uint32
	BlocksProposed   uint32
	GossipsReceived  uint32

	events interfaces.EventBus

	// ---- 环形缓冲区实现 ----
	prefHistBuf  []PreferenceSwitch // 固定容量缓冲区
	prefHistCap  int                // 容量（常量/可配置）
	prefHistHead int                // 下一个写入位置（0..cap-1）
	prefHistLen  int                // 当前已有元素个数（<= cap）
}

func NewNodeStats(events interfaces.EventBus) *NodeStats {

	historyCap := 256
	stats := &NodeStats{
		QueriesPerHeight: make(map[uint64]uint32),
		events:           events,
		prefHistCap:      historyCap,
	}
	stats.prefHistBuf = make([]PreferenceSwitch, historyCap)

	stats.events.Subscribe(types.EventPreferenceChanged, func(e interfaces.Event) {
		if preferenceSwitch, ok := e.Data().(PreferenceSwitch); ok {
			stats.pushPreferenceSwitch(preferenceSwitch)
		}
	})
	return stats
}

/* ----------------- 内部方法 ----------------- */

// O(1) 写入；满了覆盖最旧。
func (s *NodeStats) pushPreferenceSwitch(ps PreferenceSwitch) {
	if s.prefHistCap == 0 {
		return // 关闭历史记录
	}
	s.Mu.Lock()
	s.prefHistBuf[s.prefHistHead] = ps
	s.prefHistHead = (s.prefHistHead + 1) % s.prefHistCap
	if s.prefHistLen < s.prefHistCap {
		s.prefHistLen++
	}
	s.Mu.Unlock()
}

// 读取「从旧到新」的快照副本，避免外部改动内部缓冲。
func (s *NodeStats) GetPreferenceSwitchHistory() []PreferenceSwitch {
	return s.prefHistBuf
}

// CleanupOldHeights 清理低于指定高度的 QueriesPerHeight 数据，避免内存泄漏
func (s *NodeStats) CleanupOldHeights(belowHeight uint64) int {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	count := 0
	for h := range s.QueriesPerHeight {
		if h < belowHeight {
			delete(s.QueriesPerHeight, h)
			count++
		}
	}
	return count
}
