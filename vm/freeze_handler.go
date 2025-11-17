package vm

import (
	"dex/keys"
	"dex/pb"
	"encoding/json"
	"fmt"
)

// FreezeTxHandler 冻结/解冻Token交易处理器
type FreezeTxHandler struct{}

func (h *FreezeTxHandler) Kind() string {
	return "freeze"
}

func (h *FreezeTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 1. 提取FreezeTx
	freezeTx, ok := tx.GetContent().(*pb.AnyTx_FreezeTx)
	if !ok {
		return nil, &Receipt{
			TxID:   tx.GetTxId(),
			Status: "FAILED",
			Error:  "not a freeze transaction",
		}, fmt.Errorf("not a freeze transaction")
	}

	freeze := freezeTx.FreezeTx
	if freeze == nil || freeze.Base == nil {
		return nil, &Receipt{
			TxID:   tx.GetTxId(),
			Status: "FAILED",
			Error:  "invalid freeze transaction",
		}, fmt.Errorf("invalid freeze transaction")
	}

	// 2. 验证Token是否存在
	tokenKey := fmt.Sprintf("token_%s", freeze.TokenAddr)
	tokenData, tokenExists, err := sv.Get(tokenKey)
	if err != nil {
		return nil, &Receipt{
			TxID:   freeze.Base.TxId,
			Status: "FAILED",
			Error:  "read token failed",
		}, err
	}

	if !tokenExists {
		return nil, &Receipt{
			TxID:   freeze.Base.TxId,
			Status: "FAILED",
			Error:  "token not found",
		}, fmt.Errorf("token not found: %s", freeze.TokenAddr)
	}

	// 解析Token数据
	var token pb.Token
	if err := json.Unmarshal(tokenData, &token); err != nil {
		return nil, &Receipt{
			TxID:   freeze.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse token data",
		}, err
	}

	// 3. 验证操作者是否是Token的owner
	if token.Owner != freeze.Base.FromAddress {
		return nil, &Receipt{
			TxID:   freeze.Base.TxId,
			Status: "FAILED",
			Error:  "only token owner can freeze/unfreeze",
		}, fmt.Errorf("only token owner can freeze/unfreeze: owner=%s, from=%s", token.Owner, freeze.Base.FromAddress)
	}

	// 4. 读取目标账户
	targetAccountKey := keys.KeyAccount(freeze.TargetAddr)
	targetAccountData, targetExists, err := sv.Get(targetAccountKey)
	if err != nil {
		return nil, &Receipt{
			TxID:   freeze.Base.TxId,
			Status: "FAILED",
			Error:  "read target account failed",
		}, err
	}

	if !targetExists {
		return nil, &Receipt{
			TxID:   freeze.Base.TxId,
			Status: "FAILED",
			Error:  "target account not found",
		}, fmt.Errorf("target account not found: %s", freeze.TargetAddr)
	}

	// 解析目标账户
	var targetAccount pb.Account
	if err := json.Unmarshal(targetAccountData, &targetAccount); err != nil {
		return nil, &Receipt{
			TxID:   freeze.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse target account data",
		}, err
	}

	// 5. 执行冻结/解冻逻辑
	// 这里使用一个特殊的key来标记账户的某个token是否被冻结
	freezeKey := keys.KeyFreeze(freeze.TargetAddr, freeze.TokenAddr)
	
	ws := make([]WriteOp, 0)

	if freeze.Freeze {
		// 冻结：设置冻结标记
		ws = append(ws, WriteOp{
			Key:         freezeKey,
			Value:       []byte("true"),
			Del:         false,
			SyncStateDB: true, // ✨ 改为 true，支持轻节点同步
			Category:    "freeze",
		})
	} else {
		// 解冻：删除冻结标记
		ws = append(ws, WriteOp{
			Key:         freezeKey,
			Value:       nil,
			Del:         true,
			SyncStateDB: true, // ✨ 改为 true，支持轻节点同步
			Category:    "freeze",
		})
	}

	// 6. 记录冻结/解冻历史
	historyKey := keys.KeyFreezeHistory(freeze.Base.TxId)
	historyData, _ := json.Marshal(freeze)
	ws = append(ws, WriteOp{
		Key:         historyKey,
		Value:       historyData,
		Del:         false,
		SyncStateDB: false,
		Category:    "history",
	})

	// 7. 返回执行结果
	action := "frozen"
	if !freeze.Freeze {
		action = "unfrozen"
	}

	rc := &Receipt{
		TxID:       freeze.Base.TxId,
		Status:     "SUCCEED",
		WriteCount: len(ws),
		Error:      fmt.Sprintf("token %s for account %s", action, freeze.TargetAddr),
	}

	return ws, rc, nil
}

func (h *FreezeTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}

