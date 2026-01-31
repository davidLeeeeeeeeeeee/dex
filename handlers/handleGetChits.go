package handlers

import (
	"encoding/json"
	"io"
	"net/http"

	"dex/pb"

	"google.golang.org/protobuf/proto"
)

// GetChitsResponse chits 查询响应
type GetChitsResponse struct {
	Height      uint64      `json:"height"`
	BlockID     string      `json:"block_id,omitempty"`
	TotalVotes  int         `json:"total_votes"`
	FinalizedAt int64       `json:"finalized_at,omitempty"`
	Chits       []ChitEntry `json:"chits,omitempty"`
	Error       string      `json:"error,omitempty"`
}

// ChitEntry 单个投票信息
type ChitEntry struct {
	NodeID      string `json:"node_id"`
	PreferredID string `json:"preferred_id"`
	Timestamp   int64  `json:"timestamp"`
}

// HandleGetChits 处理获取区块最终化投票信息请求
func (hm *HandlerManager) HandleGetChits(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleGetChits")

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeChitsError(w, "Failed to read request body")
		return
	}

	// 使用 GetBlockRequest 作为请求结构（复用 height 字段）
	var req pb.GetBlockRequest
	if err := proto.Unmarshal(bodyBytes, &req); err != nil {
		writeChitsError(w, "Invalid request proto")
		return
	}

	if hm.consensusManager == nil {
		writeChitsError(w, "Consensus manager not available")
		return
	}

	// 通过 ConsensusNodeManager 获取 chits
	chits := hm.consensusManager.GetFinalizationChits(req.Height)
	if chits == nil {
		resp := GetChitsResponse{
			Height: req.Height,
			Error:  "Finalization chits not found for this height",
		}
		writeChitsJSON(w, resp)
		return
	}

	// 构建响应
	resp := GetChitsResponse{
		Height:      chits.Height,
		BlockID:     chits.BlockID,
		TotalVotes:  chits.TotalVotes,
		FinalizedAt: chits.FinalizedAt,
		Chits:       make([]ChitEntry, 0, len(chits.Chits)),
	}

	for _, c := range chits.Chits {
		resp.Chits = append(resp.Chits, ChitEntry{
			NodeID:      c.NodeID,
			PreferredID: c.PreferredID,
			Timestamp:   c.Timestamp,
		})
	}

	writeChitsJSON(w, resp)
}

func writeChitsJSON(w http.ResponseWriter, resp GetChitsResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

func writeChitsError(w http.ResponseWriter, errMsg string) {
	resp := GetChitsResponse{Error: errMsg}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(resp)
}
