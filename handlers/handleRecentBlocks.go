package handlers

import (
	"compress/gzip"
	"dex/logs"
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

	var summaries []*pb.BlockSummary

	for i := 0; i < count; i++ {
		if lastHeight == 0 {
			break
		}
		var block *pb.Block
		finalizedAttempted := false
		if hm.consensusManager != nil {
			if blockID, ok := hm.consensusManager.GetFinalizedBlockID(lastHeight); ok {
				finalizedAttempted = true
				b, err := hm.dbManager.GetBlockByID(blockID)
				if err == nil && b != nil {
					block = b
				} else {
					logs.Warn("[HandleGetRecentBlocks] Finalized block %s at height %d not found in DB: %v",
						blockID, lastHeight, err)
				}
			}
		}

		if block == nil && !finalizedAttempted {
			if blocksAtHeight, err := hm.dbManager.GetBlocksByHeight(lastHeight); err == nil && len(blocksAtHeight) > 1 {
				logs.Warn("[HandleGetRecentBlocks] Multiple blocks at height %d (count=%d); returning first",
					lastHeight, len(blocksAtHeight))
			}
			b, err := hm.dbManager.GetBlock(lastHeight)
			if err == nil && b != nil {
				block = b
			}
		}

		if block == nil {
			// 可能遇到gap或还没同步到? 尝试继续找前一个
			// 但如果是高度 1 找不到，通常是没数据
			if lastHeight == 1 {
				break
			}
			lastHeight--
			continue
		}

		// Create BlockSummary
		summary := &pb.BlockSummary{
			Height:            block.Header.Height,
			BlockHash:         block.BlockHash,
			TxsHash:           block.Header.TxsHash,
			Miner:             block.Header.Miner,
			TxCount:           int32(len(block.Body)),
			AccumulatedReward: block.AccumulatedReward,
			Window:            block.Header.Window,
		}
		summaries = append(summaries, summary)
		lastHeight--
	}

	resp := &pb.GetRecentBlocksResponse{
		Blocks: summaries,
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
