// witness/types.go
// 见证者模块核心类型定义
package witness

import (
	"errors"
	"time"
)

// ===================== 错误定义 =====================

var (
	ErrWitnessNotFound       = errors.New("witness not found")
	ErrWitnessNotActive      = errors.New("witness is not active")
	ErrWitnessAlreadyActive  = errors.New("witness is already active")
	ErrInsufficientStake     = errors.New("insufficient stake amount")
	ErrUnstakingInProgress   = errors.New("unstaking already in progress")
	ErrPendingTasks          = errors.New("witness has pending tasks")
	ErrShelvedArbitrations   = errors.New("witness has shelved arbitrations")
	ErrLockPeriodNotExpired  = errors.New("lock period has not expired")
	ErrRequestNotFound       = errors.New("recharge request not found")
	ErrRequestAlreadyExists  = errors.New("recharge request already exists")
	ErrInvalidVote           = errors.New("invalid vote")
	ErrDuplicateVote         = errors.New("duplicate vote")
	ErrVotingPeriodExpired   = errors.New("voting period has expired")
	ErrNotSelectedWitness    = errors.New("not a selected witness for this request")
	ErrChallengeNotFound     = errors.New("challenge not found")
	ErrChallengeExists       = errors.New("challenge already exists")
	ErrNotInChallengePeriod  = errors.New("not in challenge period")
	ErrInsufficientChallenge = errors.New("insufficient challenge stake")
	ErrArbitrationExpired    = errors.New("arbitration period has expired")
	ErrNotArbitrator         = errors.New("not an arbitrator for this challenge")
	ErrConsensusNotReached   = errors.New("consensus not reached")
	ErrNativeChainNotSupport = errors.New("native chain not supported")
)

// ===================== 配置参数 =====================

// Config 见证者模块配置
type Config struct {
	// 共识参数
	ConsensusThreshold uint32 // 共识阈值百分比（默认80）
	AbstainThreshold   uint32 // 弃票阈值百分比（默认20）

	// 时间参数（区块数）
	VotingPeriodBlocks      uint64 // 投票期
	ChallengePeriodBlocks   uint64 // 公示期
	ArbitrationPeriodBlocks uint64 // 仲裁期
	UnstakeLockBlocks       uint64 // 解质押锁定期
	RetryIntervalBlocks     uint64 // 重试间隔（区块数）

	// 金额参数
	MinStakeAmount       string // 最小质押金额
	ChallengeStakeAmount string // 挑战质押金额

	// 见证者选择参数
	InitialWitnessCount uint32 // 初始见证者数量
	ExpandMultiplier    uint32 // 扩大范围倍数

	// 奖励参数
	WitnessRewardRatio float64 // 见证者奖励比例
	SlashRatio         float64 // 罚没比例
	ChallengerReward   float64 // 挑战者奖励比例（罚没金额的百分比）
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		ConsensusThreshold:      80,
		AbstainThreshold:        20,
		VotingPeriodBlocks:      100,                      // 约5分钟（假设3秒一个区块）
		ChallengePeriodBlocks:   600,                      // 约30分钟
		ArbitrationPeriodBlocks: 28800,                    // 约24小时
		UnstakeLockBlocks:       201600,                   // 约7天
		RetryIntervalBlocks:     20,                       // 约1分钟
		MinStakeAmount:          "1000000000000000000000", // 1000 Token (18位小数)
		ChallengeStakeAmount:    "100000000000000000000",  // 100 Token
		InitialWitnessCount:     10,
		ExpandMultiplier:        2,
		WitnessRewardRatio:      0.5,
		SlashRatio:              1.0,
		ChallengerReward:        0.2,
	}
}

// ===================== 公示期等级 =====================

// ChallengePeriodLevel 公示期等级（根据金额/风险）
type ChallengePeriodLevel int

const (
	ChallengePeriodShort  ChallengePeriodLevel = iota // 5分钟
	ChallengePeriodMedium                             // 30分钟
	ChallengePeriodLong                               // 24小时
)

// GetChallengePeriodBlocks 根据等级获取公示期区块数
func GetChallengePeriodBlocks(level ChallengePeriodLevel) uint64 {
	switch level {
	case ChallengePeriodShort:
		return 100 // 约5分钟
	case ChallengePeriodMedium:
		return 600 // 约30分钟
	case ChallengePeriodLong:
		return 28800 // 约24小时
	default:
		return 600
	}
}

// ===================== 共识阈值递减 =====================

// GetConsensusThreshold 根据仲裁轮次获取共识阈值
// 初始80% -> 70% -> 60% -> 50%
func GetConsensusThreshold(round uint32) uint32 {
	switch {
	case round == 0:
		return 80
	case round == 1:
		return 70
	case round == 2:
		return 60
	default:
		return 50
	}
}

