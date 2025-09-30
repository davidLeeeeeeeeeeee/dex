package handlers

import (
	"dex/db"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// HandleGetData 处理来自对等节点的 /getdata 请求。
// 客户端会发送 GetData 消息（包含 tx_id），服务器查找该 tx（例如 AnyTx 或 Transaction），
// 并将完整交易数据返回给请求者。

// HandleGetData 处理获取交易数据请求
func (hm *HandlerManager) HandleGetData(w http.ResponseWriter, r *http.Request) {
	if !hm.checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read getdata request body", http.StatusBadRequest)
		return
	}

	var getDataMsg db.GetData
	if err := proto.Unmarshal(bodyBytes, &getDataMsg); err != nil {
		http.Error(w, "Invalid GetData proto", http.StatusBadRequest)
		return
	}

	// 先从TxPool查找
	txFromPool := hm.txPool.GetTransactionById(getDataMsg.TxId)
	if txFromPool != nil {
		respData, _ := proto.Marshal(txFromPool)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
		w.Write(respData)
		return
	}

	// 从数据库查找
	anyTx, err := hm.dbManager.GetAnyTxById(getDataMsg.TxId)
	if err != nil || anyTx == nil {
		http.Error(w, fmt.Sprintf("Transaction %s not found", getDataMsg.TxId), http.StatusNotFound)
		return
	}

	respData, _ := proto.Marshal(anyTx)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(respData)
}

// handlers/handleGetBlockByID.go
func (hm *HandlerManager) HandleGetBlockByID(w http.ResponseWriter, r *http.Request) {
	if !hm.checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req db.GetBlockByIDRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid GetBlockByID proto", http.StatusBadRequest)
		return
	}
	blk, err := hm.dbManager.GetBlockByID(req.BlockId)
	if err != nil || blk == nil {
		http.Error(w, fmt.Sprintf("Block %s not found", req.BlockId), http.StatusNotFound)
		return
	}
	resp := &db.GetBlockResponse{Block: blk}
	bytes, _ := proto.Marshal(resp)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(bytes)
}
