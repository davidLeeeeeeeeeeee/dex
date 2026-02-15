package handlers

import (
	"dex/pb"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"google.golang.org/protobuf/proto"
)

// HandleTx 处理交易提交
func (hm *HandlerManager) HandleTx(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleTx")
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	var incomingAny pb.AnyTx
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
		status := http.StatusInternalServerError
		errStr := err.Error()
		if strings.Contains(errStr, "txpool queue is full") || strings.Contains(errStr, "txpool pending is full") {
			status = http.StatusTooManyRequests
		}
		http.Error(w, fmt.Sprintf("Failed to submit tx: %v", err), status)
		return
	}

	resProto := &pb.StatusResponse{
		Status: "ok",
		Info:   "Tx received",
	}
	resBytes, _ := proto.Marshal(resProto)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(resBytes)
}
