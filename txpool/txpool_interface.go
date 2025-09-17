// txpool/txpool_interface.go
package txpool

import (
	"dex/db"
	"fmt"
)

// AddTx 实现 interfaces.TxPool 接口
func (p *TxPool) AddTx(tx interface{}) error {
	anyTx, ok := tx.(*db.AnyTx)
	if !ok {
		return fmt.Errorf("invalid tx type")
	}
	return p.StoreAnyTx(anyTx)
}

// RemoveTx 实现 interfaces.TxPool 接口
func (p *TxPool) RemoveTx(txID string) error {
	p.RemoveAnyTx(txID)
	return nil
}

// HasTx 实现 interfaces.TxPool 接口
func (p *TxPool) HasTx(txID string) bool {
	return p.HasTransaction(txID)
}

// GetTx 实现 interfaces.TxPool 接口
func (p *TxPool) GetTx(txID string) interface{} {
	return p.GetTransactionById(txID)
}

// GetPendingTxs 实现 interfaces.TxPool 接口
func (p *TxPool) GetPendingTxs(limit int) []interface{} {
	txs := p.GetPendingAnyTx()

	// 应用限制
	if limit > 0 && len(txs) > limit {
		txs = txs[:limit]
	}

	result := make([]interface{}, len(txs))
	for i, tx := range txs {
		result[i] = tx
	}
	return result
}

// GetTxsByShortHashes 重载版本，返回 []interface{}
func (p *TxPool) GetTxsByShortHashesInterface(hashes [][]byte, isPending bool) []interface{} {
	txs := p.GetTxsByShortHashes(hashes, isPending)
	result := make([]interface{}, len(txs))
	for i, tx := range txs {
		result[i] = tx
	}
	return result
}

// GetPendingCount 实现 interfaces.TxPool 接口
func (p *TxPool) GetPendingCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pendingAnyTxCache.Len()
}

// Clear 实现 interfaces.TxPool 接口
func (p *TxPool) Clear() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 清空所有缓存
	p.pendingAnyTxCache.Purge()
	p.shortPendingAnyTxCache.Purge()
	p.cacheTx.Purge()
	p.shortTxCache.Purge()

	return nil
}
