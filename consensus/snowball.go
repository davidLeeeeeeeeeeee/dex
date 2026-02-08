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
	mu                  sync.RWMutex
	preference          string
	confidence          int
	finalized           bool
	events              interfaces.EventBus
	lastVotes           map[string]int // 对应区块这一轮有多少赞成
	successHistory      []SuccessRound // 对当前偏好的有效投票历史（偏好切换时清空）
	consecutiveFailures int            // 连续失败次数（用于活性逃生阀）
}

// WindowEscalationThreshold 连续失败多少轮后，才允许在失败路径中跨 Window 切换偏好。
// 必须远大于 Beta（最终化阈值），确保在任何分区中，另一个分区不可能已经最终化。
// 设置为 Beta*3 = 45：即使最快的分区在 15 轮内最终化，
// 慢分区至少需要 45 轮失败才会切换，此时快分区的最终化结果已传播到全网。
const WindowEscalationThreshold = 45

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
//
// 安全性关键设计：偏好切换只在两种情况下发生：
//   - Case A（成功路径）：某个候选获得了 >= Alpha 的多数票 → 切换到该赢家
//   - Case B（失败路径）：仅在 preference 为空时设置初始偏好；
//     已有偏好时，只在连续失败超过 WindowEscalationThreshold 后才允许跨 Window 切换
//
// 这确保了 Snowball 的核心安全性不变量：一旦足够多的节点偏好区块 X 并开始
// 积累 confidence，其他节点不会仅因 Window 号更大就"免费"切换到区块 Y，
// 从而防止双重最终化（split-brain）。
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
		// 这是唯一合法的偏好切换路径：看到了 Alpha 多数票证据
		sb.consecutiveFailures = 0 // 成功则重置失败计数

		if winner != sb.preference {
			// 监测到全网更强的偏好 → 根据真实投票证据切换
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
		// --- 情况 B：采样失败，全网票数分散 ---
		// 重置置信度（防止僵尸置信度卡住共识）
		if sb.confidence > 0 {
			sb.confidence = 0
			sb.successHistory = nil
		}
		sb.consecutiveFailures++

		// 安全性关键：失败路径中的偏好处理
		if sb.preference == "" && len(candidates) > 0 {
			// 尚无偏好 → 设置初始偏好（使用 selectBestCandidate 包含 Window 优先级）
			best := selectBestCandidate(candidates)
			sb.preference = best
		} else if sb.preference != "" && len(candidates) > 0 && sb.consecutiveFailures >= WindowEscalationThreshold {
			// 活性逃生阀：连续失败超过阈值 → 允许跨 Window 切换
			// 此时可以安全地认为旧偏好无法收敛，需要切换到新候选
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
				sb.consecutiveFailures = 0 // 切换后重置
			}
		}
		// 如果已有偏好且未达到逃生阈值 → 保持当前偏好不变！
		// 这是安全性的核心：不根据 Window 号"免费"切换偏好
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
