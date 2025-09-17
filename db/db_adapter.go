// db/db_adapter.go
package db

import (
	"dex/interfaces"
)

// 确保 Manager 实现了 interfaces.DBManager
var _ interfaces.DBManager = (*Manager)(nil)

// 实现缺失的接口方法

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

// GetAccount 实现 DBManager 接口
func (m *Manager) GetAccount(address string) (*Account, error) {
	return GetAccount(m, address)
}

// SaveAccount 实现 DBManager 接口
func (m *Manager) SaveAccount(account *Account) error {
	return SaveAccount(m, account)
}

// GetBlock 实现 DBManager 接口
func (m *Manager) GetBlock(height uint64) (*Block, error) {
	return GetBlock(m, height)
}

// SaveBlock 实现 DBManager 接口
func (m *Manager) SaveBlock(block *Block) error {
	return SaveBlock(m, block)
}

// GetLatestBlockHeight 实现 DBManager 接口
func (m *Manager) GetLatestBlockHeight() (uint64, error) {
	return GetLatestBlockHeight(m)
}

// GetAnyTx 实现 DBManager 接口
func (m *Manager) GetAnyTx(txID string) (*AnyTx, error) {
	return GetAnyTxById(m, txID)
}

// SaveAnyTx 实现 DBManager 接口
func (m *Manager) SaveAnyTx(tx *AnyTx) error {
	return SaveAnyTx(m, tx)
}

// SavePendingAnyTx 实现 DBManager 接口
func (m *Manager) SavePendingAnyTx(tx *AnyTx) error {
	return SavePendingAnyTx(m, tx)
}

// DeletePendingAnyTx 实现 DBManager 接口
func (m *Manager) DeletePendingAnyTx(txID string) error {
	return DeletePendingAnyTx(m, txID)
}

// LoadPendingAnyTx 实现 DBManager 接口
func (m *Manager) LoadPendingAnyTx() ([]*AnyTx, error) {
	return LoadPendingAnyTx(m)
}
