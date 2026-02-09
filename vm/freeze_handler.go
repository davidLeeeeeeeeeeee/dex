package vm

import (
	"dex/keys"
	"dex/pb"
	"fmt"

	"google.golang.org/protobuf/proto"
)

// FreezeTxHandler 鍐荤粨/瑙ｅ喕Token浜ゆ槗澶勭悊鍣?
type FreezeTxHandler struct{}

func (h *FreezeTxHandler) Kind() string {
	return "freeze"
}

func (h *FreezeTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 1. 鎻愬彇FreezeTx
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

	// 2. 楠岃瘉Token鏄惁瀛樺湪
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

	// 瑙ｆ瀽Token鏁版嵁
	var token pb.Token
	if err := unmarshalProtoCompat(tokenData, &token); err != nil {
		return nil, &Receipt{
			TxID:   freeze.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse token data",
		}, err
	}

	// 3. 楠岃瘉鎿嶄綔鑰呮槸鍚︽槸Token鐨刼wner
	if token.Owner != freeze.Base.FromAddress {
		return nil, &Receipt{
			TxID:   freeze.Base.TxId,
			Status: "FAILED",
			Error:  "only token owner can freeze/unfreeze",
		}, fmt.Errorf("only token owner can freeze/unfreeze: owner=%s, from=%s", token.Owner, freeze.Base.FromAddress)
	}

	// 4. 璇诲彇鐩爣璐︽埛
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

	// 瑙ｆ瀽鐩爣璐︽埛
	var targetAccount pb.Account
	if err := unmarshalProtoCompat(targetAccountData, &targetAccount); err != nil {
		return nil, &Receipt{
			TxID:   freeze.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse target account data",
		}, err
	}

	// 5. 鎵ц鍐荤粨/瑙ｅ喕閫昏緫
	// 杩欓噷浣跨敤涓€涓壒娈婄殑key鏉ユ爣璁拌处鎴风殑鏌愪釜token鏄惁琚喕缁?
	freezeKey := keys.KeyFreeze(freeze.TargetAddr, freeze.TokenAddr)

	ws := make([]WriteOp, 0)

	if freeze.Freeze {
		// 鍐荤粨锛氳缃喕缁撴爣璁?
		ws = append(ws, WriteOp{
			Key:         freezeKey,
			Value:       []byte("true"),
			Del:         false,
			SyncStateDB: true, // 鉁?鏀逛负 true锛屾敮鎸佽交鑺傜偣鍚屾
			Category:    "freeze",
		})
	} else {
		// 瑙ｅ喕锛氬垹闄ゅ喕缁撴爣璁?
		ws = append(ws, WriteOp{
			Key:         freezeKey,
			Value:       nil,
			Del:         true,
			SyncStateDB: true, // 鉁?鏀逛负 true锛屾敮鎸佽交鑺傜偣鍚屾
			Category:    "freeze",
		})
	}

	// 6. 璁板綍鍐荤粨/瑙ｅ喕鍘嗗彶
	historyKey := keys.KeyFreezeHistory(freeze.Base.TxId)
	historyData, _ := proto.Marshal(freeze)
	ws = append(ws, WriteOp{
		Key:         historyKey,
		Value:       historyData,
		Del:         false,
		SyncStateDB: false,
		Category:    "history",
	})

	// 7. 杩斿洖鎵ц缁撴灉
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
