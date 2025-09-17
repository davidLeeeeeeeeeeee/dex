package handlers

import (
	"dex/db"
	"dex/logs"
	"fmt"
	"net/http"
	"strings"
)

// ExtractTxId 从 AnyTx 中提取其 BaseMessage.TxId
func ExtractTxId(a *db.AnyTx) (string, error) {
	switch tx := a.GetContent().(type) {
	case *db.AnyTx_IssueTokenTx:
		if tx.IssueTokenTx.Base == nil {
			return "", fmt.Errorf("IssueTokenTx base is nil")
		}
		return tx.IssueTokenTx.Base.TxId, nil

	case *db.AnyTx_FreezeTx:
		if tx.FreezeTx.Base == nil {
			return "", fmt.Errorf("FreezeTx base is nil")
		}
		return tx.FreezeTx.Base.TxId, nil

	case *db.AnyTx_Transaction:
		if tx.Transaction.Base == nil {
			return "", fmt.Errorf("Transaction base is nil")
		}
		return tx.Transaction.Base.TxId, nil

	case *db.AnyTx_OrderTx:
		if tx.OrderTx.Base == nil {
			return "", fmt.Errorf("OrderTx base is nil")
		}
		return tx.OrderTx.Base.TxId, nil

	case *db.AnyTx_AddressTx:
		if tx.AddressTx.Base == nil {
			return "", fmt.Errorf("AddressTx base is nil")
		}
		return tx.AddressTx.Base.TxId, nil

	case *db.AnyTx_CandidateTx:
		if tx.CandidateTx.Base == nil {
			return "", fmt.Errorf("CandidateTx base is nil")
		}
		return tx.CandidateTx.Base.TxId, nil

	default:
		return "", fmt.Errorf("unrecognized tx type")
	}
}

var AUTH_ENABLED = false

func CheckAuth(r *http.Request) bool {
	if !AUTH_ENABLED {
		return true
	}
	clientIP := strings.Split(r.RemoteAddr, ":")[0]
	dbMgr, err := db.NewManager("")
	if err != nil {
		logs.Error("CheckAuth GetInstance err : %v", err)
		return false
	}
	info, err := db.GetClientInfo(dbMgr, clientIP)
	if err != nil {
		logs.Debug("CheckAuth GetClientInfo err : %v key: %s \n", err, clientIP)
		return false
	}
	logs.Debug("CheckAuth info: %s", info.Ip)
	if err != nil {
		// 数据库中无记录，说明未认证
		return false
	}
	return info.GetAuthed()
}
