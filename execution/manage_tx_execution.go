// execution/manage_tx_execution.go
package execution

import (
	"dex/db"
	"dex/interfaces"
	"fmt"
	"github.com/shopspring/decimal"
)

// Executor 执行器结构
type Executor struct {
	storage interfaces.DBManager // 使用接口类型而不是 Storage
}

// NewExecutor 创建执行器
func NewExecutor(storage interfaces.DBManager) *Executor {
	return &Executor{
		storage: storage,
	}
}

// Execute 执行交易（结构体方法）
func (e *Executor) Execute(anyTx *db.AnyTx) error {
	return ExecuteAnyTx(e.storage, anyTx)
}

// ExecuteAnyTx 根据 AnyTx 不同的交易类型，调用不同的执行逻辑
func ExecuteAnyTx(storage interfaces.DBManager, anyTx *db.AnyTx) error {
	switch tx := anyTx.GetContent().(type) {
	case *db.AnyTx_IssueTokenTx:
		return ExecuteIssueTokenTx(storage, tx.IssueTokenTx)

	case *db.AnyTx_FreezeTx:
		return ExecuteFreezeTx(storage, tx.FreezeTx)

	case *db.AnyTx_Transaction:
		return ExecuteTransaction(storage, tx.Transaction)

	case *db.AnyTx_OrderTx:
		return ExecuteOrderTx(storage, tx.OrderTx)

	case *db.AnyTx_CandidateTx:
		return ExecuteCandidateTx(storage, tx.CandidateTx)

	default:
		return fmt.Errorf("ExecuteAnyTx: unknown tx type")
	}
}

func ExecuteCandidateTx(mgr interfaces.DBManager, tx *db.CandidateTx) error {
	return nil
}

// ExecuteIssueTokenTx 发币逻辑示例
func ExecuteIssueTokenTx(storage interfaces.DBManager, tx *db.IssueTokenTx) error {
	// 1. 检查是否已经有同名 Token
	tokenAddr := "TKN_" + tx.TokenSymbol

	// 类型断言为 *db.Manager 以访问特定方法
	dbMgr, ok := storage.(*db.Manager)
	if !ok {
		return fmt.Errorf("storage is not *db.Manager")
	}

	if _, err := dbMgr.GetToken(tokenAddr); err == nil {
		return fmt.Errorf("token %s already exists", tokenAddr)
	}

	// 2. 构造 Token 对象存入
	newToken := &db.Token{
		Address:     tokenAddr,
		Symbol:      tx.TokenSymbol,
		Name:        tx.TokenName,
		Owner:       tx.Base.FromAddress,
		TotalSupply: tx.TotalSupply,
		CanMint:     tx.CanMint,
	}

	if err := dbMgr.SaveToken(newToken); err != nil {
		return fmt.Errorf("save token error: %w", err)
	}

	// 3. 给发行者账户增加初始供应量
	ownerAccInterface, err := storage.GetAccount(tx.Base.FromAddress)
	if err != nil {
		// 创建新账户
		ownerAccInterface = &db.Account{
			Address:  tx.Base.FromAddress,
			Balances: make(map[string]*db.TokenBalance),
		}
	}

	ownerAcc, ok := ownerAccInterface.(*db.Account)
	if !ok {
		return fmt.Errorf("invalid account type")
	}

	if ownerAcc.Balances == nil {
		ownerAcc.Balances = make(map[string]*db.TokenBalance)
	}

	if ownerAcc.Balances[tokenAddr] == nil {
		ownerAcc.Balances[tokenAddr] = db.NewZeroTokenBalance()
	}

	initSupplyDecimal, _ := decimal.NewFromString(tx.TotalSupply)
	oldBal, _ := decimal.NewFromString(ownerAcc.Balances[tokenAddr].Balance)
	ownerAcc.Balances[tokenAddr].Balance = oldBal.Add(initSupplyDecimal).String()

	// 4. 保存账户
	if err := storage.SaveAccount(ownerAcc); err != nil {
		return fmt.Errorf("save account error: %w", err)
	}

	return nil
}

