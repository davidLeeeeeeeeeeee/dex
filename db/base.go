package db

import (
	"dex/keys"
	"dex/pb"
	"fmt"
)

func NewZeroTokenBalance() *pb.TokenBalance {
	return &pb.TokenBalance{
		Balance:               "0",
		MinerLockedBalance:    "0",
		LiquidLockedBalance:   "0",
		WitnessLockedBalance:  "0",
		LeverageLockedBalance: "0",
	}
}

// SaveTxRaw 保存交易原文（不可变）
// 这是唯一用于保存交易原始数据的函数，独立于订单簿状态
// 使用独立 key: v1_txraw_<txid>
//
// 设计原则：
// - 交易原文是不可变的，用于交易查询
// - 订单簿状态是可变的，由 VM 的 Diff 机制控制
// - 两者使用不同的 key 空间，彻底解耦
func (mgr *Manager) SaveTxRaw(anyTx *pb.AnyTx) error {
	txID := anyTx.GetTxId()
	if txID == "" {
		return fmt.Errorf("SaveTxRaw: empty txID not allowed")
	}
	// 序列化交易原文
	data, err := ProtoMarshal(anyTx)
	if err != nil {
		return err
	}
	// 只存储到 txraw_ 前缀，不写任何索引
	rawKey := keys.KeyTxRaw(txID)
	mgr.EnqueueSet(rawKey, string(data))
	return nil
}

// GetTxRaw 获取交易原文
func (mgr *Manager) GetTxRaw(txID string) (*pb.AnyTx, error) {
	rawKey := keys.KeyTxRaw(txID)
	data, err := mgr.Read(rawKey)
	if err != nil {
		return nil, err
	}
	if data == "" {
		return nil, fmt.Errorf("tx raw not found: %s", txID)
	}
	anyTx := &pb.AnyTx{}
	if err := ProtoUnmarshal([]byte(data), anyTx); err != nil {
		return nil, err
	}
	return anyTx, nil
}

// SaveAnyTx saves an AnyTx to the database
//
// ⚠️ INTERNAL API - DO NOT CALL DIRECTLY FROM OUTSIDE DB PACKAGE
// This method is used internally by db package for legacy compatibility.
// New code should use VM's unified write path (applyResult) instead.
//
// Deprecated: Use VM's WriteOp mechanism for all state changes.
// Deprecated: Use SaveTxRaw for transaction raw data storage.
func (mgr *Manager) SaveAnyTx(anyTx *pb.AnyTx) error {
	txID := anyTx.GetTxId()
	if txID == "" {
		return fmt.Errorf("SaveAnyTx: empty txID not allowed")
	}
	// 1. 序列化
	data, err := ProtoMarshal(anyTx)
	if err != nil {
		return err
	}
	// 2. 主存储
	mainKey := keys.KeyAnyTx(txID)
	mgr.EnqueueSet(mainKey, string(data))

	// 3. 如果 BaseMessage 是 PENDING，则写 "pending_anytx_<txID>"
	base := anyTx.GetBase()
	if base != nil && base.Status == pb.Status_PENDING {
		mgr.EnqueueSet(keys.KeyPendingAnyTx(txID), "")
	} else {
		mgr.EnqueueDelete(keys.KeyPendingAnyTx(txID))
	}

	// 4. 调度到不同的落库/索引函数
	switch content := anyTx.GetContent().(type) {
	case *pb.AnyTx_OrderTx:
		err = mgr.SaveOrderTx(content.OrderTx)
	case *pb.AnyTx_MinerTx:
		err = mgr.SaveMinerTx(content.MinerTx)
	case *pb.AnyTx_Transaction:
		err = mgr.SaveTransaction(content.Transaction)

	default:
		// 对没有额外索引需求的Tx，可不做任何额外操作
	}
	return err
}
