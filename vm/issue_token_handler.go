package vm

import (
	"dex/keys"
	"dex/pb"
	"fmt"

	"google.golang.org/protobuf/proto"
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
	if err := proto.Unmarshal(accountData, &account); err != nil {
		return nil, &Receipt{
			TxID:   issueTx.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse account data",
		}, err
	}

	// Validate TotalSupply
	totalSupply, err := ParseBalance(issueTx.TotalSupply)
	if err != nil {
		return nil, &Receipt{
			TxID:   issueTx.Base.TxId,
			Status: "FAILED",
			Error:  fmt.Sprintf("invalid total supply: %v", err),
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
		TotalSupply: totalSupply.String(),
		CanMint:     issueTx.CanMint,
	}

	tokenData, err := proto.Marshal(token)
	if err != nil {
		return nil, &Receipt{
			TxID:   issueTx.Base.TxId,
			Status: "FAILED",
			Error:  "failed to marshal token",
		}, err
	}

	ws := make([]WriteOp, 0)

	// 保存Token记录（独立存储，不再使用 TokenRegistry.Tokens）
	ws = append(ws, WriteOp{
		Key:         tokenKey,
		Value:       tokenData,
		Del:         false,
		SyncStateDB: true, // ✨ 改为 true，支持轻节点同步
		Category:    "token",
	})

	// 5. 将总供应量分配给发行者（使用分离存储）
	issuerBal := GetBalance(sv, issueTx.Base.FromAddress, tokenAddress)
	issuerBal.Balance = totalSupply.String()
	SetBalance(sv, issueTx.Base.FromAddress, tokenAddress, issuerBal)

	// 读取更新后的余额数据用于 WriteOp
	balanceKey := keys.KeyBalance(issueTx.Base.FromAddress, tokenAddress)
	balanceData, _, _ := sv.Get(balanceKey)

	ws = append(ws, WriteOp{
		Key:         balanceKey,
		Value:       balanceData,
		Del:         false,
		SyncStateDB: true,
		Category:    "balance",
	})

	// 6. Token 已经通过 tokenKey (KeyToken) 存储
	// 不再需要 TokenRegistry.Tokens map，每个 token 独立存储

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
