package vm

import (
	"dex/keys"
	"dex/pb"
	"fmt"

	"google.golang.org/protobuf/proto"
)

// TransferTxHandler 转账交易处理器
type TransferTxHandler struct{}

func (h *TransferTxHandler) Kind() string {
	return "transfer"
}

func (h *TransferTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 1. 提取Transaction
	transferTx, ok := tx.GetContent().(*pb.AnyTx_Transaction)
	if !ok {
		return nil, &Receipt{
			TxID:   tx.GetTxId(),
			Status: "FAILED",
			Error:  "not a transfer transaction",
		}, fmt.Errorf("not a transfer transaction")
	}

	transfer := transferTx.Transaction
	if transfer == nil || transfer.Base == nil {
		return nil, &Receipt{
			TxID:   tx.GetTxId(),
			Status: "FAILED",
			Error:  "invalid transfer transaction",
		}, fmt.Errorf("invalid transfer transaction")
	}

	// 验证转账金额
	amount, err := parsePositiveBalanceStrict("transfer amount", transfer.Amount)
	if err != nil {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  "invalid transfer amount",
		}, fmt.Errorf("invalid transfer amount: %s", transfer.Amount)
	}

	// 2. 检查发送方账户是否被冻结
	freezeKey := keys.KeyFreeze(transfer.Base.FromAddress, transfer.TokenAddress)
	freezeData, isFrozen, _ := sv.Get(freezeKey)
	if isFrozen && string(freezeData) == "true" {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  "sender account is frozen for this token",
		}, fmt.Errorf("sender account is frozen for token: %s", transfer.TokenAddress)
	}

	// 3. 读取发送方账户
	fromAccountKey := keys.KeyAccount(transfer.Base.FromAddress)
	fromAccountData, fromExists, err := sv.Get(fromAccountKey)
	if err != nil || !fromExists {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  "sender account not found",
		}, fmt.Errorf("sender account not found")
	}

	var fromAccount pb.Account
	if err := unmarshalProtoCompat(fromAccountData, &fromAccount); err != nil {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse sender account",
		}, err
	}

	// 4. 使用分离存储检查发送方余额
	fromTokenBal := GetBalance(sv, transfer.Base.FromAddress, transfer.TokenAddress)
	fromBalance, err := parseBalanceStrict("sender balance", fromTokenBal.Balance)
	if err != nil {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  "invalid sender balance",
		}, err
	}

	if fromBalance.Cmp(amount) < 0 {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  fmt.Sprintf("insufficient balance: has %s, need %s", fromBalance.String(), amount.String()),
		}, fmt.Errorf("insufficient balance")
	}

	// 5. 读取接收方账户
	toAccountKey := keys.KeyAccount(transfer.To)
	toAccountData, toExists, _ := sv.Get(toAccountKey)

	var toAccount pb.Account
	if toExists {
		if err := unmarshalProtoCompat(toAccountData, &toAccount); err != nil {
			return nil, &Receipt{
				TxID:   transfer.Base.TxId,
				Status: "FAILED",
				Error:  "failed to parse receiver account",
			}, err
		}
	} else {
		toAccount = pb.Account{Address: transfer.To}
	}

	// 6. 扣除发送方转账金额（fee 由 executor 统一扣除，此处不处理）
	newFromBalance, err := SafeSub(fromBalance, amount)
	if err != nil {
		return nil, &Receipt{TxID: transfer.Base.TxId, Status: "FAILED", Error: "balance underflow"}, err
	}
	fromTokenBal.Balance = newFromBalance.String()
	SetBalance(sv, transfer.Base.FromAddress, transfer.TokenAddress, fromTokenBal)

	// 增加接收方余额
	toTokenBal := GetBalance(sv, transfer.To, transfer.TokenAddress)
	toBalance, err := parseBalanceStrict("receiver balance", toTokenBal.Balance)
	if err != nil {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  "invalid receiver balance",
		}, err
	}
	// 使用安全加法检查接收方余额溢出
	newToBalance, err := SafeAdd(toBalance, amount)
	if err != nil {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  "balance overflow",
		}, fmt.Errorf("receiver balance overflow: %w", err)
	}
	toTokenBal.Balance = newToBalance.String()
	SetBalance(sv, transfer.To, transfer.TokenAddress, toTokenBal)

	// 7. 保存更新后的账户和余额
	ws := make([]WriteOp, 0)

	// 保存发送方账户（不含余额）
	updatedFromData, err := proto.Marshal(&fromAccount)
	if err != nil {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  "failed to marshal sender account",
		}, err
	}

	ws = append(ws, WriteOp{
		Key:      fromAccountKey,
		Value:    updatedFromData,
		Del:      false,
		Category: "account",
	})

	// 保存接收方账户（不含余额）
	updatedToData, err := proto.Marshal(&toAccount)
	if err != nil {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  "failed to marshal receiver account",
		}, err
	}

	ws = append(ws, WriteOp{
		Key:      toAccountKey,
		Value:    updatedToData,
		Del:      false,
		Category: "account",
	})

	// 8. 记录转账历史
	fromTokenBalKey := keys.KeyBalance(transfer.Base.FromAddress, transfer.TokenAddress)
	fromTokenBalData, _, _ := sv.Get(fromTokenBalKey)
	ws = append(ws, WriteOp{Key: fromTokenBalKey, Value: fromTokenBalData, Category: "balance"})

	// 添加接收方余额 WriteOps
	toTokenBalKey := keys.KeyBalance(transfer.To, transfer.TokenAddress)
	toTokenBalData, _, _ := sv.Get(toTokenBalKey)
	ws = append(ws, WriteOp{Key: toTokenBalKey, Value: toTokenBalData, Category: "balance"})

	// 记录转账历史
	historyKey := keys.KeyTransferHistory(transfer.Base.TxId)
	historyData, _ := proto.Marshal(transfer)
	ws = append(ws, WriteOp{Key: historyKey, Value: historyData, Del: false, Category: "history"})

	return ws, &Receipt{
		TxID:       transfer.Base.TxId,
		Status:     "SUCCEED",
		WriteCount: len(ws),
	}, nil
}

func (h *TransferTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}
