package vm

import (
	"dex/keys"
	"dex/pb"
	"fmt"

	"google.golang.org/protobuf/proto"
)

// MinerTxHandler 鐭垮伐浜ゆ槗澶勭悊鍣?
type MinerTxHandler struct{}

func (h *MinerTxHandler) Kind() string {
	return "miner"
}

func (h *MinerTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 1. 鎻愬彇MinerTx
	minerTxWrapper, ok := tx.GetContent().(*pb.AnyTx_MinerTx)
	if !ok {
		return nil, &Receipt{
			TxID:   tx.GetTxId(),
			Status: "FAILED",
			Error:  "not a miner transaction",
		}, fmt.Errorf("not a miner transaction")
	}

	minerTx := minerTxWrapper.MinerTx
	if minerTx == nil || minerTx.Base == nil {
		return nil, &Receipt{
			TxID:   tx.GetTxId(),
			Status: "FAILED",
			Error:  "invalid miner transaction",
		}, fmt.Errorf("invalid miner transaction")
	}

	// 2. 鏍规嵁鎿嶄綔绫诲瀷鍒嗗彂澶勭悊
	switch minerTx.Op {
	case pb.OrderOp_ADD:
		return h.handleStartMining(minerTx, sv)
	case pb.OrderOp_REMOVE:
		return h.handleStopMining(minerTx, sv)
	default:
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "unknown miner operation",
		}, fmt.Errorf("unknown miner operation: %v", minerTx.Op)
	}
}

// handleStartMining 澶勭悊鍚姩鎸栫熆
func (h *MinerTxHandler) handleStartMining(minerTx *pb.MinerTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 楠岃瘉閿佸畾閲戦
	amount, err := parsePositiveBalanceStrict("mining amount", minerTx.Amount)
	if err != nil {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "invalid mining amount",
		}, fmt.Errorf("invalid mining amount: %s", minerTx.Amount)
	}

	// 璇诲彇鐭垮伐璐︽埛
	accountKey := keys.KeyAccount(minerTx.Base.FromAddress)
	accountData, exists, err := sv.Get(accountKey)
	if err != nil || !exists {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "miner account not found",
		}, fmt.Errorf("miner account not found: %s", minerTx.Base.FromAddress)
	}

	var account pb.Account
	if err := unmarshalProtoCompat(accountData, &account); err != nil {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse miner account",
		}, err
	}

	// 鍋囪浣跨敤鍘熺敓浠ｅ竵杩涜鎸栫熆璐ㄦ娂
	nativeTokenAddr := "FB"

	// 浣跨敤鍒嗙瀛樺偍璇诲彇浣欓
	fbBal := GetBalance(sv, minerTx.Base.FromAddress, nativeTokenAddr)

	balance, err := parseBalanceStrict("balance", fbBal.Balance)
	if err != nil {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "invalid balance state",
		}, err
	}
	if balance.Cmp(amount) < 0 {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "insufficient balance for mining",
		}, fmt.Errorf("insufficient balance: has %v, need %s", balance, amount.String())
	}

	ws := make([]WriteOp, 0)

	// 浠庡彲鐢ㄤ綑棰濊浆绉诲埌鎸栫熆閿佸畾浣欓锛堜娇鐢ㄥ畨鍏ㄥ噺娉曪級
	newBalance, err := SafeSub(balance, amount)
	if err != nil {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "balance underflow",
		}, fmt.Errorf("balance underflow: %w", err)
	}
	fbBal.Balance = newBalance.String()

	currentLockedBalance, err := parseBalanceStrict("miner locked balance", fbBal.MinerLockedBalance)
	if err != nil {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "invalid locked balance state",
		}, err
	}
	// 浣跨敤瀹夊叏鍔犳硶妫€鏌ラ攣瀹氫綑棰濇孩鍑?
	newLockedBalance, err := SafeAdd(currentLockedBalance, amount)
	if err != nil {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "locked balance overflow",
		}, fmt.Errorf("locked balance overflow: %w", err)
	}
	fbBal.MinerLockedBalance = newLockedBalance.String()

	// 淇濆瓨鏇存柊鍚庣殑浣欓
	SetBalance(sv, minerTx.Base.FromAddress, nativeTokenAddr, fbBal)
	balanceKey := keys.KeyBalance(minerTx.Base.FromAddress, nativeTokenAddr)
	balanceData, _, _ := sv.Get(balanceKey)

	ws = append(ws, WriteOp{
		Key:         balanceKey,
		Value:       balanceData,
		Del:         false,
		SyncStateDB: true,
		Category:    "balance",
	})

	// 璁剧疆涓虹熆宸ョ姸鎬?
	account.IsMiner = true

	// 淇濆瓨鏇存柊鍚庣殑璐︽埛锛堜笉鍚綑棰濓級
	updatedAccountData, err := proto.Marshal(&account)
	if err != nil {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "failed to marshal account",
		}, err
	}

	ws = append(ws, WriteOp{
		Key:         accountKey,
		Value:       updatedAccountData,
		Del:         false,
		SyncStateDB: true,
		Category:    "account",
	})

	// 璁板綍鎸栫熆鍘嗗彶
	historyKey := keys.KeyMinerHistory(minerTx.Base.TxId)
	historyData, _ := proto.Marshal(minerTx)
	ws = append(ws, WriteOp{
		Key:         historyKey,
		Value:       historyData,
		Del:         false,
		SyncStateDB: false,
		Category:    "history",
	})

	return ws, &Receipt{
		TxID:       minerTx.Base.TxId,
		Status:     "SUCCEED",
		WriteCount: len(ws),
	}, nil
}

