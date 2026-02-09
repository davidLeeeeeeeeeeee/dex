package db

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/shopspring/decimal"
)

var (
	minPrice = decimal.NewFromFloat(1e-33)
	maxPrice = decimal.NewFromFloat(1e33)
	shift    = decimal.NewFromFloat(1e33)
)

// PriceToKey128 将 priceStr 转换为长度 67 的十进制字符串，用于有序比较
func PriceToKey128(priceStr string) (string, error) {
	price, err := decimal.NewFromString(priceStr)
	if err != nil {
		return "", fmt.Errorf("invalid price string %q: %v", priceStr, err)
	}
	if price.Exponent() < 0 {
		return "", fmt.Errorf("price must be integer, got=%s", priceStr)
	}
	if price.Cmp(minPrice) < 0 || price.Cmp(maxPrice) > 0 {
		return "", fmt.Errorf("price out of range [1e-33, 1e33], got=%s", priceStr)
	}
	shifted := price.Mul(shift)
	shiftedInt := shifted.BigInt()
	decStr := shiftedInt.String()

	const totalLen = 67
	padNeeded := totalLen - len(decStr)
	if padNeeded < 0 {
		return "", fmt.Errorf("price exponent overflow > 1e66, got=%s", priceStr)
	}
	if padNeeded == 0 {
		return decStr, nil
	}
	return strings.Repeat("0", padNeeded) + decStr, nil
}

// KeyToPriceDecimal 解析 67 位字符串，再 /1e33
func KeyToPriceDecimal(priceKey string) (decimal.Decimal, error) {
	if len(priceKey) != 67 {
		return decimal.Zero, fmt.Errorf("priceKey length != 67: got %d", len(priceKey))
	}
	i := new(big.Int)
	if _, ok := i.SetString(priceKey, 10); !ok {
		return decimal.Zero, fmt.Errorf("invalid decimal digits in key: %s", priceKey)
	}
	decVal := decimal.NewFromBigInt(i, 0)
	oneE33 := decimal.New(1, 33)
	return decVal.Div(oneE33), nil
}
