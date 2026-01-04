// handlers/frost_query_handlers.go
// Frost 只读查询 API

package handlers

import (
	"dex/pb"
	"encoding/json"
	"net/http"
	"strconv"

	"google.golang.org/protobuf/proto"
)

// GetFrostConfigResponse Frost 配置响应
type GetFrostConfigResponse struct {
	SupportedChains  []string `json:"supported_chains"`
	DefaultThreshold int      `json:"default_threshold"`
	VaultCount       int      `json:"vault_count"`
	MinWithdraw      string   `json:"min_withdraw"`
}

// GetWithdrawStatusResponse 提现状态响应
type GetWithdrawStatusResponse struct {
	LotID    string `json:"lot_id"`
	Chain    string `json:"chain"`
	Status   string `json:"status"` // QUEUED | SIGNING | SIGNED | BROADCAST
	Amount   string `json:"amount"`
	Receiver string `json:"receiver"`
	Height   uint64 `json:"height"`
	TxHash   string `json:"tx_hash,omitempty"`
}

// ListWithdrawsResponse 提现列表响应
type ListWithdrawsResponse struct {
	Withdraws []GetWithdrawStatusResponse `json:"withdraws"`
	Total     int                         `json:"total"`
}

// HandleGetFrostConfig 获取 Frost 配置
func (hm *HandlerManager) HandleGetFrostConfig(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("GetFrostConfig")

	// TODO: 从配置/DB 读取真实值
	resp := GetFrostConfigResponse{
		SupportedChains:  []string{"BTC", "ETH", "SOL", "TRX"},
		DefaultThreshold: 7,
		VaultCount:       10,
		MinWithdraw:      "0.001",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// HandleGetWithdrawStatus 获取提现状态
func (hm *HandlerManager) HandleGetWithdrawStatus(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("GetWithdrawStatus")

	lotID := r.URL.Query().Get("lot_id")
	if lotID == "" {
		http.Error(w, "missing lot_id", http.StatusBadRequest)
		return
	}

	// 从 DB 读取 WithdrawState
	key := "v1_frost_withdraw_" + lotID
	data, err := hm.dbManager.Get(key)
	if err != nil || data == nil {
		http.Error(w, "withdraw not found", http.StatusNotFound)
		return
	}

	var state pb.FrostWithdrawState
	if err := proto.Unmarshal(data, &state); err != nil {
		http.Error(w, "invalid state data", http.StatusInternalServerError)
		return
	}

	resp := GetWithdrawStatusResponse{
		LotID:    state.WithdrawId,
		Chain:    state.Chain,
		Status:   state.Status,
		Amount:   state.Amount,
		Receiver: state.To,
		Height:   state.RequestHeight,
		TxHash:   state.JobId, // 使用 job_id 作为标识
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// HandleListWithdraws 列出提现
func (hm *HandlerManager) HandleListWithdraws(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("ListWithdraws")

	chain := r.URL.Query().Get("chain")
	status := r.URL.Query().Get("status")
	limitStr := r.URL.Query().Get("limit")

	limit := 20
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	// 扫描所有 lot
	prefix := "v1_frost_lot_"
	if chain != "" {
		prefix = "v1_frost_lot_" + chain + "_"
	}

	results, err := hm.dbManager.Scan(prefix)
	if err != nil {
		http.Error(w, "scan failed", http.StatusInternalServerError)
		return
	}

	var withdraws []GetWithdrawStatusResponse
	for _, data := range results {
		var state pb.FrostWithdrawState
		if err := proto.Unmarshal(data, &state); err != nil {
			continue
		}

		// 过滤 status
		if status != "" && state.Status != status {
			continue
		}

		withdraws = append(withdraws, GetWithdrawStatusResponse{
			LotID:    state.WithdrawId,
			Chain:    state.Chain,
			Status:   state.Status,
			Amount:   state.Amount,
			Receiver: state.To,
			Height:   state.RequestHeight,
		})

		if len(withdraws) >= limit {
			break
		}
	}

	resp := ListWithdrawsResponse{
		Withdraws: withdraws,
		Total:     len(withdraws),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
