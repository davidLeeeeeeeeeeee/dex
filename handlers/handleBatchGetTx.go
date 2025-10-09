package handlers

import (
	"dex/db"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// 处理批量获取交易请求
func (hm *HandlerManager) HandleBatchGetTx(w http.ResponseWriter, r *http.Request) {

	hm.Stats.RecordAPICall("HandleBatchGetTx")
	if !hm.checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	var req db.BatchGetShortTxRequest
	if err := proto.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, "Invalid BatchGetDataRequest proto", http.StatusBadRequest)
		return
	}

	matchedTxs := hm.txPool.GetTxsByShortHashes(req.ShortHashes, true)
	resp := &db.BatchGetShortTxResponse{
		Transactions: matchedTxs,
	}

	respBytes, _ := proto.Marshal(resp)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(respBytes)
}
