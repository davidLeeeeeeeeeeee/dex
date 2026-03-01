package indexdb

import (
	"encoding/json"
	"sort"
)

// GetAddressTxHistory gets tx history for an address in reverse-time order.
func (idb *IndexDB) GetAddressTxHistory(address string, limit int) ([]*TxRecord, error) {
	prefix := KeyAddressTxPrefix(address)
	// Scan all first, then dedupe, then apply limit.
	kvs, err := idb.ScanPrefix(prefix, 0)
	if err != nil {
		return nil, err
	}

	byTxID := make(map[string]*TxRecord, len(kvs))
	for _, kv := range kvs {
		txID := kv.Value
		record, err := idb.GetTxRecord(txID)
		if err != nil || record == nil {
			continue
		}

		normalized := *record
		if h, txIndex, ok := ParseAddressTxKey(kv.Key, address); ok {
			// Recover true height/index from address index key to avoid overwritten tx_detail height.
			normalized.Height = h
			normalized.TxIndex = txIndex
		}

		// For duplicate tx_id entries, keep the earliest execution occurrence.
		existing, exists := byTxID[txID]
		if !exists ||
			normalized.Height < existing.Height ||
			(normalized.Height == existing.Height && normalized.TxIndex < existing.TxIndex) {
			copy := normalized
			byTxID[txID] = &copy
		}
	}

	results := make([]*TxRecord, 0, len(byTxID))
	for _, rec := range byTxID {
		results = append(results, rec)
	}

	// Keep compatibility with prior ordering: height desc, tx_index asc.
	sort.Slice(results, func(i, j int) bool {
		if results[i].Height != results[j].Height {
			return results[i].Height > results[j].Height
		}
		if results[i].TxIndex != results[j].TxIndex {
			return results[i].TxIndex < results[j].TxIndex
		}
		return results[i].TxID < results[j].TxID
	})

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

// GetTxRecord gets tx detail record by txID.
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

// GetBlockTxs gets all tx records in a block.
func (idb *IndexDB) GetBlockTxs(height uint64) ([]*TxRecord, error) {
	prefix := KeyBlockTxPrefix(height)
	kvs, err := idb.ScanPrefix(prefix, 0)
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

// GetRecentTxs 按 block_tx_ 索引倒序（高度从大到小）返回最近 limit 条 tx。
// txType 为 "" 或 "all" 时返回所有类型，否则按类型过滤。
// 由于 block_tx_ 是正序（高度小在前），需从已同步最高块向前遍历。
func (idb *IndexDB) GetRecentTxs(txType string, limit int) ([]*TxRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	syncH, err := idb.GetSyncHeight()
	if err != nil || syncH == 0 {
		return nil, err
	}

	filterAll := txType == "" || txType == "all"
	var results []*TxRecord

	// 从最高块往前扫，直到集满或到 height=1
	for h := syncH; h >= 1 && len(results) < limit; h-- {
		txs, err := idb.GetBlockTxs(h)
		if err != nil {
			continue
		}
		// 逆序，让同一区块内高 index 排后
		for i := len(txs) - 1; i >= 0 && len(results) < limit; i-- {
			rec := txs[i]
			if filterAll || rec.TxType == txType {
				results = append(results, rec)
			}
		}
	}
	return results, nil
}
