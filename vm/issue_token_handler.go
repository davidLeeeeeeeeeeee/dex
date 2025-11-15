package vm

import (
	"dex/keys"
	"dex/pb"
	"encoding/json"
	"fmt"
)

// IssueTokenTxHandler 发币交易处理器
type IssueTokenTxHandler struct{}

func (h *IssueTokenTxHandler) Kind() string {
	return "issue_token"
}

func (h *IssueTokenTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 1. 提取IssueTokenTx
	issueTokenTx, ok := tx.GetContent().(*pb.AnyTx_IssueTokenTx)
	if !ok {
		return nil, &Receipt{
			TxID:   tx.GetTxId(),
			Status: "FAILED",
			Error:  "not an issue token transaction",
		}, fmt.Errorf("not an issue token transaction")
	}

	issueTx := issueTokenTx.IssueTokenTx
	if issueTx == nil || issueTx.Base == nil {
		return nil, &Receipt{
			TxID:   tx.GetTxId(),
			Status: "FAILED",
			Error:  "invalid issue token transaction",
		}, fmt.Errorf("invalid issue token transaction")
	}

	// 2. 验证发行者账户是否存在
	accountKey := keys.KeyAccount(issueTx.Base.FromAddress)
	accountData, exists, err := sv.Get(accountKey)
	if err != nil {
		return nil, &Receipt{
			TxID:   issueTx.Base.TxId,
			Status: "FAILED",
			Error:  "read account failed",
		}, err
	}

	if !exists {
		return nil, &Receipt{
			TxID:   issueTx.Base.TxId,
			Status: "FAILED",
			Error:  "account not found",
		}, fmt.Errorf("account not found: %s", issueTx.Base.FromAddress)
	}

	// 解析账户数据
	var account pb.Account
	if err := json.Unmarshal(accountData, &account); err != nil {
		return nil, &Receipt{
			TxID:   issueTx.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse account data",
		}, err
	}

	// 3. 生成Token地址（使用tx_id作为token地址）
	tokenAddress := issueTx.Base.TxId

	// 检查token是否已存在
	tokenKey := keys.KeyToken(tokenAddress)
	_, tokenExists, _ := sv.Get(tokenKey)
	if tokenExists {
		return nil, &Receipt{
			TxID:   issueTx.Base.TxId,
			Status: "FAILED",
			Error:  "token already exists",
		}, fmt.Errorf("token already exists: %s", tokenAddress)
	}

	// 4. 创建Token记录
	token := &pb.Token{
		Address:     tokenAddress,
		Symbol:      issueTx.TokenSymbol,
		Name:        issueTx.TokenName,
		Owner:       issueTx.Base.FromAddress,
		TotalSupply: issueTx.TotalSupply,
		CanMint:     issueTx.CanMint,
	}

	tokenData, err := json.Marshal(token)
	if err != nil {
		return nil, &Receipt{
			TxID:   issueTx.Base.TxId,
			Status: "FAILED",
			Error:  "failed to marshal token",
		}, err
	}

	ws := make([]WriteOp, 0)

	// 保存Token记录
	ws = append(ws, WriteOp{
		Key:         tokenKey,
		Value:       tokenData,
		Del:         false,
		SyncStateDB: false,
		Category:    "token",
	})

	// 5. 将总供应量分配给发行者
	// 初始化或更新发行者的token余额
	if account.Balances == nil {
		account.Balances = make(map[string]*pb.TokenBalance)
	}

	if account.Balances[tokenAddress] == nil {
		account.Balances[tokenAddress] = &pb.TokenBalance{
			Balance:                  issueTx.TotalSupply,
			CandidateLockedBalance:   "0",
			MinerLockedBalance:       "0",
			LiquidLockedBalance:      "0",
			WitnessLockedBalance:     "0",
			LeverageLockedBalance:    "0",
		}
	} else {
		// 如果已存在余额（理论上不应该发生），累加
		account.Balances[tokenAddress].Balance = issueTx.TotalSupply
	}

	// 保存更新后的账户
	updatedAccountData, err := json.Marshal(&account)
	if err != nil {
		return nil, &Receipt{
			TxID:   issueTx.Base.TxId,
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

	// 6. 更新TokenRegistry
	registryKey := keys.KeyTokenRegistry()
	registryData, _, _ := sv.Get(registryKey)

	var registry pb.TokenRegistry
	if registryData != nil {
		if err := json.Unmarshal(registryData, &registry); err != nil {
			// 如果解析失败，创建新的registry
			registry.Tokens = make(map[string]*pb.Token)
		}
	} else {
		registry.Tokens = make(map[string]*pb.Token)
	}

	registry.Tokens[tokenAddress] = token

	updatedRegistryData, err := json.Marshal(&registry)
	if err != nil {
		return nil, &Receipt{
			TxID:   issueTx.Base.TxId,
			Status: "FAILED",
			Error:  "failed to marshal token registry",
		}, err
	}

	ws = append(ws, WriteOp{
		Key:         registryKey,
		Value:       updatedRegistryData,
		Del:         false,
		SyncStateDB: false,
		Category:    "registry",
	})

	// 7. 返回执行结果
	rc := &Receipt{
		TxID:       issueTx.Base.TxId,
		Status:     "SUCCEED",
		WriteCount: len(ws),
	}

	return ws, rc, nil
}

func (h *IssueTokenTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}

