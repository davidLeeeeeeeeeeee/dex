package vm

import (
	"dex/pb"
	"encoding/json"
	"fmt"
	"math/big"
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
	freezeKey := fmt.Sprintf("freeze_%s_%s", transfer.Base.FromAddress, transfer.TokenAddress)
	freezeData, isFrozen, _ := sv.Get(freezeKey)
	if isFrozen && string(freezeData) == "true" {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  "sender account is frozen for this token",
		}, fmt.Errorf("sender account is frozen for token: %s", transfer.TokenAddress)
	}

	// 3. 读取发送方账户
	fromAccountKey := fmt.Sprintf("account_%s", transfer.Base.FromAddress)
	fromAccountData, fromExists, err := sv.Get(fromAccountKey)
	if err != nil || !fromExists {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  "sender account not found",
		}, fmt.Errorf("sender account not found")
	}

	var fromAccount pb.Account
	if err := json.Unmarshal(fromAccountData, &fromAccount); err != nil {
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
			Error:  "invalid sender balance format",
		}, fmt.Errorf("invalid balance format")
	}

	if fromBalance.Cmp(amount) < 0 {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  "insufficient balance",
		}, fmt.Errorf("insufficient balance: has %s, need %s", fromBalance.String(), amount.String())
	}

	// 5. 读取接收方账户（如果不存在则创建）
	toAccountKey := fmt.Sprintf("account_%s", transfer.To)
	toAccountData, toExists, _ := sv.Get(toAccountKey)

	var toAccount pb.Account
	if toExists {
		if err := json.Unmarshal(toAccountData, &toAccount); err != nil {
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

	// 6. 执行转账
	// 减少发送方余额
	newFromBalance := new(big.Int).Sub(fromBalance, amount)
	fromAccount.Balances[transfer.TokenAddress].Balance = newFromBalance.String()

	// 增加接收方余额
	if toAccount.Balances == nil {
		toAccount.Balances = make(map[string]*pb.TokenBalance)
	}
	if toAccount.Balances[transfer.TokenAddress] == nil {
		toAccount.Balances[transfer.TokenAddress] = &pb.TokenBalance{
			Balance:                  "0",
			CandidateLockedBalance:   "0",
			MinerLockedBalance:       "0",
			LiquidLockedBalance:      "0",
			WitnessLockedBalance:     "0",
			LeverageLockedBalance:    "0",
		}
	}

	toBalance, _ := new(big.Int).SetString(toAccount.Balances[transfer.TokenAddress].Balance, 10)
	if toBalance == nil {
		toBalance = big.NewInt(0)
	}
	newToBalance := new(big.Int).Add(toBalance, amount)
	toAccount.Balances[transfer.TokenAddress].Balance = newToBalance.String()

	// 7. 保存更新后的账户
	ws := make([]WriteOp, 0)

	updatedFromData, err := json.Marshal(&fromAccount)
	if err != nil {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  "failed to marshal sender account",
		}, err
	}

	ws = append(ws, WriteOp{
		Key:   fromAccountKey,
		Value: updatedFromData,
		Del:   false,
	})

	updatedToData, err := json.Marshal(&toAccount)
	if err != nil {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  "failed to marshal receiver account",
		}, err
	}

	ws = append(ws, WriteOp{
		Key:   toAccountKey,
		Value: updatedToData,
		Del:   false,
	})

	// 8. 记录转账历史
	historyKey := fmt.Sprintf("transfer_history_%s", transfer.Base.TxId)
	historyData, _ := json.Marshal(transfer)
	ws = append(ws, WriteOp{
		Key:   historyKey,
		Value: historyData,
		Del:   false,
	})

	return ws, &Receipt{
		TxID:       transfer.Base.TxId,
		Status:     "SUCCEED",
		WriteCount: len(ws),
	}, nil
}

func (h *TransferTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}

