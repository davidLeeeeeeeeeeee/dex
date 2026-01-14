package vm

import (
	"math/big"

	"github.com/shopspring/decimal"
)

// BlockRewardParams 区块奖励参数
type BlockRewardParams struct {
	InitialReward      *big.Int
	HalvingInterval    uint64 // 减半周期（区块数）
	DecayRate          decimal.Decimal
	MinReward          *big.Int
	BlocksPerYear      uint64
	WitnessRewardRatio decimal.Decimal
}

// DefaultBlockRewardParams 默认奖励参数
var DefaultBlockRewardParams = BlockRewardParams{
	InitialReward:      big.NewInt(1000000000),    // 10 FB
	HalvingInterval:    31536000,                  // 约 1 年（按 1s 一块计算）
	DecayRate:          decimal.NewFromFloat(0.9), // 每年衰减到 90%
	MinReward:          big.NewInt(100000),        // 最小奖励
	BlocksPerYear:      31536000,
	WitnessRewardRatio: decimal.NewFromFloat(0.3), // 30% 给见证者
}

// CalculateBlockReward 计算给定高度的区块奖励
func CalculateBlockReward(height uint64, params BlockRewardParams) *big.Int {
	if params.BlocksPerYear == 0 {
		params.BlocksPerYear = 31536000
	}

	years := height / params.BlocksPerYear
	if years == 0 {
		return params.InitialReward
	}

	// 奖励 = InitialReward * (DecayRate ^ years)
	rewardDec := decimal.NewFromBigInt(params.InitialReward, 0)
	decayFactor := params.DecayRate.Pow(decimal.NewFromInt(int64(years)))

	finalRewardDec := rewardDec.Mul(decayFactor)
	finalReward := finalRewardDec.BigInt()

	if finalReward.Cmp(params.MinReward) < 0 {
		return params.MinReward
	}

	return finalReward
}
