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

	// 4. 检查发送方余额
	if fromAccount.Balances == nil || fromAccount.Balances[transfer.TokenAddress] == nil {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  "insufficient balance",
		}, fmt.Errorf("sender has no balance for token: %s", transfer.TokenAddress)
	}

	fromBalance, ok := new(big.Int).SetString(fromAccount.Balances[transfer.TokenAddress].Balance, 10)
	if !ok {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  fmt.Sprintf("invalid sender balance format for %s: %s", transfer.TokenAddress, fromAccount.Balances[transfer.TokenAddress].Balance),
		}, nil // 注意这里返回 nil 而不是 error，允许区块继续执行
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

	// 读取 FB 余额用于扣费
	var fbBalance *big.Int
	if transfer.TokenAddress == FeeToken {
		fbBalance = fromBalance
	} else {
		fbBal := fromAccount.Balances[FeeToken]
		if fbBal == nil {
			fbBalance = big.NewInt(0)
		} else {
			fbBalance, _ = ParseBalance(fbBal.Balance)
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

	// 6. 读取接收方账户（如果不存在则创建）
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
		// 创建新账户
		toAccount = pb.Account{
			Address:  transfer.To,
			Balances: make(map[string]*pb.TokenBalance),
		}
	}

	// 7. 执行转账与扣费
	// 减少发送方余额（使用安全减法）
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
		fromAccount.Balances[FeeToken].Balance = newFromBalance.String()
	} else {
		// 非 FB 转账：分别扣除 amount 和 fee
		newFromBalance, err := SafeSub(fromBalance, amount)
		if err != nil {
			return nil, &Receipt{TxID: transfer.Base.TxId, Status: "FAILED", Error: "balance underflow"}, err
		}
		fromAccount.Balances[transfer.TokenAddress].Balance = newFromBalance.String()

		// 扣除 FB Fee
		fbBalRecord := fromAccount.Balances[FeeToken]
		if fbBalRecord == nil {
			// 理论上前面检查过，这里做个保险
			fbBalRecord = &pb.TokenBalance{Balance: "0"}
			fromAccount.Balances[FeeToken] = fbBalRecord
		}
		currentFB, err := ParseBalance(fbBalRecord.Balance)
		if err != nil {
			return nil, &Receipt{TxID: transfer.Base.TxId, Status: "FAILED", Error: "invalid FB balance"}, err
		}
		newFB, err := SafeSub(currentFB, feeAmount)
		if err != nil {
			return nil, &Receipt{TxID: transfer.Base.TxId, Status: "FAILED", Error: "FB fee underflow"}, err
		}
		fbBalRecord.Balance = newFB.String()
	}

	// 增加接收方余额
	if toAccount.Balances == nil {
		toAccount.Balances = make(map[string]*pb.TokenBalance)
	}
	if toAccount.Balances[transfer.TokenAddress] == nil {
		toAccount.Balances[transfer.TokenAddress] = &pb.TokenBalance{
			Balance:               "0",
			MinerLockedBalance:    "0",
			LiquidLockedBalance:   "0",
			WitnessLockedBalance:  "0",
			LeverageLockedBalance: "0",
		}
	}

	toBalance, _ := new(big.Int).SetString(toAccount.Balances[transfer.TokenAddress].Balance, 10)
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
	toAccount.Balances[transfer.TokenAddress].Balance = newToBalance.String()

	// 7. 保存更新后的账户
	ws := make([]WriteOp, 0)

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
