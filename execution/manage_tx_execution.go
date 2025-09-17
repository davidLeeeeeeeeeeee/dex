// manage_tx_execution.go

package execution

import (
	"dex/db"
	"fmt"

	"github.com/shopspring/decimal"
)

// Storage 定义执行器需要的存储接口
type Storage interface {
	// 基础读写
	Read(key string) (string, error)
	EnqueueSet(key string, value interface{})

	// 账户操作
	GetAccount(address string) (*db.Account, error)
	SaveAccount(account *db.Account) error

	// Token操作
	GetToken(tokenAddr string) (*db.Token, error)
	SaveToken(token *db.Token) error
}

// Executor 执行器结构（可选的，如果需要状态或配置）
type Executor struct {
	storage Storage
	// 可以添加其他依赖，如：
	// eventBus EventBus  // 发送执行事件
	// cache    Cache     // 缓存热点数据
	// metrics  Metrics   // 性能监控
}

// NewExecutor 创建执行器（如果使用结构体方式）
func NewExecutor(storage Storage) *Executor {
	return &Executor{
		storage: storage,
	}
}

// Execute 执行交易（结构体方法）
func (e *Executor) Execute(anyTx *db.AnyTx) error {
	return ExecuteAnyTx(e.storage, anyTx)
}

// ExecuteAnyTx 根据 AnyTx 不同的交易类型，调用不同的执行逻辑
func ExecuteAnyTx(storage Storage, anyTx *db.AnyTx) error {
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

func ExecuteCandidateTx(mgr Storage, tx *db.CandidateTx) error {
	return nil
}

// ExecuteIssueTokenTx 发币逻辑示例
func ExecuteIssueTokenTx(storage Storage, tx *db.IssueTokenTx) error {
	// 1. 检查是否已经有同名 Token
	tokenAddr := "TKN_" + tx.TokenSymbol

	if _, err := storage.GetToken(tokenAddr); err == nil {
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

	if err := storage.SaveToken(newToken); err != nil {
		return fmt.Errorf("save token error: %w", err)
	}

	// 3. 给发行者账户增加初始供应量
	// ✅ 正确：通过接口方法调用，而不是 db.GetAccount
	ownerAcc, err := storage.GetAccount(tx.Base.FromAddress)
	if err != nil {
		return fmt.Errorf("cannot load owner account: %w", err)
	}

	if ownerAcc == nil {
		ownerAcc = &db.Account{
			Address:  tx.Base.FromAddress,
			Balances: make(map[string]*db.TokenBalance),
		}
	}

	if ownerAcc.Balances[tokenAddr] == nil {
		// ✅ 正确：db.NewZeroTokenBalance() 是纯函数，可以直接调用
		ownerAcc.Balances[tokenAddr] = db.NewZeroTokenBalance()
	}

	initSupplyDecimal, _ := decimal.NewFromString(tx.TotalSupply)
	oldBal, _ := decimal.NewFromString(ownerAcc.Balances[tokenAddr].Balance)
	ownerAcc.Balances[tokenAddr].Balance = oldBal.Add(initSupplyDecimal).String()

	// 4. 保存账户
	// ✅ 正确：通过接口方法调用，而不是 db.SaveAccount
	if err := storage.SaveAccount(ownerAcc); err != nil {
		return fmt.Errorf("save account error: %w", err)
	}

	return nil
}

// ExecuteFreezeTx 冻结或解冻指定 targetAddr 在某 tokenAddr 下的余额（或其它限制）
func ExecuteFreezeTx(mgr Storage, tx *db.FreezeTx) error {
	// 1. 先读取 Token 看一下 owner 是否是 fromAddress
	//    你可以自定义“只有 token 的 owner 才能对别人账户做冻结”之类的规则
	val, err := mgr.Read("token_" + tx.TokenAddr)
	if err != nil {
		return fmt.Errorf("ExecuteFreezeTx: cannot read token info: %v", err)
	}
	tk := &db.Token{}
	if err := db.ProtoUnmarshal([]byte(val), tk); err != nil {
		return fmt.Errorf("ExecuteFreezeTx: parse token err: %v", err)
	}
	if tk.Owner != tx.Base.FromAddress {
		return fmt.Errorf("ExecuteFreezeTx: no permission, must be token owner")
	}

	// 2. 根据 freeze=true/false 做相应操作
	//    这里演示：把 targetAddr 的此 token 的 Balance 字段清零
	//    或者把 frozen 状态设个标记
	targetAcc, err := mgr.GetAccount(tx.Base.FromAddress)
	if err != nil {
		return fmt.Errorf("cannot load owner account: %w", err)
	}
	if err != nil {
		return fmt.Errorf("ExecuteFreezeTx: cannot read target account: %v", err)
	}
	if targetAcc == nil {
		return fmt.Errorf("ExecuteFreezeTx: target account not exist: %s", tx.TargetAddr)
	}

	// 用一个示例字段——假设 tokenBalance 里加一个 bool Freeze 标记（需要你先在 proto 或结构加）
	// 这里简单模拟
	if freezeBal, ok := targetAcc.Balances[tx.TokenAddr]; ok {
		if tx.Freeze {
			// 置零
			freezeBal.Balance = "0"
			// 其它锁定余额也可置0
		} else {
			// 如果是解冻，也许要恢复，但得知道原有数值，所以真实逻辑要看你的业务
		}
	} else {
		return fmt.Errorf("ExecuteFreezeTx: target account no token balance to freeze/unfreeze")
	}

	// 3. 保存
	if err := mgr.SaveAccount(targetAcc); err != nil {
		return err
	}
	return nil
}

// ExecuteTransaction 普通转账
func ExecuteTransaction(storage Storage, tx *db.Transaction) error {
	fromAcc, err := storage.GetAccount(tx.Base.FromAddress)
	if err != nil {
		return fmt.Errorf("cannot get from account: %w", err)
	}
	if fromAcc == nil {
		return fmt.Errorf("from account not found: %s", tx.Base.FromAddress)
	}

	toAcc, err := storage.GetAccount(tx.To)
	if err != nil {
		return fmt.Errorf("cannot get to account: %w", err)
	}
	if toAcc == nil {
		// 如果收款账户不存在，自动创建
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

// ExecuteOrderTx ：下单或撤单（示例中：Op=ADD -> 买单，Op=REMOVE -> 卖单）
func ExecuteOrderTx(mgr Storage, tx *db.OrderTx) error {
	// 如果是ADD，直接存入数据库即可，撮合系统会去取
	return nil
}

// RandString 仅示例
func RandString(n int) string {
	letters := []rune("abcdefghijklmnopqrstuvwxyz0123456789")
	out := make([]rune, n)
	for i := range out {
		out[i] = letters[int63()%int64(len(letters))]
	}
	return string(out)
}
func int63() int64 {
	// 这里可以使用真实随机或者 go stdlib rng
	// 这里只是示例
	return 12345678
}
