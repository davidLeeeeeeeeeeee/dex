package vm

import (
	"errors"
	"math/big"
)

// safe_math.go 提供带溢出检查的 big.Int 运算
// 用于 VM 中余额、质押等资产相关的安全运算

var (
	// ErrOverflow 加法溢出错误
	ErrOverflow = errors.New("arithmetic overflow")
	// ErrUnderflow 减法下溢错误（结果为负数）
	ErrUnderflow = errors.New("arithmetic underflow")
	// ErrInvalidBalance 无效的余额格式
	ErrInvalidBalance = errors.New("invalid balance format")
	// ErrBalanceTooLong 余额字符串过长
	ErrBalanceTooLong = errors.New("balance string too long")
	// ErrNegativeBalance 余额不能为负
	ErrNegativeBalance = errors.New("negative balance not allowed")
)

// MaxBalanceStringLen 余额字符串最大长度（78 字符足够表示 2^256-1）
const MaxBalanceStringLen = 78

// MaxUint256 是 256 位无符号整数的最大值
// 用作余额上限，防止溢出
var MaxUint256 = func() *big.Int {
	max := new(big.Int)
	max.Exp(big.NewInt(2), big.NewInt(256), nil)
	max.Sub(max, big.NewInt(1))
	return max
}()

// SafeAdd 安全加法：a + b
// 返回 (结果, 错误)
// 如果结果超过 MaxUint256，返回 ErrOverflow
func SafeAdd(a, b *big.Int) (*big.Int, error) {
	if a == nil {
		a = big.NewInt(0)
	}
	if b == nil {
		b = big.NewInt(0)
	}

	// 检查是否有负数
	if a.Sign() < 0 || b.Sign() < 0 {
		return nil, errors.New("negative value not allowed")
	}

	result := new(big.Int).Add(a, b)

	// 检查是否超过最大值
	if result.Cmp(MaxUint256) > 0 {
		return nil, ErrOverflow
	}

	return result, nil
}

// SafeSub 安全减法：a - b
// 返回 (结果, 错误)
// 如果 a < b，返回 ErrUnderflow
func SafeSub(a, b *big.Int) (*big.Int, error) {
	if a == nil {
		a = big.NewInt(0)
	}
	if b == nil {
		b = big.NewInt(0)
	}

	// 检查是否有负数
	if a.Sign() < 0 || b.Sign() < 0 {
		return nil, errors.New("negative value not allowed")
	}

	// 检查是否会下溢
	if a.Cmp(b) < 0 {
		return nil, ErrUnderflow
	}

	result := new(big.Int).Sub(a, b)
	return result, nil
}

// MustAdd 安全加法，panic 版本（仅用于已验证不会溢出的场景）
func MustAdd(a, b *big.Int) *big.Int {
	result, err := SafeAdd(a, b)
	if err != nil {
		panic(err)
	}
	return result
}

// MustSub 安全减法，panic 版本（仅用于已验证不会下溢的场景）
func MustSub(a, b *big.Int) *big.Int {
	result, err := SafeSub(a, b)
	if err != nil {
		panic(err)
	}
	return result
}

// ParseBalance 安全解析余额字符串
// 验证：
// 1. 长度不超过 MaxBalanceStringLen (78字符)
// 2. 是有效的十进制数字字符串
// 3. 非负数
// 4. 不超过 MaxUint256
func ParseBalance(s string) (*big.Int, error) {
	// 空字符串视为 0
	if s == "" {
		return big.NewInt(0), nil
	}

	// 检查长度
	if len(s) > MaxBalanceStringLen {
		return nil, ErrBalanceTooLong
	}

	// 检查是否只包含数字（不允许前导负号、空格等）
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return nil, ErrInvalidBalance
		}
	}

	// 解析为 big.Int
	balance, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, ErrInvalidBalance
	}

	// 检查非负（虽然上面已过滤负号，但双重保险）
	if balance.Sign() < 0 {
		return nil, ErrNegativeBalance
	}

	// 检查不超过最大值
	if balance.Cmp(MaxUint256) > 0 {
		return nil, ErrOverflow
	}

	return balance, nil
}

// ParseBalanceOrZero 解析余额，失败时返回 0
// 用于兼容现有代码的渐进式迁移
func ParseBalanceOrZero(s string) *big.Int {
	balance, err := ParseBalance(s)
	if err != nil {
		return big.NewInt(0)
	}
	return balance
}

// ValidateBalance 验证余额字符串是否合法
// 返回 true 表示合法
func ValidateBalance(s string) bool {
	_, err := ParseBalance(s)
	return err == nil
}
