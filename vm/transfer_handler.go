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

	// 5. 鎵ｉ櫎浜ゆ槗鎵嬬画璐癸紙濡傛灉鏄?native token锛?
	// 鍋囪鎵嬬画璐瑰缁堢敤 FB 鏀粯
	const FeeToken = "FB"
	feeAmount, err := parseBalanceStrict("tx fee", transfer.Base.Fee)
	if err != nil {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  "invalid fee",
		}, err
	}

	// 璇诲彇 FB 浣欓鐢ㄤ簬鎵ｈ垂锛堜娇鐢ㄥ垎绂诲瓨鍌級
	var fbBalance *big.Int
	var fromFBBal *pb.TokenBalance
	if transfer.TokenAddress == FeeToken {
		fbBalance = fromBalance
		fromFBBal = fromTokenBal
	} else {
		fromFBBal = GetBalance(sv, transfer.Base.FromAddress, FeeToken)
		fbBalance, err = parseBalanceStrict("sender FB balance", fromFBBal.Balance)
		if err != nil {
			return nil, &Receipt{
				TxID:   transfer.Base.TxId,
				Status: "FAILED",
				Error:  "invalid sender FB balance",
			}, err
		}
	}

	// 妫€鏌ユ槸鍚﹁冻澶熸敮浠樻墜缁垂
	if transfer.TokenAddress == FeeToken {
		// 濡傛灉杞处鐨勬槸 FB锛屾€婚 = amount + fee
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
		// 濡傛灉杞处涓嶆槸 FB锛岀嫭绔嬫鏌?FB 浣欓鏀粯 fee
		if fbBalance.Cmp(feeAmount) < 0 {
			return nil, &Receipt{
				TxID:   transfer.Base.TxId,
				Status: "FAILED",
				Error:  fmt.Sprintf("insufficient FB balance for fee: has %s, need %s", fbBalance.String(), feeAmount.String()),
			}, fmt.Errorf("insufficient FB balance for fee")
		}
	}

	// 6. 璇诲彇鎺ユ敹鏂硅处鎴凤紙鐢ㄤ簬纭繚璐︽埛瀛樺湪锛屽悗缁綑棰濅娇鐢ㄥ垎绂诲瓨鍌級
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
		// 鍒涘缓鏂拌处鎴凤紙涓嶅惈浣欓瀛楁锛?
		toAccount = pb.Account{
			Address: transfer.To,
		}
	}

	// 7. 鎵ц杞处涓庢墸璐癸紙浣跨敤鍒嗙瀛樺偍锛?
	// 鍑忓皯鍙戦€佹柟浣欓
	if transfer.TokenAddress == FeeToken {
		// 非 FB 转账：分别扣除 amount 和 fee
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
		// 闈?FB 杞处锛氬垎鍒墸闄?amount 鍜?fee
		newFromBalance, err := SafeSub(fromBalance, amount)
		if err != nil {
			return nil, &Receipt{TxID: transfer.Base.TxId, Status: "FAILED", Error: "balance underflow"}, err
		}
		fromTokenBal.Balance = newFromBalance.String()
		SetBalance(sv, transfer.Base.FromAddress, transfer.TokenAddress, fromTokenBal)

		// 鎵ｉ櫎 FB Fee
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

	// 澧炲姞鎺ユ敹鏂逛綑棰濓紙浣跨敤鍒嗙瀛樺偍锛?
	toTokenBal := GetBalance(sv, transfer.To, transfer.TokenAddress)
	toBalance, err := parseBalanceStrict("receiver balance", toTokenBal.Balance)
	if err != nil {
		return nil, &Receipt{
			TxID:   transfer.Base.TxId,
			Status: "FAILED",
			Error:  "invalid receiver balance",
		}, err
	}
	// 浣跨敤瀹夊叏鍔犳硶妫€鏌ユ帴鏀舵柟浣欓婧㈠嚭
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

	// 7. 淇濆瓨鏇存柊鍚庣殑璐︽埛鍜屼綑棰?
	ws := make([]WriteOp, 0)

	// 淇濆瓨鍙戦€佹柟璐︽埛锛堜笉鍚綑棰濓級
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
		Category:    "account",
	})

	// 淇濆瓨鎺ユ敹鏂硅处鎴凤紙涓嶅惈浣欓锛?
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
		Category:    "account",
	})

	// 8. 记录转账历史
	fromTokenBalKey := keys.KeyBalance(transfer.Base.FromAddress, transfer.TokenAddress)
	fromTokenBalData, _, _ := sv.Get(fromTokenBalKey)
	ws = append(ws, WriteOp{Key: fromTokenBalKey, Value: fromTokenBalData, Category: "balance"})

	if transfer.TokenAddress != FeeToken {
		fromFBBalKey := keys.KeyBalance(transfer.Base.FromAddress, FeeToken)
		fromFBBalData, _, _ := sv.Get(fromFBBalKey)
		ws = append(ws, WriteOp{Key: fromFBBalKey, Value: fromFBBalData, Category: "balance"})
	}

	// 娣诲姞浣欓 WriteOps锛堟帴鏀舵柟锛?
	toTokenBalKey := keys.KeyBalance(transfer.To, transfer.TokenAddress)
	toTokenBalData, _, _ := sv.Get(toTokenBalKey)
	ws = append(ws, WriteOp{Key: toTokenBalKey, Value: toTokenBalData, Category: "balance"})

	// 8. 璁板綍杞处鍘嗗彶
	historyKey := keys.KeyTransferHistory(transfer.Base.TxId)
	historyData, _ := proto.Marshal(transfer)
	ws = append(ws, WriteOp{
		Key:         historyKey,
		Value:       historyData,
		Del:         false,
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
