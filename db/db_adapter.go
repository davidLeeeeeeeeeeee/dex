// db/db_adapter.go
package db

import "fmt"

// 注意：不再导入 interfaces 包，只实现方法

// ========== DBManager 接口实现 ==========

// Get 实现 DBManager 接口
func (m *Manager) Get(key string) ([]byte, error) {
	val, err := m.Read(key)
	if err != nil {
		return nil, err
	}
	return []byte(val), nil
}

// Set 实现 DBManager 接口
func (m *Manager) Set(key string, value []byte) error {
	m.EnqueueSet(key, string(value))
	return nil
}

// Delete 实现 DBManager 接口
func (m *Manager) Delete(key string) error {
	m.EnqueueDelete(key)
	return nil
}

// GetAccount 实现 DBManager 接口 - 返回 interface{}
func (m *Manager) GetAccount(address string) (interface{}, error) {
	return GetAccount(m, address)
}

// SaveAccount 实现 DBManager 接口 - 接受 interface{}
func (m *Manager) SaveAccount(account interface{}) error {
	acc, ok := account.(*Account)
	if !ok {
		return fmt.Errorf("invalid account type")
	}
	return SaveAccount(m, acc)
}

// GetBlock 实现 DBManager 接口 - 返回 interface{}
func (m *Manager) GetBlock(height uint64) (interface{}, error) {
	return GetBlock(m, height)
}

// SaveBlock 实现 DBManager 接口 - 接受 interface{}
func (m *Manager) SaveBlock(block interface{}) error {
	blk, ok := block.(*Block)
	if !ok {
		return fmt.Errorf("invalid block type")
	}
	return SaveBlock(m, blk)
}

// GetLatestBlockHeight 实现 DBManager 接口
func (m *Manager) GetLatestBlockHeight() (uint64, error) {
	return GetLatestBlockHeight(m)
}

// GetAnyTx 实现 DBManager 接口 - 返回 interface{}
func (m *Manager) GetAnyTx(txID string) (interface{}, error) {
	return GetAnyTxById(m, txID)
}

// SaveAnyTx 实现 DBManager 接口 - 接受 interface{}
func (m *Manager) SaveAnyTx(tx interface{}) error {
	anyTx, ok := tx.(*AnyTx)
	if !ok {
		return fmt.Errorf("invalid tx type")
	}
	return SaveAnyTx(m, anyTx)
}

// SavePendingAnyTx 实现 DBManager 接口 - 接受 interface{}
func (m *Manager) SavePendingAnyTx(tx interface{}) error {
	anyTx, ok := tx.(*AnyTx)
	if !ok {
		return fmt.Errorf("invalid tx type")
	}
	return SavePendingAnyTx(m, anyTx)
}

// DeletePendingAnyTx 实现 DBManager 接口
func (m *Manager) DeletePendingAnyTx(txID string) error {
	return DeletePendingAnyTx(m, txID)
}

// LoadPendingAnyTx 实现 DBManager 接口 - 返回 []interface{}
func (m *Manager) LoadPendingAnyTx() ([]interface{}, error) {
	txs, err := LoadPendingAnyTx(m)
	if err != nil {
		return nil, err
	}

	result := make([]interface{}, len(txs))
	for i, tx := range txs {
		result[i] = tx
	}
	return result, nil
}

// ========== Storage 接口实现（用于 execution 包）==========

// GetToken 实现 Storage 接口
func (m *Manager) GetToken(tokenAddr string) (*Token, error) {
	key := "token_" + tokenAddr
	val, err := m.Read(key)
	if err != nil {
		return nil, err
	}
	token := &Token{}
	if err := ProtoUnmarshal([]byte(val), token); err != nil {
		return nil, err
	}
	return token, nil
}

// SaveToken 实现 Storage 接口
func (m *Manager) SaveToken(token *Token) error {
	key := "token_" + token.Address
	data, err := ProtoMarshal(token)
	if err != nil {
		return err
	}
	m.EnqueueSet(key, string(data))
	return nil
}
