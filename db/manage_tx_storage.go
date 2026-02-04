package db

import (
	"dex/pb"
	"dex/utils"
	"fmt"
	"strings"
)

// ------------- 基础交易 -------------

// SaveTransaction saves a Transaction to the database
//
// ⚠️ INTERNAL API - DO NOT CALL DIRECTLY FROM OUTSIDE DB PACKAGE
// This method is used internally by db package for legacy compatibility.
// New code should use VM's unified write path (applyResult) instead.
//
// Deprecated: Use VM's WriteOp mechanism for all state changes.
func (mgr *Manager) SaveTransaction(tx *pb.Transaction) error {
	//logs.Trace("SaveTransaction %s\n", tx)
	key := KeyTx(tx.Base.TxId)
	data, err := ProtoMarshal(tx)
	if err != nil {
		return err
	}
	mgr.EnqueueSet(key, string(data))
	// 如果是PENDING
	if tx.Base.Status == pb.Status_PENDING {
		pendingKey := KeyPendingAnyTx(tx.Base.TxId)
		mgr.EnqueueSet(pendingKey, string(data))
	}
	// 2. 同时把它封装进 AnyTx 并存 "anyTx_<txid>"
	mgr.EnqueueSet(KeyAnyTx(tx.Base.TxId), key)
	return nil
}

func (mgr *Manager) GetTransaction(txID string) (*pb.Transaction, error) {
	key := KeyTx(txID)
	val, err := mgr.Read(key)
	if err != nil {
		return nil, err
	}
	tx := &pb.Transaction{}
	if err := ProtoUnmarshal([]byte(val), tx); err != nil {
		return nil, err
	}
	return tx, nil
}

// ------------- OrderTx -------------

// SaveOrderTx saves an OrderTx to the database
//
// ⚠️ INTERNAL API - DO NOT CALL DIRECTLY FROM OUTSIDE DB PACKAGE
// This method is used internally by db package for legacy compatibility.
// New code should use VM's unified write path (applyResult) instead.
//
// Deprecated: Use VM's WriteOp mechanism for all state changes.
func (mgr *Manager) SaveOrderTx(order *pb.OrderTx) error {
	// 1. 先拿到 pairKey
	pairKey := utils.GeneratePairKey(order.BaseToken, order.QuoteToken)

	// 2. 把 price 转成 stringKey
	priceKey, err := PriceToKey128(order.Price)
	if err != nil {
		return err
	}

	// 3. 构造索引key
	//    例如: "pair:BTC_USDT|price:000000000123123|order_id:..."
	// 新版本 OrderTx 不再有 IsFilled 字段，新订单默认未成交
	indexKey := KeyOrderPriceIndex(pairKey, order.Side, false, priceKey, order.Base.TxId)

	// 4. 存储 (跟你现在的逻辑一样，只是把 "base_token_base_quote" 替换成 pairKey)
	data, err := ProtoMarshal(order)
	if err != nil {
		return err
	}
	mgr.EnqueueSet(indexKey, string(data))

	// 存储原始订单
	orderKey := KeyOrderTx(order.Base.TxId)
	mgr.EnqueueSet(orderKey, string(data))
	// 5. 同时把它封装进 AnyTx 并存 "anyTx_<txid>"
	mgr.EnqueueSet(KeyAnyTx(order.Base.TxId), orderKey)
	return nil
}
func GetOrderTx(mgr *Manager, txID string) (*pb.OrderTx, error) {
	key := "order_" + txID
	val, err := mgr.Read(key)
	if err != nil {
		return nil, err
	}
	order := &pb.OrderTx{}
	if err := ProtoUnmarshal([]byte(val), order); err != nil {
		return nil, err
	}
	return order, nil
}

func (mgr *Manager) GetOrderTx(txID string) (*pb.OrderTx, error) {
	key := KeyOrderTx(txID)
	val, err := mgr.Read(key)
	if err != nil {
		return nil, err
	}
	order := &pb.OrderTx{}
	if err := ProtoUnmarshal([]byte(val), order); err != nil {
		return nil, err
	}
	return order, nil
}

// SaveMinerTx saves a MinerTx to the database
//
// ⚠️ INTERNAL API - DO NOT CALL DIRECTLY FROM OUTSIDE DB PACKAGE
// This method is used internally by db package for legacy compatibility.
// New code should use VM's unified write path (applyResult) instead.
//
// ⚠️ 重要变更：余额已迁移到分离存储 (v1_balance_)，此函数不再处理余额逻辑。
// 账户余额和质押操作现在由 VM 层的 miner_handler.go 统一处理。
//
// Deprecated: Use VM's WriteOp mechanism for all state changes.
func (mgr *Manager) SaveMinerTx(tx *pb.MinerTx) error {
	// 只做基本的交易存储，不再处理余额逻辑
	// 真正的业务逻辑（质押、解质押、余额变更）由 VM 层处理
	mainKey := KeyMinerTx(tx.Base.TxId)
	data, err := ProtoMarshal(tx)
	if err != nil {
		return err
	}
	mgr.EnqueueSet(mainKey, string(data))
	mgr.EnqueueSet(KeyAnyTx(tx.Base.TxId), mainKey)
	return nil
}

