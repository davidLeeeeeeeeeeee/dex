package vm

import (
	"dex/keys"
	"dex/pb"
	"encoding/json"
	"fmt"
	"math/big"

	"google.golang.org/protobuf/proto"
)

// RechargeTxHandler 上账交易处理器
type RechargeTxHandler struct{}

func (h *RechargeTxHandler) Kind() string {
	return "recharge"
}

func (h *RechargeTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 1. 提取RechargeTx
	rechargeTx, ok := tx.GetContent().(*pb.AnyTx_AddressTx)
	if !ok {
		return nil, &Receipt{
			TxID:   tx.GetTxId(),
			Status: "FAILED",
			Error:  "not a recharge transaction",
		}, fmt.Errorf("not a recharge transaction")
	}

	recharge := rechargeTx.AddressTx
	if recharge == nil || recharge.Base == nil {
		return nil, &Receipt{
			TxID:   tx.GetTxId(),
			Status: "FAILED",
			Error:  "invalid recharge transaction",
		}, fmt.Errorf("invalid recharge transaction")
	}

	// 2. 验证必要字段
	if recharge.TokenAddress == "" {
		return nil, &Receipt{
			TxID:   recharge.Base.TxId,
			Status: "FAILED",
			Error:  "token_address is required",
		}, fmt.Errorf("token_address is required")
	}

	if recharge.Tweak == "" {
		return nil, &Receipt{
			TxID:   recharge.Base.TxId,
			Status: "FAILED",
			Error:  "tweak is required",
		}, fmt.Errorf("tweak is required")
	}

	// 3. 验证Token是否存在
	tokenKey := keys.KeyToken(recharge.TokenAddress)
	_, tokenExists, err := sv.Get(tokenKey)
	if err != nil {
		return nil, &Receipt{
			TxID:   recharge.Base.TxId,
			Status: "FAILED",
			Error:  "read token failed",
		}, err
	}

	if !tokenExists {
		return nil, &Receipt{
			TxID:   recharge.Base.TxId,
			Status: "FAILED",
			Error:  "token not found",
		}, fmt.Errorf("token not found: %s", recharge.TokenAddress)
	}

	// 4. 检查该充值地址是否已经被使用过
	// 使用 tweak 作为唯一标识，防止重复上账
	rechargeRecordKey := keys.KeyRechargeRecord(recharge.Base.FromAddress, recharge.Tweak)
	_, recordExists, _ := sv.Get(rechargeRecordKey)
	if recordExists {
		return nil, &Receipt{
			TxID:   recharge.Base.TxId,
			Status: "FAILED",
			Error:  "recharge address already used",
		}, fmt.Errorf("recharge address already used for tweak: %s", recharge.Tweak)
	}

	// 5. 读取或创建用户账户
	accountKey := keys.KeyAccount(recharge.Base.FromAddress)
	accountData, accountExists, _ := sv.Get(accountKey)

	var account pb.Account
	if accountExists {
		if err := proto.Unmarshal(accountData, &account); err != nil {
			return nil, &Receipt{
				TxID:   recharge.Base.TxId,
				Status: "FAILED",
				Error:  "failed to parse account",
			}, err
		}
	} else {
		// 创建新账户
		account = pb.Account{
			Address:  recharge.Base.FromAddress,
			Balances: make(map[string]*pb.TokenBalance),
		}
	}

	// 6. 生成充值地址（在实际应用中，这应该在打包区块时由矿工生成）
	// 这里简化处理，使用 from_address + tweak 的组合
	generatedAddress := fmt.Sprintf("%s_%s", recharge.Base.FromAddress, recharge.Tweak)
	
	// TODO: 实际应该从链外数据源（如BTC区块链）查询该地址的充值金额
	// 这里假设充值金额已经通过某种方式验证，暂时硬编码为示例值
	// 在真实场景中，需要：
	// 1. 验证该地址在对应区块链上确实收到了资金
	// 2. 验证充值金额
	// 3. 防止双花攻击
	
	// 示例：假设充值了 100 个token
	rechargeAmount := big.NewInt(100)

	// 7. 更新账户余额
	if account.Balances == nil {
		account.Balances = make(map[string]*pb.TokenBalance)
	}

	if account.Balances[recharge.TokenAddress] == nil {
		account.Balances[recharge.TokenAddress] = &pb.TokenBalance{
			Balance:                  "0",
			CandidateLockedBalance:   "0",
			MinerLockedBalance:       "0",
			LiquidLockedBalance:      "0",
			WitnessLockedBalance:     "0",
			LeverageLockedBalance:    "0",
		}
	}

	currentBalance, _ := new(big.Int).SetString(account.Balances[recharge.TokenAddress].Balance, 10)
	if currentBalance == nil {
		currentBalance = big.NewInt(0)
	}
	newBalance := new(big.Int).Add(currentBalance, rechargeAmount)
	account.Balances[recharge.TokenAddress].Balance = newBalance.String()

	ws := make([]WriteOp, 0)

	// 8. 保存更新后的账户
	updatedAccountData, err := proto.Marshal(&account)
	if err != nil {
		return nil, &Receipt{
			TxID:   recharge.Base.TxId,
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

	// 9. 保存充值记录，防止重复上账
	rechargeRecord := map[string]string{
		"tx_id":             recharge.Base.TxId,
		"from_address":      recharge.Base.FromAddress,
		"token_address":     recharge.TokenAddress,
		"generated_address": generatedAddress,
		"tweak":             recharge.Tweak,
		"amount":            rechargeAmount.String(),
	}
	rechargeRecordData, _ := json.Marshal(rechargeRecord)

	ws = append(ws, WriteOp{
		Key:         rechargeRecordKey,
		Value:       rechargeRecordData,
		Del:         false,
		SyncStateDB: true, // ✨ 改为 true，支持轻节点同步
		Category:    "record",
	})

	// 10. 保存充值交易历史
	historyKey := keys.KeyRechargeHistory(recharge.Base.TxId)
	historyData, _ := proto.Marshal(recharge)
	ws = append(ws, WriteOp{
		Key:         historyKey,
		Value:       historyData,
		Del:         false,
		SyncStateDB: false,
		Category:    "history",
	})

	// 11. 保存生成的地址映射（用于查询）
	addressMappingKey := keys.KeyRechargeAddress(generatedAddress)
	addressMapping := map[string]string{
		"user_address":  recharge.Base.FromAddress,
		"token_address": recharge.TokenAddress,
		"tx_id":         recharge.Base.TxId,
	}
	addressMappingData, _ := json.Marshal(addressMapping)
	ws = append(ws, WriteOp{
		Key:         addressMappingKey,
		Value:       addressMappingData,
		Del:         false,
		SyncStateDB: false,
		Category:    "index",
	})

	return ws, &Receipt{
		TxID:       recharge.Base.TxId,
		Status:     "SUCCEED",
		WriteCount: len(ws),
	}, nil
}

func (h *RechargeTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}

