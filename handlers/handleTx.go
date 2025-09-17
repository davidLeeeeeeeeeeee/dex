package handlers

import (
	"dex/db"
	"dex/txpool"
	"google.golang.org/protobuf/proto"
	"io"
	"net"
	"net/http"
)

func HandleTx(w http.ResponseWriter, r *http.Request) {
	// 1. 先 CheckAuth
	//if !CheckAuth(r) {
	//	http.Error(w, "Unauthorized", http.StatusUnauthorized)
	//	return
	//}

	//clientIP := strings.Split(r.RemoteAddr, ":")[0]
	//dbMgr, err := db.GetInstance("")
	//if err != nil {
	//	http.Error(w, "DB not available", http.StatusInternalServerError)
	//	return
	//}

	//info, err := db.GetClientInfo(dbMgr, clientIP)
	//if err != nil || !info.GetAuthed() {
	//	http.Error(w, "Unauthorized", http.StatusUnauthorized)
	//	return
	//}

	// 2. 读取并解析 protobuf: AnyTx
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	var incomingAny db.AnyTx
	if err := proto.Unmarshal(bodyBytes, &incomingAny); err != nil {
		http.Error(w, "Invalid AnyTx proto", http.StatusBadRequest)
		return
	}

	// 把剩余的处理逻辑（共识校验、存入TxPool、广播等）“排队”到 TxPoolQueue
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	msg := &txpool.TxPoolMessage{
		Type:  txpool.MsgAddTx,
		AnyTx: &incomingAny,
		IP:    host,
		OnAdded: func(txID string) {
			// 这里才调用 sender
			//sender.BroadcastTx(txID)
		},
	}
	txpool.GlobalTxPoolQueue.Enqueue(msg)

	// 返回protobuf响应
	resProto := &db.StatusResponse{
		Status: "ok",
		Info:   "Tx received",
	}
	resBytes, _ := proto.Marshal(resProto)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(resBytes)
}
