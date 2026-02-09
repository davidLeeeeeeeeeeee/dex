package indexdb

import (
	"encoding/json"
)

// GetAddressTxHistory 获取地址的交易历史（按时间倒序）
func (idb *IndexDB) GetAddressTxHistory(address string, limit int) ([]*TxRecord, error) {
	// limit <= 0 means no limit (return all records)

	prefix := KeyAddressTxPrefix(address)
	// 使用正向扫描，因为 key 中的高度已经是倒序的
	kvs, err := idb.ScanPrefix(prefix, limit)
	if err != nil {
		return nil, err
	}

	var results []*TxRecord
	for _, kv := range kvs {
		txID := kv.Value
		record, err := idb.GetTxRecord(txID)
		if err != nil {
			continue
		}
		if record != nil {
			results = append(results, record)
		}
	}
	return results, nil
}

// GetTxRecord 获取交易记录
func (idb *IndexDB) GetTxRecord(txID string) (*TxRecord, error) {
	val, err := idb.Get(KeyTxDetail(txID))
	if err != nil {
		return nil, err
	}
	if val == "" {
		return nil, nil
	}

	var record TxRecord
	if err := json.Unmarshal([]byte(val), &record); err != nil {
		return nil, err
	}
	return &record, nil
}

// GetBlockTxs 获取区块的所有交易
func (idb *IndexDB) GetBlockTxs(height uint64) ([]*TxRecord, error) {
	prefix := KeyBlockTxPrefix(height)
	kvs, err := idb.ScanPrefix(prefix, 0) // 不限制数量
	if err != nil {
		return nil, err
	}

	var results []*TxRecord
	for _, kv := range kvs {
		txID := kv.Value
		record, err := idb.GetTxRecord(txID)
		if err != nil {
			continue
		}
		if record != nil {
			results = append(results, record)
		}
	}
	return results, nil
}
