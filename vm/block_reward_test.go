package vm

import (
	"math/big"
	"testing"

	"github.com/shopspring/decimal"
)

func TestCalculateBlockReward_FirstYear(t *testing.T) {
	params := DefaultBlockRewardParams

	// 高度 0：第一年，应该返回初始奖励
	reward := CalculateBlockReward(0, params)
	if reward.Cmp(params.InitialReward) != 0 {
		t.Errorf("height 0: expected %s, got %s", params.InitialReward, reward)
	}

	// 高度 1000：仍在第一年
	reward = CalculateBlockReward(1000, params)
	if reward.Cmp(params.InitialReward) != 0 {
		t.Errorf("height 1000: expected %s, got %s", params.InitialReward, reward)
	}

	// 高度接近一年末（31535999）
	reward = CalculateBlockReward(31535999, params)
	if reward.Cmp(params.InitialReward) != 0 {
		t.Errorf("height 31535999: expected %s, got %s", params.InitialReward, reward)
	}
}

func TestCalculateBlockReward_SecondYear(t *testing.T) {
	params := DefaultBlockRewardParams

	// 高度正好一年（31536000）：进入第二年，衰减到 90%
	reward := CalculateBlockReward(31536000, params)
	expected := new(big.Int).Mul(params.InitialReward, big.NewInt(9))
	expected.Div(expected, big.NewInt(10)) // 90%

	if reward.Cmp(expected) != 0 {
		t.Errorf("height 31536000: expected %s, got %s", expected, reward)
	}
}

func TestCalculateBlockReward_ThirdYear(t *testing.T) {
	params := DefaultBlockRewardParams

	// 高度两年（63072000）：衰减到 81%
	reward := CalculateBlockReward(63072000, params)
	// 0.9 ^ 2 = 0.81
	expected := big.NewInt(810000000) // 1000000000 * 0.81

	if reward.Cmp(expected) != 0 {
		t.Errorf("height 63072000: expected %s, got %s", expected, reward)
	}
}

func TestCalculateBlockReward_MinReward(t *testing.T) {
	// 创建一个快速衰减的参数以测试最小奖励
	params := BlockRewardParams{
		InitialReward:   big.NewInt(1000),
		HalvingInterval: 100,
		DecayRate:       decimal.NewFromFloat(0.1), // 每周期衰减到 10%
		MinReward:       big.NewInt(50),
		BlocksPerYear:   100,
	}

	// 足够多年后应该触及最小奖励
	reward := CalculateBlockReward(500, params) // 5 年后: 1000 * 0.1^5 = 0.01 < 50
	if reward.Cmp(params.MinReward) != 0 {
		t.Errorf("expected min reward %s, got %s", params.MinReward, reward)
	}
}

func TestCalculateBlockReward_ZeroBlocksPerYear(t *testing.T) {
	params := BlockRewardParams{
		InitialReward:   big.NewInt(1000000000),
		DecayRate:       decimal.NewFromFloat(0.9),
		MinReward:       big.NewInt(100000),
		BlocksPerYear:   0, // 应该使用默认值
		HalvingInterval: 31536000,
	}

	reward := CalculateBlockReward(0, params)
	if reward.Cmp(params.InitialReward) != 0 {
		t.Errorf("expected initial reward %s, got %s", params.InitialReward, reward)
	}
}

func TestSafeAdd_Basic(t *testing.T) {
	a := big.NewInt(100)
	b := big.NewInt(200)

	result, err := SafeAdd(a, b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Cmp(big.NewInt(300)) != 0 {
		t.Errorf("expected 300, got %s", result)
	}
}

func TestSafeAdd_Overflow(t *testing.T) {
	a := new(big.Int).Set(MaxUint256)
	b := big.NewInt(1)

	_, err := SafeAdd(a, b)
	if err != ErrOverflow {
		t.Errorf("expected ErrOverflow, got %v", err)
	}
}

func TestSafeAdd_NilValues(t *testing.T) {
	result, err := SafeAdd(nil, big.NewInt(100))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Cmp(big.NewInt(100)) != 0 {
		t.Errorf("expected 100, got %s", result)
	}
}

func TestSafeSub_Basic(t *testing.T) {
	a := big.NewInt(300)
	b := big.NewInt(100)

	result, err := SafeSub(a, b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Cmp(big.NewInt(200)) != 0 {
		t.Errorf("expected 200, got %s", result)
	}
}

func TestSafeSub_Underflow(t *testing.T) {
	a := big.NewInt(100)
	b := big.NewInt(200)

	_, err := SafeSub(a, b)
	if err != ErrUnderflow {
		t.Errorf("expected ErrUnderflow, got %v", err)
	}
}

func TestParseBalance_Valid(t *testing.T) {
	balance, err := ParseBalance("12345678901234567890")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected, _ := new(big.Int).SetString("12345678901234567890", 10)
	if balance.Cmp(expected) != 0 {
		t.Errorf("expected %s, got %s", expected, balance)
	}
}