// ExecuteFreezeTx 冻结或解冻指定 targetAddr 在某 tokenAddr 下的余额
func ExecuteFreezeTx(storage interfaces.DBManager, tx *db.FreezeTx) error {
	// 类型断言为 *db.Manager
	dbMgr, ok := storage.(*db.Manager)
	if !ok {
		return fmt.Errorf("storage is not *db.Manager")
	}

	// 1. 先读取 Token 看一下 owner 是否是 fromAddress
	tk, err := dbMgr.GetToken(tx.TokenAddr)
	if err != nil {
		return fmt.Errorf("ExecuteFreezeTx: cannot read token info: %v", err)
	}
	if tk.Owner != tx.Base.FromAddress {
		return fmt.Errorf("ExecuteFreezeTx: no permission, must be token owner")
	}

	// 2. 获取目标账户
	targetAccInterface, err := storage.GetAccount(tx.TargetAddr)
	if err != nil {
		return fmt.Errorf("ExecuteFreezeTx: cannot read target account: %v", err)
	}

	targetAcc, ok := targetAccInterface.(*db.Account)
	if !ok || targetAcc == nil {
		return fmt.Errorf("ExecuteFreezeTx: target account not exist: %s", tx.TargetAddr)
	}

	// 用一个示例字段——假设 tokenBalance 里加一个 bool Freeze 标记
	if freezeBal, ok := targetAcc.Balances[tx.TokenAddr]; ok {
		if tx.Freeze {
			// 置零
			freezeBal.Balance = "0"
		} else {
			// 解冻逻辑
		}
	} else {
		return fmt.Errorf("ExecuteFreezeTx: target account no token balance to freeze/unfreeze")
	}

	// 3. 保存
	if err := storage.SaveAccount(targetAcc); err != nil {
		return err
	}
	return nil
}

// ExecuteTransaction 普通转账
func ExecuteTransaction(storage interfaces.DBManager, tx *db.Transaction) error {
	fromAccInterface, err := storage.GetAccount(tx.Base.FromAddress)
	if err != nil {
		return fmt.Errorf("cannot get from account: %w", err)
	}

	fromAcc, ok := fromAccInterface.(*db.Account)
	if !ok || fromAcc == nil {
		return fmt.Errorf("from account not found: %s", tx.Base.FromAddress)
	}

	toAccInterface, err := storage.GetAccount(tx.To)
	if err != nil {
		// 如果收款账户不存在，自动创建
		toAccInterface = &db.Account{
			Address:  tx.To,
			Balances: make(map[string]*db.TokenBalance),
		}
	}

	toAcc, ok := toAccInterface.(*db.Account)
	if !ok {
		toAcc = &db.Account{
			Address:  tx.To,
			Balances: make(map[string]*db.TokenBalance),
		}
	}

	if toAcc == nil {
		toAcc = &db.Account{
			Address:  tx.To,
			Balances: make(map[string]*db.TokenBalance),
		}
	}

	amountDec, err := decimal.NewFromString(tx.Amount)
	if err != nil {
		return fmt.Errorf("parse amount error: %w", err)
	}
	if amountDec.IsNegative() || amountDec.IsZero() {
		return fmt.Errorf("invalid amount: %s", tx.Amount)
	}

	// 从发送方扣款
	fromBal, ok := fromAcc.Balances[tx.TokenAddress]
	if !ok || fromBal == nil {
		return fmt.Errorf("sender has no balance for token: %s", tx.TokenAddress)
	}

	fromDec, _ := decimal.NewFromString(fromBal.Balance)
	if fromDec.LessThan(amountDec) {
		return fmt.Errorf("insufficient balance: have=%s need=%s",
			fromBal.Balance, tx.Amount)
	}
	fromBal.Balance = fromDec.Sub(amountDec).String()

	// 给接收方加款
	if toAcc.Balances == nil {
		toAcc.Balances = make(map[string]*db.TokenBalance)
	}
	if toAcc.Balances[tx.TokenAddress] == nil {
		toAcc.Balances[tx.TokenAddress] = &db.TokenBalance{
			Balance: "0",
		}
	}
	toDec, _ := decimal.NewFromString(toAcc.Balances[tx.TokenAddress].Balance)
	toAcc.Balances[tx.TokenAddress].Balance = toDec.Add(amountDec).String()

	// 保存两个账户
	if err := storage.SaveAccount(fromAcc); err != nil {
		return fmt.Errorf("save from account error: %w", err)
	}
	if err := storage.SaveAccount(toAcc); err != nil {
		return fmt.Errorf("save to account error: %w", err)
	}

	return nil
}

// ExecuteOrderTx ：下单或撤单
func ExecuteOrderTx(mgr interfaces.DBManager, tx *db.OrderTx) error {
	// 如果是ADD，直接存入数据库即可，撮合系统会去取
	return nil
}
