// vm/frost_withdraw_signed.go
// Frost 提现签名完成交易处理器
package vm

import (
	"dex/pb"
	"errors"
)

// FrostWithdrawSignedTxHandler Frost 提现签名完成交易处理器
type FrostWithdrawSignedTxHandler struct{}

func (h *FrostWithdrawSignedTxHandler) Kind() string {
	return "frost_withdraw_signed"
}

func (h *FrostWithdrawSignedTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	signedTx, ok := tx.GetContent().(*pb.AnyTx_FrostWithdrawSignedTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a frost withdraw signed transaction"}, errors.New("not a frost withdraw signed transaction")
	}

	signed := signedTx.FrostWithdrawSignedTx
	if signed == nil || signed.Base == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid frost withdraw signed transaction"}, errors.New("invalid frost withdraw signed transaction")
	}

	// TODO: 实现 FrostWithdrawSignedTx 逻辑
	// 1. 确定性重算该 (chain, asset) 队首 job 窗口并校验当前 job
	// 2. 写入 signed_package_bytes 并追加 receipt/history
	// 3. job 置为 SIGNED，withdraw 由 QUEUED → SIGNED
	return nil, &Receipt{TxID: signed.Base.TxId, Status: "FAILED", Error: "not implemented"}, ErrNotImplemented
}

func (h *FrostWithdrawSignedTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}
