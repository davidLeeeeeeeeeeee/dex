package db

import (
	"dex/pb"
	"fmt"
)

func NewZeroTokenBalance() *pb.TokenBalance {
	return &pb.TokenBalance{
		Balance:                "0",
		CandidateLockedBalance: "0",
		MinerLockedBalance:     "0",
		LiquidLockedBalance:    "0",
		WitnessLockedBalance:   "0",
		LeverageLockedBalance:  "0",
	}
}

// SaveAnyTx 改为成员函数
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
	mainKey := KeyAnyTx(txID)
	mgr.EnqueueSet(mainKey, string(data))

	// 3. 如果 BaseMessage 是 PENDING，则写 "pending_anytx_<txID>"
	base := anyTx.GetBase()
	if base != nil && base.Status == pb.Status_PENDING {
		mgr.EnqueueSet(KeyPendingAnyTx(txID), "")
	} else {
		mgr.EnqueueDelete(KeyPendingAnyTx(txID))
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
