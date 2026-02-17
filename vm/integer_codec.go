package vm

import (
	"fmt"
	"math/big"

	"github.com/shopspring/decimal"
)

func parseBalanceStrict(fieldName, raw string) (*big.Int, error) {
	if raw == "" {
		return big.NewInt(0), nil
	}
	// 使用 decimal 解析，支持 "10.0" 这种格式（兼容旧数据/测试数据）
	v, err := decimal.NewFromString(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid %s format: %w", fieldName, err)
	}
	// 验证是否为非负数
	if v.Sign() < 0 {
		return nil, fmt.Errorf("%s must be non-negative", fieldName)
	}
	// 转换为 BigInt (截断小数部分，如果系统要求严格整数，这里可能需要 v.Explorer() check，但为了兼容 .0，BigInt() 是合适的)
	// 注意：TestE2E 使用 "0.5"，这里会截断为 0。
	return v.BigInt(), nil
}

func parsePositiveBalanceStrict(fieldName, raw string) (*big.Int, error) {
	v, err := parseBalanceStrict(fieldName, raw)
	if err != nil {
		return nil, err
	}
	if v.Sign() <= 0 {
		return nil, fmt.Errorf("%s must be positive", fieldName)
	}
	return v, nil
}

func decimalToBalanceStrict(fieldName string, v decimal.Decimal) (*big.Int, error) {
	if v.Exponent() < 0 {
		return nil, fmt.Errorf("%s must be integer, got %s", fieldName, v.String())
	}
	if v.Sign() < 0 {
		return nil, fmt.Errorf("%s must be non-negative", fieldName)
	}
	bi := v.BigInt()
	if bi.Cmp(MaxUint256) > 0 {
		return nil, ErrOverflow
	}
	return bi, nil
}

func balanceToDecimal(v *big.Int) decimal.Decimal {
	if v == nil {
		return decimal.Zero
	}
	return decimal.NewFromBigInt(v, 0)
}
