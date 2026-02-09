package vm

import (
	"dex/keys"
	"dex/pb"
	"fmt"

	"google.golang.org/protobuf/proto"
)

// IssueTokenTxHandler 鍙戝竵浜ゆ槗澶勭悊鍣?
type IssueTokenTxHandler struct{}

func (h *IssueTokenTxHandler) Kind() string {
	return "issue_token"
}

func (h *IssueTokenTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 1. 鎻愬彇IssueTokenTx
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

	// 2. 楠岃瘉鍙戣鑰呰处鎴锋槸鍚﹀瓨鍦?
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

	// 瑙ｆ瀽璐︽埛鏁版嵁
	var account pb.Account
	if err := unmarshalProtoCompat(accountData, &account); err != nil {
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

	// 3. 鐢熸垚Token鍦板潃锛堜娇鐢╰x_id浣滀负token鍦板潃锛?
	tokenAddress := issueTx.Base.TxId

	// 妫€鏌oken鏄惁宸插瓨鍦?
	tokenKey := keys.KeyToken(tokenAddress)
	_, tokenExists, _ := sv.Get(tokenKey)
	if tokenExists {
		return nil, &Receipt{
			TxID:   issueTx.Base.TxId,
			Status: "FAILED",
			Error:  "token already exists",
		}, fmt.Errorf("token already exists: %s", tokenAddress)
	}

	// 4. 鍒涘缓Token璁板綍
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

	// 淇濆瓨Token璁板綍锛堢嫭绔嬪瓨鍌紝涓嶅啀浣跨敤 TokenRegistry.Tokens锛?
	ws = append(ws, WriteOp{
		Key:         tokenKey,
		Value:       tokenData,
		Del:         false,
		SyncStateDB: true, // 鉁?鏀逛负 true锛屾敮鎸佽交鑺傜偣鍚屾
		Category:    "token",
	})

	// 5. 灏嗘€讳緵搴旈噺鍒嗛厤缁欏彂琛岃€咃紙浣跨敤鍒嗙瀛樺偍锛?
	issuerBal := GetBalance(sv, issueTx.Base.FromAddress, tokenAddress)
	issuerBal.Balance = totalSupply.String()
	SetBalance(sv, issueTx.Base.FromAddress, tokenAddress, issuerBal)

	// 璇诲彇鏇存柊鍚庣殑浣欓鏁版嵁鐢ㄤ簬 WriteOp
	balanceKey := keys.KeyBalance(issueTx.Base.FromAddress, tokenAddress)
	balanceData, _, _ := sv.Get(balanceKey)

	ws = append(ws, WriteOp{
		Key:         balanceKey,
		Value:       balanceData,
		Del:         false,
		SyncStateDB: true,
		Category:    "balance",
	})

	// 6. Token 宸茬粡閫氳繃 tokenKey (KeyToken) 瀛樺偍
	// 涓嶅啀闇€瑕?TokenRegistry.Tokens map锛屾瘡涓?token 鐙珛瀛樺偍

	// 7. 杩斿洖鎵ц缁撴灉
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
