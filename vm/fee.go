package vm

import (
	"dex/keys"
	"fmt"
	"math/big"
)

// ========== 全局手续费常量 ==========

const FeeToken = "FB"

// MinTxFee 全网统一最低手续费（1000 FB）
var MinTxFee = big.NewInt(1000)

// DeductFee 统一扣除交易手续费。
// 返回扣费成功后需要追加的 WriteOp（FB 余额写回）以及实际扣费金额字符串。
// 若 feeStr 为空或 < MinTxFee 或余额不足，返回 error。
func DeductFee(sv StateView, fromAddr, feeStr string) (WriteOp, string, error) {
	fee, err := parseBalanceStrict("tx fee", feeStr)
	if err != nil || fee.Cmp(MinTxFee) < 0 {
		return WriteOp{}, "", fmt.Errorf("fee must be >= %s, got %s", MinTxFee.String(), feeStr)
	}

	fbBal := GetBalance(sv, fromAddr, FeeToken)
	balance, err := parseBalanceStrict("sender FB balance", fbBal.Balance)
	if err != nil {
		return WriteOp{}, "", fmt.Errorf("invalid sender FB balance: %w", err)
	}
	if balance.Cmp(fee) < 0 {
		return WriteOp{}, "", fmt.Errorf("insufficient FB for fee: has %s, need %s", balance.String(), fee.String())
	}

	newBal, err := SafeSub(balance, fee)
	if err != nil {
		return WriteOp{}, "", fmt.Errorf("fee deduction underflow: %w", err)
	}
	fbBal.Balance = newBal.String()
	SetBalance(sv, fromAddr, FeeToken, fbBal)

	// 读回序列化数据用于 WriteOp
	balKey := keys.KeyBalance(fromAddr, FeeToken)
	balData, _, _ := sv.Get(balKey)
	return WriteOp{Key: balKey, Value: balData, Category: "balance"}, fee.String(), nil
}
