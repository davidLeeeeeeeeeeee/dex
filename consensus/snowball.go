package consensus

import (
	"dex/interfaces"
	"dex/types"
	"strings"
	"sync"
	"time"
)

// ============================================
// Snowball 共识算法核心
// ============================================

// SuccessRound 记录一轮成功的投票（达到 alpha 阈值）
type SuccessRound struct {
	Round     int                      // 从 1 开始的有效轮次号
	Timestamp int64                    // 时间戳
	Votes     []types.FinalizationChit // 本轮的投票详情
}

type Snowball struct {
	mu             sync.RWMutex
	preference     string
	confidence     int
	finalized      bool
	events         interfaces.EventBus
	lastVotes      map[string]int // 对应区块这一轮有多少赞成
	successHistory []SuccessRound // 对当前偏好的有效投票历史（偏好切换时清空）
}

func NewSnowball(events interfaces.EventBus) *Snowball {
	return &Snowball{
		events:         events,
		lastVotes:      make(map[string]int),
		successHistory: make([]SuccessRound, 0),
	}
}

type PreferenceChangedData struct {
	BlockId    string // "success" | "timeout"
	Confidence int    // 结束的查询键（可选）
}

// RecordVoteWithDetails 记录投票并保存投票详情（用于最终化证据）
func (sb *Snowball) RecordVoteWithDetails(candidates []string, votes map[string]int, alpha int, voteDetails []types.FinalizationChit) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	sb.lastVotes = votes

	// 确定性增强：如果当前偏好存在，但它不是所有候选集中 hash 最小的那个，
	// 我们必须强制切换到最小的，否则节点会因为"赢家（最小Hash）"票数不足 Alpha 而卡在旧偏好上。
	if sb.preference != "" && len(candidates) > 0 {
		minBlock := selectByMinHash(candidates)
		if sb.preference != minBlock {
			sb.events.PublishAsync(types.BaseEvent{
				EventType: types.EventPreferenceChanged,
				EventData: PreferenceSwitch{BlockID: sb.preference, Confidence: sb.confidence, Winner: minBlock, Alpha: alpha},
			})
			sb.preference = minBlock
			sb.confidence = 0
			sb.successHistory = nil // 偏好切换，清空历史
		}
	}

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
			sb.successHistory = nil // 偏好切换，清空历史
		}
	}

	// 确定性选择：直接选择所有候选区块中 hash 最小的作为本轮"赢家"
	var winner string
	winnerVotes := 0
	if len(candidates) > 0 {
		winner = selectByMinHash(candidates)
		winnerVotes = votes[winner]
	}

	if winnerVotes >= alpha {
		// 关键：只有当这个最小 hash 的区块获得足够票数时才考虑切换或增加置信度
		if winner != sb.preference {
			sb.events.PublishAsync(types.BaseEvent{
				EventType: types.EventPreferenceChanged,
				EventData: PreferenceSwitch{BlockID: sb.preference, Confidence: sb.confidence, Winner: winner, Alpha: alpha},
			})
			sb.preference = winner
			sb.confidence = 1
			sb.successHistory = nil // 偏好切换，清空历史
			// 记录第一轮有效投票
			sb.successHistory = append(sb.successHistory, SuccessRound{
				Round:     1,
				Timestamp: time.Now().UnixMilli(),
				Votes:     voteDetails,
			})
		} else {
			sb.confidence++
			// 记录有效投票轮次
			sb.successHistory = append(sb.successHistory, SuccessRound{
				Round:     sb.confidence,
				Timestamp: time.Now().UnixMilli(),
				Votes:     voteDetails,
			})
		}
	} else {
		// 票数不足 alpha，置信度重置
		if sb.confidence > 0 {
			sb.confidence = 0
			sb.successHistory = nil // 失败，清空历史
		}
	}
}

// RecordVote 兼容旧接口（不记录投票详情）
func (sb *Snowball) RecordVote(candidates []string, votes map[string]int, alpha int) {
	sb.RecordVoteWithDetails(candidates, votes, alpha, nil)
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

// GetSuccessHistory 获取有效轮次历史（用于最终化证据）
func (sb *Snowball) GetSuccessHistory() []SuccessRound {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	result := make([]SuccessRound, len(sb.successHistory))
	copy(result, sb.successHistory)
	return result
}
