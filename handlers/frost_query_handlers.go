// handlers/frost_query_handlers.go
// Frost 只读查询 API

package handlers

import (
	"dex/config"
	"dex/keys"
	"dex/pb"
	"encoding/json"
	"net/http"
	"strconv"

	"google.golang.org/protobuf/proto"
)

// GetFrostConfigResponse Frost 配置响应
type GetFrostConfigResponse struct {
	Enabled          bool                `json:"enabled"`
	SupportedChains  []string            `json:"supported_chains"`
	DefaultThreshold float64             `json:"default_threshold"`
	VaultCount       int                 `json:"vault_count"`
	CommitteeSize    int                 `json:"committee_size"`
	MinWithdraw      string              `json:"min_withdraw"`
	Chains           map[string]ChainCfg `json:"chains,omitempty"`
}

// ChainCfg 链级别配置
type ChainCfg struct {
	SignAlgo       string `json:"sign_algo"`
	FrostVariant   string `json:"frost_variant,omitempty"`
	VaultsPerChain int    `json:"vaults_per_chain"`
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

	// 先尝试从 DB 读取配置
	var frostCfg config.FrostConfig
	cfgKey := keys.KeyFrostConfig()
	data, err := hm.dbManager.Get(cfgKey)
	if err != nil || data == nil {
		// DB 没有配置，使用默认配置
		frostCfg = config.DefaultFrostConfig()
	} else {
		// 尝试反序列化
		if err := json.Unmarshal(data, &frostCfg); err != nil {
			frostCfg = config.DefaultFrostConfig()
		}
	}

	// 构建支持的链列表
	supportedChains := make([]string, 0, len(frostCfg.Chains))
	chainCfgs := make(map[string]ChainCfg)
	for chainName, chainCfg := range frostCfg.Chains {
		supportedChains = append(supportedChains, chainName)
		chainCfgs[chainName] = ChainCfg{
			SignAlgo:       chainCfg.SignAlgo,
			FrostVariant:   chainCfg.FrostVariant,
			VaultsPerChain: chainCfg.VaultsPerChain,
		}
	}

	// 计算总 vault 数量
	totalVaults := 0
	for _, c := range frostCfg.Chains {
		totalVaults += c.VaultsPerChain
	}

	resp := GetFrostConfigResponse{
		Enabled:          frostCfg.Enabled,
		SupportedChains:  supportedChains,
		DefaultThreshold: frostCfg.Vault.ThresholdRatio,
		VaultCount:       totalVaults,
		CommitteeSize:    frostCfg.Vault.DefaultK,
		MinWithdraw:      "0.001", // TODO: 从配置读取
		Chains:           chainCfgs,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// HandleGetWithdrawStatus 获取提现状态
func (hm *HandlerManager) HandleGetWithdrawStatus(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("GetWithdrawStatus")

	withdrawID := r.URL.Query().Get("withdraw_id")
	if withdrawID == "" {
		// 兼容旧参数名
		withdrawID = r.URL.Query().Get("lot_id")
	}
	if withdrawID == "" {
		http.Error(w, "missing withdraw_id", http.StatusBadRequest)
		return
	}

	// 从 DB 读取 WithdrawState（使用统一的 key 格式）
	key := keys.KeyFrostWithdraw(withdrawID)
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

	// 扫描所有提现（使用统一的 key 前缀）
	// v1_frost_withdraw_ 前缀
	prefix := "v1_frost_withdraw_"

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

		// 过滤 chain
		if chain != "" && state.Chain != chain {
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
