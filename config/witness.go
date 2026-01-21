package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// WitnessConfig 见证者模块配置
type WitnessConfig struct {
	Enabled bool `json:"enabled"`

	// 共识参数
	ConsensusThreshold uint32 `json:"consensusThreshold"`
	AbstainThreshold   uint32 `json:"abstainThreshold"`

	// 时间参数（区块数）
	VotingPeriodBlocks      uint64 `json:"votingPeriodBlocks"`
	ChallengePeriodBlocks   uint64 `json:"challengePeriodBlocks"`
	ArbitrationPeriodBlocks uint64 `json:"arbitrationPeriodBlocks"`
	UnstakeLockBlocks       uint64 `json:"unstakeLockBlocks"`
	RetryIntervalBlocks     uint64 `json:"retryIntervalBlocks"`

	// 金额参数
	MinStakeAmount       string `json:"minStakeAmount"`
	ChallengeStakeAmount string `json:"challengeStakeAmount"`

	// 见证者选择参数
	InitialWitnessCount uint32 `json:"initialWitnessCount"`
	ExpandMultiplier    uint32 `json:"expandMultiplier"`

	// 奖励参数
	WitnessRewardRatio float64 `json:"witnessRewardRatio"`
	SlashRatio         float64 `json:"slashRatio"`
	ChallengerReward   float64 `json:"challengerReward"`
}

// ToWitnessConfig 转换为 witness 包的 Config
func (wc WitnessConfig) ToWitnessConfig() interface{} {
	// 由于循环引用，我们返回 interface{} 或者在 witness 包中定义处理逻辑
	// 更好的办法是让 witness 包直接引用这里的配置，或者在调用处转换
	return wc
}

// DefaultWitnessConfig 返回默认见证者配置
func DefaultWitnessConfig() WitnessConfig {
	return WitnessConfig{
		Enabled:                 true,
		ConsensusThreshold:      80,
		AbstainThreshold:        20,
		VotingPeriodBlocks:      100,
		ChallengePeriodBlocks:   600,
		ArbitrationPeriodBlocks: 28800,
		UnstakeLockBlocks:       201600,
		RetryIntervalBlocks:     20,
		// 1000 Token (18位小数) - 注意：这里为了方便测试，可以设置小一些
		MinStakeAmount:       "1000000000000000000000",
		ChallengeStakeAmount: "100000000000000000000",
		InitialWitnessCount:  10,
		ExpandMultiplier:     2,
		WitnessRewardRatio:   0.5,
		SlashRatio:           1.0,
		ChallengerReward:     0.2,
	}
}

// LoadWitnessConfig 从文件加载见证者配置
func LoadWitnessConfig(path string) (WitnessConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultWitnessConfig(), nil
		}
		return WitnessConfig{}, fmt.Errorf("failed to read witness config: %w", err)
	}

	var cfg WitnessConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return WitnessConfig{}, fmt.Errorf("failed to parse witness config: %w", err)
	}

	return cfg, nil
}