// handleStopMining 澶勭悊鍋滄鎸栫熆
func (h *MinerTxHandler) handleStopMining(minerTx *pb.MinerTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 璇诲彇鐭垮伐璐︽埛
	accountKey := keys.KeyAccount(minerTx.Base.FromAddress)
	accountData, exists, err := sv.Get(accountKey)
	if err != nil || !exists {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "miner account not found",
		}, fmt.Errorf("miner account not found")
	}

	var account pb.Account
	if err := unmarshalProtoCompat(accountData, &account); err != nil {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse miner account",
		}, err
	}

	// 妫€鏌ユ槸鍚︽槸鐭垮伐
	if !account.IsMiner {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "account is not a miner",
		}, fmt.Errorf("account is not a miner")
	}

	nativeTokenAddr := "FB"

	// 浣跨敤鍒嗙瀛樺偍璇诲彇浣欓
	fbBal := GetBalance(sv, minerTx.Base.FromAddress, nativeTokenAddr)

	lockedBalance, err := parseBalanceStrict("miner locked balance", fbBal.MinerLockedBalance)
	if err != nil {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "invalid locked balance state",
		}, err
	}
	if lockedBalance.Sign() <= 0 {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "no locked balance to release",
		}, fmt.Errorf("no locked balance")
	}

	ws := make([]WriteOp, 0)

	// 保存更新后的余额
	fbBal.MinerLockedBalance = "0"

	currentBalance, err := parseBalanceStrict("balance", fbBal.Balance)
	if err != nil {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "invalid balance state",
		}, err
	}
	newBalance, err := SafeAdd(currentBalance, lockedBalance)
	if err != nil {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "balance overflow",
		}, fmt.Errorf("balance overflow: %w", err)
	}
	fbBal.Balance = newBalance.String()

	// 淇濆瓨鏇存柊鍚庣殑浣欓
	SetBalance(sv, minerTx.Base.FromAddress, nativeTokenAddr, fbBal)
	balanceKey := keys.KeyBalance(minerTx.Base.FromAddress, nativeTokenAddr)
	balanceData, _, _ := sv.Get(balanceKey)

	ws = append(ws, WriteOp{
		Key:         balanceKey,
		Value:       balanceData,
		Del:         false,
		SyncStateDB: true,
		Category:    "balance",
	})

	// 记录停止挖矿历史
	account.IsMiner = false

	// 淇濆瓨鏇存柊鍚庣殑璐︽埛锛堜笉鍚綑棰濓級
	updatedAccountData, err := proto.Marshal(&account)
	if err != nil {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "failed to marshal account",
		}, err
	}

	ws = append(ws, WriteOp{
		Key:         accountKey,
		Value:       updatedAccountData,
		Del:         false,
		SyncStateDB: true,
		Category:    "account",
	})

	// 璁板綍鍋滄鎸栫熆鍘嗗彶
	historyKey := keys.KeyMinerHistory(minerTx.Base.TxId)
	historyData, _ := proto.Marshal(minerTx)
	ws = append(ws, WriteOp{
		Key:         historyKey,
		Value:       historyData,
		Del:         false,
		SyncStateDB: false,
		Category:    "history",
	})

	return ws, &Receipt{
		TxID:       minerTx.Base.TxId,
		Status:     "SUCCEED",
		WriteCount: len(ws),
	}, nil
}

func (h *MinerTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}
