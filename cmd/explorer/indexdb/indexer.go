package indexdb

import (
	"dex/pb"
	"encoding/json"
	"fmt"
	"strconv"
)

// TxRecord 交易记录（存储在索引中的简化版本）
type TxRecord struct {
	TxID        string `json:"tx_id"`
	TxType      string `json:"tx_type"`
	FromAddress string `json:"from_address"`
	ToAddress   string `json:"to_address,omitempty"`
	Value       string `json:"value,omitempty"`
	Status      string `json:"status"`
	Fee         string `json:"fee,omitempty"`
	Nonce       uint64 `json:"nonce,omitempty"`
	Height      uint64 `json:"height"`
	TxIndex     int    `json:"tx_index"`
}

// IndexBlock 索引一个区块的所有交易
func (idb *IndexDB) IndexBlock(block *pb.Block) error {
	if block == nil {
		return nil
	}

	height := block.Height
	for i, tx := range block.Body {
		if tx == nil {
			continue
		}
		if err := idb.indexTransaction(tx, height, i); err != nil {
			return fmt.Errorf("failed to index tx at height %d index %d: %w", height, i, err)
		}
	}

	// 更新同步高度
	return idb.Set(KeySyncHeight, strconv.FormatUint(height, 10))
}

// indexTransaction 索引单个交易
func (idb *IndexDB) indexTransaction(tx *pb.AnyTx, height uint64, txIndex int) error {
	record := extractTxRecord(tx, height, txIndex)
	if record == nil {
		return nil
	}

	// 序列化记录
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	dataStr := string(data)

	// 1. 保存交易详情
	if err := idb.Set(KeyTxDetail(record.TxID), dataStr); err != nil {
		return err
	}

	// 2. 索引 from 地址
	if record.FromAddress != "" {
		key := KeyAddressTx(record.FromAddress, height, txIndex)
		if err := idb.Set(key, record.TxID); err != nil {
			return err
		}
		// 更新地址交易计数
		idb.incrementAddressCount(record.FromAddress)
	}

	// 3. 索引 to 地址（如果不同于 from）
	if record.ToAddress != "" && record.ToAddress != record.FromAddress {
		key := KeyAddressTx(record.ToAddress, height, txIndex)
		if err := idb.Set(key, record.TxID); err != nil {
			return err
		}
		idb.incrementAddressCount(record.ToAddress)
	}

	// 4. 索引区块交易
	blockKey := KeyBlockTx(height, txIndex)
	if err := idb.Set(blockKey, record.TxID); err != nil {
		return err
	}

	return nil
}

// incrementAddressCount 增加地址交易计数
func (idb *IndexDB) incrementAddressCount(address string) {
	key := KeyAddressCount(address)
	val, _ := idb.Get(key)
	count := 0
	if val != "" {
		count, _ = strconv.Atoi(val)
	}
	count++
	idb.Set(key, strconv.Itoa(count))
}

// GetSyncHeight 获取已同步高度
func (idb *IndexDB) GetSyncHeight() (uint64, error) {
	val, err := idb.Get(KeySyncHeight)
	if err != nil {
		return 0, err
	}
	if val == "" {
		return 0, nil
	}
	return strconv.ParseUint(val, 10, 64)
}

// SetSyncNode 设置同步节点
func (idb *IndexDB) SetSyncNode(node string) error {
	return idb.Set(KeySyncNode, node)
}

// GetSyncNode 获取同步节点
func (idb *IndexDB) GetSyncNode() (string, error) {
	return idb.Get(KeySyncNode)
}

// GetAddressTxCount 获取地址交易数量
func (idb *IndexDB) GetAddressTxCount(address string) (int, error) {
	val, err := idb.Get(KeyAddressCount(address))
	if err != nil {
		return 0, err
	}
	if val == "" {
		return 0, nil
	}
	return strconv.Atoi(val)
}
