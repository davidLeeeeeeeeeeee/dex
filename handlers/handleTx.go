package handlers

import (
	"dex/db"
	"fmt"
	"google.golang.org/protobuf/proto"
	"io"
	"net"
	"net/http"
)

// HandleTx 处理交易提交
func (hm *HandlerManager) HandleTx(w http.ResponseWriter, r *http.Request) {
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

	host, _, _ := net.SplitHostPort(r.RemoteAddr)

	// 直接提交给TxPool，不再关心内部队列
	err = hm.txPool.SubmitTx(&incomingAny, host, func(txID string) {
		// 广播回调
		hm.senderManager.BroadcastTx(&incomingAny)
	})

	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to submit tx: %v", err), http.StatusInternalServerError)
		return
	}

	resProto := &db.StatusResponse{
		Status: "ok",
		Info:   "Tx received",
	}
	resBytes, _ := proto.Marshal(resProto)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(resBytes)
}
