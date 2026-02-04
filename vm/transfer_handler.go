package vm

import (
	"dex/keys"
	"dex/pb"
	"fmt"
	"math/big"

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
	amount, ok := new(big.Int).SetString(transfer.Amount, 10)
	if !ok || amount.Sign() <= 0 {
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
	if err := proto.Unmarshal(fromAccountData, &fromAccount); err != nil {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse sender account",
		}, err
	}

	// 4. 使用分离存储检查发送方余额
	fromTokenBal := GetBalance(sv, transfer.Base.FromAddress, transfer.TokenAddress)
	fromBalance, ok := new(big.Int).SetString(fromTokenBal.Balance, 10)
	if !ok || fromBalance == nil {
		fromBalance = big.NewInt(0)
	}

	if fromBalance.Cmp(amount) < 0 {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  fmt.Sprintf("insufficient balance: has %s, need %s", fromBalance.String(), amount.String()),
		}, fmt.Errorf("insufficient balance")
	}

	// 5. 扣除交易手续费（如果是 native token）
	// 假设手续费始终用 FB 支付
	const FeeToken = "FB"
	feeAmount, _ := ParseBalance(transfer.Base.Fee)
	if feeAmount == nil {
		feeAmount = big.NewInt(0)
	}

	// 读取 FB 余额用于扣费（使用分离存储）
	var fbBalance *big.Int
	var fromFBBal *pb.TokenBalance
	if transfer.TokenAddress == FeeToken {
		fbBalance = fromBalance
		fromFBBal = fromTokenBal
	} else {
		fromFBBal = GetBalance(sv, transfer.Base.FromAddress, FeeToken)
		fbBalance, _ = ParseBalance(fromFBBal.Balance)
		if fbBalance == nil {
			fbBalance = big.NewInt(0)
		}
	}

	// 检查是否足够支付手续费
	if transfer.TokenAddress == FeeToken {
		// 如果转账的是 FB，总额 = amount + fee
		totalNeeded, err := SafeAdd(amount, feeAmount)
		if err != nil {
			return nil, &Receipt{TxID: transfer.Base.TxId, Status: "FAILED", Error: "amount+fee overflow"}, err
		}
		if fbBalance.Cmp(totalNeeded) < 0 {
			return nil, &Receipt{
				TxID:   transfer.Base.TxId,
				Status: "FAILED",
				Error:  fmt.Sprintf("insufficient FB balance for amount + fee: has %s, need %s", fbBalance.String(), totalNeeded.String()),
			}, fmt.Errorf("insufficient FB balance")
		}
	} else {
		// 如果转账不是 FB，独立检查 FB 余额支付 fee
		if fbBalance.Cmp(feeAmount) < 0 {
			return nil, &Receipt{
				TxID:   transfer.Base.TxId,
				Status: "FAILED",
				Error:  fmt.Sprintf("insufficient FB balance for fee: has %s, need %s", fbBalance.String(), feeAmount.String()),
			}, fmt.Errorf("insufficient FB balance for fee")
		}
	}

	// 6. 读取接收方账户（用于确保账户存在，后续余额使用分离存储）
	toAccountKey := keys.KeyAccount(transfer.To)
	toAccountData, toExists, _ := sv.Get(toAccountKey)

	var toAccount pb.Account
	if toExists {
		if err := proto.Unmarshal(toAccountData, &toAccount); err != nil {
			return nil, &Receipt{
				TxID:   transfer.Base.TxId,
				Status: "FAILED",
				Error:  "failed to parse receiver account",
			}, err
		}
	} else {
		// 创建新账户（不含余额字段）
		toAccount = pb.Account{
			Address: transfer.To,
		}
	}

	// 7. 执行转账与扣费（使用分离存储）
	// 减少发送方余额
	if transfer.TokenAddress == FeeToken {
		// FB 转账：一次性扣除 total (amount + fee)
		totalDeduct, err := SafeAdd(amount, feeAmount)
		if err != nil {
			return nil, &Receipt{TxID: transfer.Base.TxId, Status: "FAILED", Error: "amount+fee overflow"}, err
		}
		newFromBalance, err := SafeSub(fbBalance, totalDeduct)
		if err != nil {
			return nil, &Receipt{TxID: transfer.Base.TxId, Status: "FAILED", Error: "balance underflow"}, err
		}
		fromFBBal.Balance = newFromBalance.String()
		SetBalance(sv, transfer.Base.FromAddress, FeeToken, fromFBBal)
	} else {
		// 非 FB 转账：分别扣除 amount 和 fee
		newFromBalance, err := SafeSub(fromBalance, amount)
		if err != nil {
			return nil, &Receipt{TxID: transfer.Base.TxId, Status: "FAILED", Error: "balance underflow"}, err
		}
		fromTokenBal.Balance = newFromBalance.String()
		SetBalance(sv, transfer.Base.FromAddress, transfer.TokenAddress, fromTokenBal)

		// 扣除 FB Fee
		currentFB, err := ParseBalance(fromFBBal.Balance)
		if err != nil {
			return nil, &Receipt{TxID: transfer.Base.TxId, Status: "FAILED", Error: "invalid FB balance"}, err
		}
		newFB, err := SafeSub(currentFB, feeAmount)
		if err != nil {
			return nil, &Receipt{TxID: transfer.Base.TxId, Status: "FAILED", Error: "FB fee underflow"}, err
		}
		fromFBBal.Balance = newFB.String()
		SetBalance(sv, transfer.Base.FromAddress, FeeToken, fromFBBal)
	}

	// 增加接收方余额（使用分离存储）
	toTokenBal := GetBalance(sv, transfer.To, transfer.TokenAddress)
	toBalance, _ := new(big.Int).SetString(toTokenBal.Balance, 10)
	if toBalance == nil {
		toBalance = big.NewInt(0)
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
		Key:         fromAccountKey,
		Value:       updatedFromData,
		Del:         false,
		SyncStateDB: true,
		Category:    "account",
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
		Key:         toAccountKey,
		Value:       updatedToData,
		Del:         false,
		SyncStateDB: true,
		Category:    "account",
	})

	// 添加余额 WriteOps（发送方）
	fromTokenBalKey := keys.KeyBalance(transfer.Base.FromAddress, transfer.TokenAddress)
	fromTokenBalData, _, _ := sv.Get(fromTokenBalKey)
	ws = append(ws, WriteOp{Key: fromTokenBalKey, Value: fromTokenBalData, SyncStateDB: true, Category: "balance"})

	if transfer.TokenAddress != FeeToken {
		fromFBBalKey := keys.KeyBalance(transfer.Base.FromAddress, FeeToken)
		fromFBBalData, _, _ := sv.Get(fromFBBalKey)
		ws = append(ws, WriteOp{Key: fromFBBalKey, Value: fromFBBalData, SyncStateDB: true, Category: "balance"})
	}

	// 添加余额 WriteOps（接收方）
	toTokenBalKey := keys.KeyBalance(transfer.To, transfer.TokenAddress)
	toTokenBalData, _, _ := sv.Get(toTokenBalKey)
	ws = append(ws, WriteOp{Key: toTokenBalKey, Value: toTokenBalData, SyncStateDB: true, Category: "balance"})

	// 8. 记录转账历史
	historyKey := keys.KeyTransferHistory(transfer.Base.TxId)
	historyData, _ := proto.Marshal(transfer)
	ws = append(ws, WriteOp{
		Key:         historyKey,
		Value:       historyData,
		Del:         false,
		SyncStateDB: false,
		Category:    "history",
	})

	return ws, &Receipt{
		TxID:       transfer.Base.TxId,
		Status:     "SUCCEED",
		Fee:        transfer.Base.Fee,
		WriteCount: len(ws),
	}, nil
}

func (h *TransferTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}
