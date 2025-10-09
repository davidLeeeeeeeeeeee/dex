package consensus

import (
	"dex/interfaces"
	"dex/types"
	"sort"
	"sync"
)

// ============================================
// Snowball 共识算法核心
// ============================================

type Snowball struct {
	mu         sync.RWMutex
	preference string
	confidence int
	finalized  bool
	events     interfaces.EventBus
	lastVotes  map[string]int // 对应区块这一轮有多少赞成
}

func NewSnowball(events interfaces.EventBus) *Snowball {
	return &Snowball{
		events:    events,
		lastVotes: make(map[string]int),
	}
}

type PreferenceChangedData struct {
	BlockId    string // "success" | "timeout"
	Confidence int    // 结束的查询键（可选）
}

func (sb *Snowball) RecordVote(candidates []string, votes map[string]int, alpha int) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	sb.lastVotes = votes

	var winner string
	maxVotes := 0
	for cid, v := range votes {
		if v > maxVotes {
			maxVotes = v
			winner = cid
		}
	}

	if maxVotes >= alpha {
		if winner != sb.preference {
			sb.events.PublishAsync(types.BaseEvent{
				EventType: types.EventPreferenceChanged,
				EventData: PreferenceSwitch{BlockID: sb.preference, Confidence: sb.confidence, Winner: winner, Alpha: alpha},
			})
			sb.preference = winner
			sb.confidence = 1
		} else {
			sb.confidence++
		}
	} else {
		if len(candidates) > 0 {
			sort.Strings(candidates)
			largestBlock := candidates[len(candidates)-1]
			if largestBlock != sb.preference {
				sb.events.PublishAsync(types.BaseEvent{
					EventType: types.EventPreferenceChanged,
					EventData: PreferenceSwitch{BlockID: sb.preference, Confidence: sb.confidence, Winner: winner, Alpha: alpha},
				})
				sb.preference = largestBlock
				sb.confidence = 0
			}
		}
	}
}

func (sb *Snowball) GetPreference() string {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	return sb.preference
}

func (sb *Snowball) GetConfidence() int {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	return sb.confidence
}

func (sb *Snowball) CanFinalize(beta int) bool {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	return sb.confidence >= beta && !sb.finalized
}

func (sb *Snowball) Finalize() {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.finalized = true
}

func (sb *Snowball) IsFinalized() bool {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	return sb.finalized
}
