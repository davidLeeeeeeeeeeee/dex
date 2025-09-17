package db

import "fmt"

func NewZeroTokenBalance() *TokenBalance {
	return &TokenBalance{
		Balance:                "0",
		CandidateLockedBalance: "0",
		MinerLockedBalance:     "0",
		LiquidLockedBalance:    "0",
		WitnessLockedBalance:   "0",
		LeverageLockedBalance:  "0",
	}
}

func SaveAnyTx(mgr *Manager, anyTx *AnyTx) error {
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
	mainKey := "anyTx_" + txID
	mgr.EnqueueSet(mainKey, string(data))

	// 3. 如果 BaseMessage 是 PENDING，则写 "pending_anytx_<txID>"
	base := anyTx.GetBase()
	if base != nil && base.Status == Status_PENDING {
		mgr.EnqueueSet("pending_anytx_"+txID, "")
	} else {
		mgr.EnqueueDelete("pending_anytx_" + txID)
	}

	// 4. 调度到不同的落库/索引函数
	switch content := anyTx.GetContent().(type) {
	case *AnyTx_OrderTx:
		err = SaveOrderTx(mgr, content.OrderTx)
	case *AnyTx_MinerTx:
		err = SaveMinerTx(mgr, content.MinerTx)
	case *AnyTx_Transaction:
		err = SaveTransaction(mgr, content.Transaction)
	//case *AnyTx_FreezeTx:
	//	err = saveFreezeTxIndex(mgr, content.FreezeTx)
	//case *AnyTx_IssueTokenTx:
	//	err = saveIssueTokenTxIndex(mgr, content.IssueTokenTx)

	default:
		// 对没有额外索引需求的Tx，可不做任何额外操作
	}
	return err
}
