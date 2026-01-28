package consensus

import (
	"dex/interfaces"
	"dex/types"
	"strings"
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

	// 关键修复：如果没有偏好，首先从所有候选中选择 hash 最小的作为初始偏好
	// 这确保了所有节点从相同的初始状态开始
	if sb.preference == "" && len(candidates) > 0 {
		sb.preference = selectByMinHash(candidates)
	}

	// 如果当前偏好不在候选列表中，重置为 hash 最小的候选
	// 这可能发生在区块被发现无效或网络分区后恢复
	if sb.preference != "" {
		inCandidates := false
		for _, c := range candidates {
			if c == sb.preference {
				inCandidates = true
				break
			}
		}
		if !inCandidates && len(candidates) > 0 {
			sb.preference = selectByMinHash(candidates)
			sb.confidence = 0
		}
	}

	// 找出最高票数
	maxVotes := 0
	for _, v := range votes {
		if v > maxVotes {
			maxVotes = v
		}
	}

	// 收集所有达到最高票数的候选（可能有多个并列）
	var topCandidates []string
	for cid, v := range votes {
		if v == maxVotes {
			topCandidates = append(topCandidates, cid)
		}
	}

	// 确定性选择：按 hash 最小
	var winner string
	if len(topCandidates) > 0 {
		winner = selectByMinHash(topCandidates)
	}

	if maxVotes >= alpha {
		// 关键：只有当 winner 获得足够票数时才考虑切换
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
	}
	// 移除票数不足时切换偏好的逻辑 - 这是导致不一致的根源
	// 票数不足 alpha 时，保持当前偏好不变，等待下一轮投票
}

// extractBlockHash 从 blockID 中提取 hash 部分
// blockID 格式: block-<height>-<proposer>-w<window>-<hash>
// 返回最后一个 "-" 后面的部分作为 hash
func extractBlockHash(blockID string) string {
	lastDash := strings.LastIndex(blockID, "-")
	if lastDash == -1 || lastDash == len(blockID)-1 {
		return blockID // 无法解析，返回原始 blockID
	}
	return blockID[lastDash+1:]
}

// selectByMinHash 从候选列表中选择 hash 最小的区块
// 使用字符串比较（hex 编码的 hash 字符串比较等价于数值比较）
func selectByMinHash(candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	if len(candidates) == 1 {
		return candidates[0]
	}

	minBlock := candidates[0]
	minHash := extractBlockHash(candidates[0])
	for _, cid := range candidates[1:] {
		h := extractBlockHash(cid)
		if h < minHash {
			minHash = h
			minBlock = cid
		}
	}
	return minBlock
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

// GetLastVotes 获取最后一轮每个区块的投票数
func (sb *Snowball) GetLastVotes() map[string]int {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	result := make(map[string]int, len(sb.lastVotes))
	for k, v := range sb.lastVotes {
		result[k] = v
	}
	return result
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
