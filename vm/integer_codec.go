package vm

import (
	"fmt"
	"math/big"

	"github.com/shopspring/decimal"
)

func parseBalanceStrict(fieldName, raw string) (*big.Int, error) {
	v, err := ParseBalance(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", fieldName, err)
	}
	return v, nil
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
