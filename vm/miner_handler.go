package vm

import (
	"dex/keys"
	"dex/pb"
	"fmt"
	"math/big"

	"google.golang.org/protobuf/proto"
)

// MinerTxHandler 矿工交易处理器
type MinerTxHandler struct{}

func (h *MinerTxHandler) Kind() string {
	return "miner"
}

func (h *MinerTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 1. 提取MinerTx
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

	// 2. 根据操作类型分发处理
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

// handleStartMining 处理启动挖矿
func (h *MinerTxHandler) handleStartMining(minerTx *pb.MinerTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 验证锁定金额
	amount, ok := new(big.Int).SetString(minerTx.Amount, 10)
	if !ok || amount.Sign() <= 0 {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "invalid mining amount",
		}, fmt.Errorf("invalid mining amount: %s", minerTx.Amount)
	}

	// 读取矿工账户
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
	if err := proto.Unmarshal(accountData, &account); err != nil {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse miner account",
		}, err
	}

	// 假设使用原生代币进行挖矿质押
	nativeTokenAddr := "native_token"

	// 检查余额
	if account.Balances == nil || account.Balances[nativeTokenAddr] == nil {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "insufficient balance for mining",
		}, fmt.Errorf("no balance for native token")
	}

	balance, _ := new(big.Int).SetString(account.Balances[nativeTokenAddr].Balance, 10)
	if balance == nil || balance.Cmp(amount) < 0 {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "insufficient balance for mining",
		}, fmt.Errorf("insufficient balance: has %s, need %s", balance.String(), amount.String())
	}

	ws := make([]WriteOp, 0)

	// 从可用余额转移到挖矿锁定余额（使用安全减法）
	newBalance, err := SafeSub(balance, amount)
	if err != nil {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "balance underflow",
		}, fmt.Errorf("balance underflow: %w", err)
	}
	account.Balances[nativeTokenAddr].Balance = newBalance.String()

	currentLockedBalance, _ := new(big.Int).SetString(account.Balances[nativeTokenAddr].MinerLockedBalance, 10)
	if currentLockedBalance == nil {
		currentLockedBalance = big.NewInt(0)
	}
	// 使用安全加法检查锁定余额溢出
	newLockedBalance, err := SafeAdd(currentLockedBalance, amount)
	if err != nil {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "locked balance overflow",
		}, fmt.Errorf("locked balance overflow: %w", err)
	}
	account.Balances[nativeTokenAddr].MinerLockedBalance = newLockedBalance.String()

	// 设置为矿工状态
	account.IsMiner = true

	// 保存更新后的账户
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

	// 记录挖矿历史
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

// handleStopMining 处理停止挖矿
func (h *MinerTxHandler) handleStopMining(minerTx *pb.MinerTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 读取矿工账户
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
	if err := proto.Unmarshal(accountData, &account); err != nil {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse miner account",
		}, err
	}

	// 检查是否是矿工
	if !account.IsMiner {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "account is not a miner",
		}, fmt.Errorf("account is not a miner")
	}

	nativeTokenAddr := "native_token"

	// 检查是否有锁定余额
	if account.Balances == nil || account.Balances[nativeTokenAddr] == nil {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "no locked balance found",
		}, fmt.Errorf("no locked balance")
	}

	lockedBalance, _ := new(big.Int).SetString(account.Balances[nativeTokenAddr].MinerLockedBalance, 10)
	if lockedBalance == nil || lockedBalance.Sign() <= 0 {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "no locked balance to release",
		}, fmt.Errorf("no locked balance")
	}

	ws := make([]WriteOp, 0)

	// 将锁定余额转回可用余额（使用安全加法）
	account.Balances[nativeTokenAddr].MinerLockedBalance = "0"

	currentBalance, _ := new(big.Int).SetString(account.Balances[nativeTokenAddr].Balance, 10)
	if currentBalance == nil {
		currentBalance = big.NewInt(0)
	}
	newBalance, err := SafeAdd(currentBalance, lockedBalance)
	if err != nil {
		return nil, &Receipt{
			TxID:   minerTx.Base.TxId,
			Status: "FAILED",
			Error:  "balance overflow",
		}, fmt.Errorf("balance overflow: %w", err)
	}
	account.Balances[nativeTokenAddr].Balance = newBalance.String()

	// 取消矿工状态
	account.IsMiner = false

	// 保存更新后的账户
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

	// 记录停止挖矿历史
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