// GetAnyTxById 根据给定的 tx_id 从数据库中读取对应的交易（AnyTx）
// 优先从新的 txraw_ 前缀读取（交易原文，不可变）
// 如果不存在，则回退到旧的 anyTx_ 间接引用方式（兼容旧数据）
func (mgr *Manager) GetAnyTxById(txID string) (*pb.AnyTx, error) {
	// 1. 优先尝试从新的 txraw_ 前缀读取（交易原文，不可变）
	rawKey := KeyTxRaw(txID)
	if rawData, err := mgr.Read(rawKey); err == nil && rawData != "" {
		anyTx := &pb.AnyTx{}
		if err := ProtoUnmarshal([]byte(rawData), anyTx); err != nil {
			return nil, fmt.Errorf("failed to unmarshal txraw data: %v", err)
		}
		return anyTx, nil
	}

	// 2. 回退：读取旧的通用 key "anyTx_<txID>"（兼容旧数据）
	anyKey := KeyAnyTx(txID)
	specificKey, err := mgr.Read(anyKey)
	if err != nil {
		return nil, fmt.Errorf("failed to read anyTx key %s: %v", anyKey, err)
	}
	if specificKey == "" {
		return nil, fmt.Errorf("no anyTx record for txID %s", txID)
	}

	// 3. 根据专用 key读取实际交易数据
	txData, err := mgr.Read(specificKey)
	if err != nil {
		return nil, fmt.Errorf("failed to read transaction data for key %s: %v", specificKey, err)
	}
	if txData == "" {
		return nil, fmt.Errorf("empty transaction data for key %s", specificKey)
	}

	// 4. 根据 specificKey 的前缀判断类型并反序列化
	// 注意：key 格式是 v1_tx_xxx, v1_order_xxx, v1_minerTx_xxx 等
	anyTx := &pb.AnyTx{}
	switch {
	case strings.Contains(specificKey, "_tx_"):
		var tx pb.Transaction
		if err := ProtoUnmarshal([]byte(txData), &tx); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Transaction: %v", err)
		}
		anyTx.Content = &pb.AnyTx_Transaction{Transaction: &tx}
	case strings.Contains(specificKey, "_order_"):
		var tx pb.OrderTx
		if err := ProtoUnmarshal([]byte(txData), &tx); err != nil {
			return nil, fmt.Errorf("failed to unmarshal OrderTx: %v", err)
		}
		anyTx.Content = &pb.AnyTx_OrderTx{OrderTx: &tx}
	case strings.Contains(specificKey, "_minerTx_"):
		var tx pb.MinerTx
		if err := ProtoUnmarshal([]byte(txData), &tx); err != nil {
			return nil, fmt.Errorf("failed to unmarshal MinerTx: %v", err)
		}
		anyTx.Content = &pb.AnyTx_MinerTx{MinerTx: &tx}
	case strings.Contains(specificKey, "_issuetoken_"):
		var tx pb.IssueTokenTx
		if err := ProtoUnmarshal([]byte(txData), &tx); err != nil {
			return nil, fmt.Errorf("failed to unmarshal IssueTokenTx: %v", err)
		}
		anyTx.Content = &pb.AnyTx_IssueTokenTx{IssueTokenTx: &tx}
	case strings.Contains(specificKey, "_freeze_"):
		var tx pb.FreezeTx
		if err := ProtoUnmarshal([]byte(txData), &tx); err != nil {
			return nil, fmt.Errorf("failed to unmarshal FreezeTx: %v", err)
		}
		anyTx.Content = &pb.AnyTx_FreezeTx{FreezeTx: &tx}
	case strings.Contains(specificKey, "_witnessstake_"):
		var tx pb.WitnessStakeTx
		if err := ProtoUnmarshal([]byte(txData), &tx); err != nil {
			return nil, fmt.Errorf("failed to unmarshal WitnessStakeTx: %v", err)
		}
		anyTx.Content = &pb.AnyTx_WitnessStakeTx{WitnessStakeTx: &tx}
	default:
		// 尝试直接反序列化为 AnyTx（兼容旧数据）
		if err := ProtoUnmarshal([]byte(txData), anyTx); err != nil {
			return nil, fmt.Errorf("failed to unmarshal AnyTx data (key=%s): %v", specificKey, err)
		}
	}
	return anyTx, nil
}

// GetTxReceipt 获取交易回执
func (mgr *Manager) GetTxReceipt(txID string) (*pb.Receipt, error) {
	// 1. 获取状态
	statusKey := KeyVMAppliedTx(txID)
	status, err := mgr.Read(statusKey)
	if err != nil || status == "" {
		return nil, fmt.Errorf("transaction not applied or not found")
	}

	// 2. 获取错误（可能有也可能没有）
	errorKey := KeyVMTxError(txID)
	errMsg, _ := mgr.Read(errorKey)

	// 3. 获取高度
	heightKey := KeyVMTxHeight(txID)
	heightStr, _ := mgr.Read(heightKey)
	var height uint64
	if heightStr != "" {
		fmt.Sscanf(heightStr, "%d", &height)
	}

	// 4. 重组为 pb.Receipt
	receipt := &pb.Receipt{
		TxId:        txID,
		Status:      status,
		Error:       errMsg,
		BlockHeight: height,
	}

	return receipt, nil
}
