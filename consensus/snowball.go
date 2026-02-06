package consensus

import (
	"dex/interfaces"
	"dex/types"
	"strconv"
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

	// 1. 找到这一轮谁真实拿到了最多票 (采样集中的多数派)
	var winner string
	winnerVotes := 0
	for id, count := range votes {
		if count > winnerVotes {
			winner = id
			winnerVotes = count
		} else if count == winnerVotes && count > 0 {
			// 如果票数相等，通过规则打破平局，让各节点采样对齐
			winner = selectBestCandidate([]string{winner, id})
		}
	}

	if winnerVotes >= alpha {
		// --- 情况 A：采样成功，产生多数派赢家 ---
		if winner != sb.preference {
			// 监测到全网更强的偏好，或者从过时的低 Window 切换到高 Window
			sb.events.PublishAsync(types.BaseEvent{
				EventType: types.EventPreferenceChanged,
				EventData: PreferenceSwitch{
					BlockID:    sb.preference,
					Confidence: sb.confidence,
					Winner:     winner,
					Alpha:      alpha,
				},
			})
			sb.preference = winner
			sb.confidence = 1
			sb.successHistory = nil // 偏好切换，重置历史
			sb.successHistory = append(sb.successHistory, SuccessRound{
				Round:     1,
				Timestamp: time.Now().UnixMilli(),
				Votes:     voteDetails,
			})
		} else {
			// 维持偏好，增加置信度
			sb.confidence++
			sb.successHistory = append(sb.successHistory, SuccessRound{
				Round:     sb.confidence,
				Timestamp: time.Now().UnixMilli(),
				Votes:     voteDetails,
			})
		}
	} else {
		// --- 情况 B：采样失败，全网票数分散 (如 33/33/33 或旧 Window 抢占失败) ---
		// 此时重置信心，避免旧偏好由于“僵尸置信度”卡住共识
		if sb.confidence > 0 {
			sb.confidence = 0
			sb.successHistory = nil
		}

		// 关键破局逻辑：当没有产生明确赢家时，节点应撤离过时的窗口，
		// 向逻辑上最优的块 (Window 优先) 下齐，从而在下一轮瞬间形成多数派。
		if len(candidates) > 0 {
			best := selectBestCandidate(candidates)
			if sb.preference != best {
				sb.events.PublishAsync(types.BaseEvent{
					EventType: types.EventPreferenceChanged,
					EventData: PreferenceSwitch{
						BlockID:    sb.preference,
						Confidence: 0,
						Winner:     best,
						Alpha:      alpha,
					},
				})
				sb.preference = best
			}
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

// extractWindow 从 blockID 中提取 window 部分
// blockID 格式: block-<height>-<proposer>-w<window>-<hash>
func extractWindow(blockID string) int {
	idx := strings.LastIndex(blockID, "-w")
	if idx == -1 {
		return 999 // 无法解析
	}
	windowPart := blockID[idx+2:]
	nextDash := strings.Index(windowPart, "-")
	if nextDash != -1 {
		windowPart = windowPart[:nextDash]
	}
	w, err := strconv.Atoi(windowPart)
	if err != nil {
		return 999
	}
	return w
}

// selectBestCandidate 从候选列表中选择最优区块
// 优先级规则：Window 越大优先级越高（代表更近、更有机会成功的轮次）；
// Window 相同时，Hash 越小优先级越高（用于打破平局）。
func selectBestCandidate(candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	if len(candidates) == 1 {
		return candidates[0]
	}

	bestBlock := candidates[0]
	maxWindow := extractWindow(bestBlock)
	minHash := extractBlockHash(bestBlock)

	for _, cid := range candidates[1:] {
		w := extractWindow(cid)
		h := extractBlockHash(cid)

		// 关键修正：Window 越大，优先级越高
		if w > maxWindow {
			maxWindow = w
			minHash = h
			bestBlock = cid
		} else if w == maxWindow {
			if h < minHash {
				minHash = h
				bestBlock = cid
			}
		}
	}
	return bestBlock
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
