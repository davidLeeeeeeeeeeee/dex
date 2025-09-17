package handlers

import (
	"dex/db"
	"dex/logs"
	"dex/txpool"
	"io"
	"net/http"
	"time"

	"google.golang.org/protobuf/proto"
)

// HandleBatchGetTx 处理批量获取交易请求
func HandleBatchGetTx(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	defer func() {
		elapsed := time.Since(start)
		logs.Info("[HandleBatchGetTx] elapsed=%s", elapsed)
	}()
	if !CheckAuth(r) {
		http.Error(w, "[HandleBatchGetTx]Unauthorized", http.StatusUnauthorized)
		return
	}
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "[HandleBatchGetTx]Failed to read request body", http.StatusBadRequest)
		return
	}
	var req db.BatchGetShortTxRequest
	if err := proto.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, "[HandleBatchGetTx]Invalid BatchGetDataRequest proto", http.StatusBadRequest)
		return
	}
	if len(req.ShortHashes) == 0 {
		http.Error(w, "[HandleBatchGetTx]No short hashes provided", http.StatusBadRequest)
		return
	}
	pool := txpool.GetInstance()
	matchedTxs := pool.GetTxsByShortHashes(req.ShortHashes, true)

	resp := &db.BatchGetShortTxResponse{
		Transactions: matchedTxs,
	}
	respBytes, err := proto.Marshal(resp)
	if err != nil {
		http.Error(w, "[HandleBatchGetTx]Failed to marshal response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(respBytes)
}