// ===================== 支持的原生链 =====================

// NativeChain 原生链信息
type NativeChain struct {
	ID          string        // 链标识
	Name        string        // 链名称
	ConfirmTime time.Duration // 确认时间
	MinConfirms uint32        // 最小确认数
}

// SupportedChains 支持的原生链列表
var SupportedChains = map[string]*NativeChain{
	"BTC": {
		ID:          "BTC",
		Name:        "Bitcoin",
		ConfirmTime: 10 * time.Minute,
		MinConfirms: 6,
	},
	"ETH": {
		ID:          "ETH",
		Name:        "Ethereum",
		ConfirmTime: 15 * time.Second,
		MinConfirms: 12,
	},
	"BSC": {
		ID:          "BSC",
		Name:        "BNB Smart Chain",
		ConfirmTime: 3 * time.Second,
		MinConfirms: 15,
	},
	"TRON": {
		ID:          "TRON",
		Name:        "Tron",
		ConfirmTime: 3 * time.Second,
		MinConfirms: 19,
	},
}

// IsSupportedChain 检查是否支持该原生链
func IsSupportedChain(chainID string) bool {
	_, ok := SupportedChains[chainID]
	return ok
}

// ===================== 投票结果统计 =====================

// VoteStats 投票统计
type VoteStats struct {
	Total        uint32  // 总票数
	Pass         uint32  // 通过票
	Fail         uint32  // 拒绝票
	Abstain      uint32  // 弃票
	PassRatio    float64 // 通过率
	FailRatio    float64 // 拒绝率
	AbstainRatio float64 // 弃票率
}

// CalculateVoteStats 计算投票统计
func CalculateVoteStats(pass, fail, abstain, total uint32) *VoteStats {
	if total == 0 {
		return &VoteStats{}
	}
	return &VoteStats{
		Total:        total,
		Pass:         pass,
		Fail:         fail,
		Abstain:      abstain,
		PassRatio:    float64(pass) / float64(total) * 100,
		FailRatio:    float64(fail) / float64(total) * 100,
		AbstainRatio: float64(abstain) / float64(total) * 100,
	}
}

// ConsensusResult 共识结果
type ConsensusResult int

const (
	ConsensusNone     ConsensusResult = iota // 未达成共识
	ConsensusPass                            // 共识通过
	ConsensusFail                            // 共识拒绝
	ConsensusExpand                          // 需要扩大范围
	ConsensusConflict                        // 矛盾结果（自动挑战）
)

// DetermineConsensus 判断共识结果
func DetermineConsensus(stats *VoteStats, threshold uint32, isDeadline bool) ConsensusResult {
	thresholdFloat := float64(threshold)

	// 检查是否达到通过阈值
	if stats.PassRatio >= thresholdFloat {
		return ConsensusPass
	}

	// 检查是否达到拒绝阈值
	if stats.FailRatio >= thresholdFloat {
		return ConsensusFail
	}

	// 如果到达截止时间
	if isDeadline {
		// 检查是否有矛盾结果（既有通过票也有拒绝票）
		if stats.Pass > 0 && stats.Fail > 0 {
			return ConsensusConflict
		}

		// 弃票率超过阈值，需要扩大范围
		if stats.AbstainRatio > float64(100-threshold) {
			return ConsensusExpand
		}
	}

	return ConsensusNone
}

// ===================== 事件类型 =====================

// EventType 事件类型
type EventType string

const (
	EventWitnessStaked        EventType = "witness_staked"
	EventWitnessUnstaking     EventType = "witness_unstaking"
	EventWitnessExited        EventType = "witness_exited"
	EventWitnessSlashed       EventType = "witness_slashed"
	EventRechargeRequested    EventType = "recharge_requested"
	EventVoteReceived         EventType = "vote_received"
	EventConsensusReached     EventType = "consensus_reached"
	EventConsensusFailed      EventType = "consensus_failed"
	EventScopeExpanded        EventType = "scope_expanded"
	EventChallengePeriodStart EventType = "challenge_period_start"
	EventChallengeInitiated   EventType = "challenge_initiated"
	EventArbitrationStart     EventType = "arbitration_start"
	EventArbitrationVote      EventType = "arbitration_vote"
	EventChallengeResolved    EventType = "challenge_resolved"
	EventRechargeFinalized    EventType = "recharge_finalized"
	EventRechargeRejected     EventType = "recharge_rejected"
	EventRechargeShelved      EventType = "recharge_shelved"
)

// Event 事件
type Event struct {
	Type      EventType
	RequestID string
	Data      interface{}
	Height    uint64
	Timestamp int64
}
