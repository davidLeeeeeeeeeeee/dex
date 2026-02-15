package handlers

import (
	"compress/gzip"
	"dex/logs"
	"dex/pb"
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

	var req pb.GetBlockRequest
	if err := proto.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, "Invalid GetBlockRequest proto", http.StatusBadRequest)
		return
	}

	var block *pb.Block
	source := "unknown"
	finalizedAttempted := false
	if hm.consensusManager != nil {
		if blockID, ok := hm.consensusManager.GetFinalizedBlockID(req.Height); ok {
			finalizedAttempted = true
			b, err := hm.dbManager.GetBlockByID(blockID)
			if err == nil && b != nil {
				block = b
				source = "finalized_db"
			} else {
				logs.Warn("[HandleGetBlock] Finalized block %s at height %d not found in DB: %v",
					blockID, req.Height, err)
			}
		} else {
			logs.Warn("[HandleGetBlock] No finalized block at height %d; fallback to non-finalized", req.Height)
		}
	}

	if block == nil && !finalizedAttempted {
		if blocksAtHeight, err := hm.dbManager.GetBlocksByHeight(req.Height); err == nil && len(blocksAtHeight) > 1 {
			logs.Warn("[HandleGetBlock] Multiple blocks at height %d (count=%d); returning first",
				req.Height, len(blocksAtHeight))
		}
		b, err := hm.dbManager.GetBlock(req.Height)
		if err == nil && b != nil {
			block = b
			source = "height_fallback_db"
		}
	}

	if block == nil {
		resp := &pb.GetBlockResponse{
			Error: fmt.Sprintf("Block not found at height %d", req.Height),
		}
		respBytes, err := proto.Marshal(resp)
		if err != nil {
			logs.Error("[HandleGetBlock] Failed to marshal not-found response: height=%d err=%v", req.Height, err)
			http.Error(w, fmt.Sprintf("Failed to marshal GetBlockResponse: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusNotFound)
		w.Write(respBytes)
		return
	}

	resp := &pb.GetBlockResponse{
		Block: block,
	}
	respBytes, err := proto.Marshal(resp)
	if err != nil {
		detail := buildGetBlockMarshalDetail(block.BlockHash, source, block, err)
		logs.Error("[HandleGetBlock] Failed to marshal GetBlockResponse: %s", detail)
		http.Error(w, "Failed to marshal GetBlockResponse: "+detail, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)

	gz := gzip.NewWriter(w)
	defer gz.Close()
	gz.Write(respBytes)
}
