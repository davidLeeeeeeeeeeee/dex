package handlers

import (
	"compress/gzip"
	"dex/db"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// 处理获取区块请求
func (hm *HandlerManager) HandleGetBlock(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleGetBlock")
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	var req db.GetBlockRequest
	if err := proto.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, "Invalid GetBlockRequest proto", http.StatusBadRequest)
		return
	}

	// 使用注入的dbManager而不是创建新实例
	block, err := hm.dbManager.GetBlock(req.Height)
	if err != nil || block == nil {
		resp := &db.GetBlockResponse{
			Error: fmt.Sprintf("Block not found at height %d", req.Height),
		}
		respBytes, _ := proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusNotFound)
		w.Write(respBytes)
		return
	}

	resp := &db.GetBlockResponse{
		Block: block,
	}
	respBytes, err := proto.Marshal(resp)
	if err != nil {
		http.Error(w, "Failed to marshal GetBlockResponse", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)

	gz := gzip.NewWriter(w)
	defer gz.Close()
	gz.Write(respBytes)
}
