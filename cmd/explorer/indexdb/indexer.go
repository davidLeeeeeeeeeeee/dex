package indexdb

import (
	"dex/db"
	"dex/keys"
	"dex/pb"
	"encoding/json"
	"fmt"
	"log"
	"strconv"

	"google.golang.org/protobuf/proto"
)

// TxRecord 交易记录（存储在索引中的简化版本）
type TxRecord struct {
	TxID           string `json:"tx_id"`
	TxType         string `json:"tx_type"`
	FromAddress    string `json:"from_address"`
	ToAddress      string `json:"to_address,omitempty"`
	Value          string `json:"value,omitempty"`
	Status         string `json:"status"`
	Fee            string `json:"fee,omitempty"`
	Nonce          uint64 `json:"nonce,omitempty"`
	Height         uint64 `json:"height"`
	TxIndex        int    `json:"tx_index"`
	FBBalanceAfter string `json:"fb_balance_after,omitempty"`
}

// SetNodeDB 设置节点数据库引用（用于查询余额快照）
func (idb *IndexDB) SetNodeDB(nodeDB *db.Manager) {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	idb.nodeDB = nodeDB
}

// IndexBlock 索引一个区块的所有交易
func (idb *IndexDB) IndexBlock(block *pb.Block) error {
	if block == nil {
		return nil
	}

	height := block.Header.Height
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
	if record == nil || record.TxID == "" {
		return nil
	}

	// 防止累计区块重复索引同一 tx_id 覆盖历史详情
	existing, err := idb.GetTxRecord(record.TxID)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}

	// 尝试从 nodeDB 读取交易级 FB 余额快照
	idb.mu.RLock()
	ndb := idb.nodeDB
	idb.mu.RUnlock()
	if ndb != nil && record.TxID != "" {
		// key 格式: v1_vmtx_fbbal_<txID>
		fbKey := fmt.Sprintf("v1_vmtx_fbbal_%s", record.TxID)
		if data, err := ndb.Get(fbKey); err == nil && data != nil && len(data) > 0 {
			record.FBBalanceAfter = string(data)
		}
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
	txs, err := idb.GetAddressTxHistory(address, 0)
	if err != nil {
		return 0, err
	}
	return len(txs), nil
}

// CollectBlockAddresses 收集区块中涉及的所有地址
func CollectBlockAddresses(block *pb.Block) map[string]struct{} {
	addressSet := make(map[string]struct{})
	if block == nil {
		return addressSet
	}
	for _, tx := range block.Body {
		if tx == nil {
			continue
		}
		if base := tx.GetBase(); base != nil && base.FromAddress != "" {
			addressSet[base.FromAddress] = struct{}{}
		}
		if to, _ := extractToAndValue(tx); to != "" {
			addressSet[to] = struct{}{}
		}
	}
	return addressSet
}

// SnapshotBalancesForHeight 为指定高度的地址集合存储 FB 余额快照
// 应在确认 nodeDB 状态与该高度一致时调用
func (idb *IndexDB) SnapshotBalancesForHeight(addressSet map[string]struct{}, height uint64) {
	idb.mu.RLock()
	nodeDB := idb.nodeDB
	idb.mu.RUnlock()

	if nodeDB == nil || len(addressSet) == 0 {
		return
	}

	for addr := range addressSet {
		balJSON := idb.queryFBBalanceJSON(nodeDB, addr)
		if balJSON == "" {
			continue
		}
		key := KeyBalanceSnapshot(addr, height)
		if err := idb.Set(key, balJSON); err != nil {
			log.Printf("[indexdb] failed to save balance snapshot for %s at height %d: %v", addr, height, err)
		}
	}
}

// BalanceSnapshot FB余额快照（JSON结构）
type BalanceSnapshot struct {
	Balance       string `json:"balance"`
	MinerLocked   string `json:"miner_locked,omitempty"`
	WitnessLocked string `json:"witness_locked,omitempty"`
	OrderFrozen   string `json:"order_frozen,omitempty"`
	LiquidLocked  string `json:"liquid_locked,omitempty"`
}

// queryFBBalanceJSON 从 nodeDB 查询地址的 FB 余额，返回 JSON
func (idb *IndexDB) queryFBBalanceJSON(nodeDB *db.Manager, addr string) string {
	balKey := keys.KeyBalance(addr, "FB")
	data, err := nodeDB.Get(balKey)
	if err != nil || data == nil || len(data) == 0 {
		return ""
	}

	var record pb.TokenBalanceRecord
	if err := proto.Unmarshal(data, &record); err != nil {
		return ""
	}
	if record.Balance == nil {
		return ""
	}

	snap := BalanceSnapshot{
		Balance:       record.Balance.Balance,
		MinerLocked:   record.Balance.MinerLockedBalance,
		WitnessLocked: record.Balance.WitnessLockedBalance,
		OrderFrozen:   record.Balance.OrderFrozenBalance,
		LiquidLocked:  record.Balance.LiquidLockedBalance,
	}
	jsonData, err := json.Marshal(snap)
	if err != nil {
		return ""
	}
	return string(jsonData)
}
