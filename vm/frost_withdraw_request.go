// vm/frost_withdraw_request.go
// Frost 提现请求交易处理器
package vm

import (
	"dex/pb"
	"errors"
)

// FrostWithdrawRequestTxHandler Frost 提现请求交易处理器
type FrostWithdrawRequestTxHandler struct{}

func (h *FrostWithdrawRequestTxHandler) Kind() string {
	return "frost_withdraw_request"
}

func (h *FrostWithdrawRequestTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	reqTx, ok := tx.GetContent().(*pb.AnyTx_FrostWithdrawRequestTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a frost withdraw request transaction"}, errors.New("not a frost withdraw request transaction")
	}

	req := reqTx.FrostWithdrawRequestTx
	if req == nil || req.Base == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid frost withdraw request transaction"}, errors.New("invalid frost withdraw request transaction")
	}

	// TODO: 实现 FrostWithdrawRequestTx 逻辑
	// 1. 创建 WithdrawRequest{status=QUEUED}
	// 2. 按 (chain, asset) 分配 FIFO index
	// 3. 记录 request_height
	return nil, &Receipt{TxID: req.Base.TxId, Status: "FAILED", Error: "not implemented"}, ErrNotImplemented
}

func (h *FrostWithdrawRequestTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}
