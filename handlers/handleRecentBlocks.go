package handlers

import (
	"compress/gzip"
	"dex/pb"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

func (hm *HandlerManager) HandleGetRecentBlocks(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleGetRecentBlocks")
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	var req pb.GetRecentBlocksRequest
	if err := proto.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, "Invalid GetRecentBlocksRequest proto", http.StatusBadRequest)
		return
	}

	count := int(req.Count)
	if count <= 0 {
		count = 10
	}
	if count > 100 {
		count = 100
	}

	_, lastHeight := hm.consensusManager.GetLastAccepted()

	var headers []*pb.BlockHeader

	for i := 0; i < count; i++ {
		if lastHeight == 0 {
			break
		}
		// Fetch block
		block, err := hm.dbManager.GetBlock(lastHeight)
		if err != nil || block == nil {
			// 可能遇到gap或还没同步到? 尝试继续找前一个
			// 但如果是高度 1 找不到，通常是没数据
			if lastHeight == 1 {
				break
			}
			lastHeight--
			continue
		}

		// Create header
		header := &pb.BlockHeader{
			Height:            block.Height,
			BlockHash:         block.BlockHash,
			TxsHash:           block.TxsHash,
			Miner:             block.Miner,
			TxCount:           int32(len(block.Body)),
			AccumulatedReward: block.AccumulatedReward,
			Window:            block.Window,
		}
		headers = append(headers, header)
		lastHeight--
	}

	resp := &pb.GetRecentBlocksResponse{
		Blocks: headers,
	}

	respBytes, err := proto.Marshal(resp)
	if err != nil {
		http.Error(w, "Failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)

	gz := gzip.NewWriter(w)
	defer gz.Close()
	gz.Write(respBytes)
}
