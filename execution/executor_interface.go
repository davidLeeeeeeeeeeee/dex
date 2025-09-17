// execution/executor_interface.go
package execution

import (
	"dex/db"
	"fmt"
)

// ExecuteTx 执行单个交易（实现 interfaces.Executor）
func (e *Executor) ExecuteTx(tx interface{}) error {
	anyTx, ok := tx.(*db.AnyTx)
	if !ok {
		return fmt.Errorf("invalid tx type")
	}
	return e.Execute(anyTx)
}

// ExecuteBatch 批量执行交易（实现 interfaces.Executor）
func (e *Executor) ExecuteBatch(txs []interface{}) error {
	for _, tx := range txs {
		if err := e.ExecuteTx(tx); err != nil {
			return fmt.Errorf("batch execution failed: %w", err)
		}
	}
	return nil
}

// ValidateTx 验证交易（实现 interfaces.Executor）
func (e *Executor) ValidateTx(tx interface{}) error {
	anyTx, ok := tx.(*db.AnyTx)
	if !ok {
		return fmt.Errorf("invalid tx type")
	}

	// 基础验证
	base := anyTx.GetBase()
	if base == nil {
		return fmt.Errorf("missing base message")
	}

	if base.TxId == "" {
		return fmt.Errorf("empty tx id")
	}

	if base.FromAddress == "" {
		return fmt.Errorf("empty from address")
	}

	// 可以添加更多验证逻辑
	return nil
}

// GetBalance 获取余额（实现 interfaces.Executor）
func (e *Executor) GetBalance(address, token string) (string, error) {
	// 现在 storage 是 interfaces.DBManager 类型
	dbMgr := e.storage

	// 调用接口方法，返回 interface{}
	accInterface, err := dbMgr.GetAccount(address)
	if err != nil {
		return "0", nil // 如果账户不存在，返回0
	}

	// 类型断言为具体类型
	acc, ok := accInterface.(*db.Account)
	if !ok || acc == nil || acc.Balances == nil {
		return "0", nil
	}

	if bal, exists := acc.Balances[token]; exists && bal != nil {
		return bal.Balance, nil
	}

	return "0", nil
}

// GetAccountState 获取账户状态（实现 interfaces.Executor）
func (e *Executor) GetAccountState(address string) (interface{}, error) {
	return e.storage.GetAccount(address)
}
