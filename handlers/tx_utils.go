package handlers

import (
	"dex/db"
	"dex/logs"
	"dex/pb"
	"fmt"
	"net/http"
	"strings"
)

// ExtractTxId 从 AnyTx 中提取其 BaseMessage.TxId
func ExtractTxId(a *pb.AnyTx) (string, error) {
	switch tx := a.GetContent().(type) {
	case *pb.AnyTx_IssueTokenTx:
		if tx.IssueTokenTx.Base == nil {
			return "", fmt.Errorf("IssueTokenTx base is nil")
		}
		return tx.IssueTokenTx.Base.TxId, nil

	case *pb.AnyTx_FreezeTx:
		if tx.FreezeTx.Base == nil {
			return "", fmt.Errorf("FreezeTx base is nil")
		}
		return tx.FreezeTx.Base.TxId, nil

	case *pb.AnyTx_Transaction:
		if tx.Transaction.Base == nil {
			return "", fmt.Errorf("Transaction base is nil")
		}
		return tx.Transaction.Base.TxId, nil

	case *pb.AnyTx_OrderTx:
		if tx.OrderTx.Base == nil {
			return "", fmt.Errorf("OrderTx base is nil")
		}
		return tx.OrderTx.Base.TxId, nil

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
	dbPath := ""
	dbMgr, err := db.NewManager(dbPath, logs.NewNodeLogger("test", 0))
	if err != nil {
		logs.Error("CheckAuth GetInstance err : %v", err)
		return false
	}
	// 为了在 CheckAuth 中使用 hm.Logger，我们需要重构此函数
	// 目前临时保持现状，但显式传递一个 Logger 或改为 HandlerManager 的方法
	// 鉴于目前这是一个工具函数，先保持 logs 库调用或注入
	// TODO: 彻底 DI 化
	info, err := dbMgr.GetClientInfo(clientIP)
	if err != nil {
		// 数据库中无记录，说明未认证
		return false
	}
	return info.GetAuthed()
}
