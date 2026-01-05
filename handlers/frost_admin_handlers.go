// handlers/frost_admin_handlers.go
// Frost 管理接口

package handlers

import (
	"encoding/json"
	"net/http"
	"runtime"
	"sync"
	"time"
)

// HealthResponse 健康检查响应
type HealthResponse struct {
	Status    string `json:"status"`
	Uptime    string `json:"uptime"`
	Version   string `json:"version"`
	GoVersion string `json:"go_version"`
	NumCPU    int    `json:"num_cpu"`
}

// MetricsResponse 指标响应
type MetricsResponse struct {
	HeapAlloc      uint64 `json:"heap_alloc"`
	HeapSys        uint64 `json:"heap_sys"`
	NumGoroutine   int    `json:"num_goroutine"`
	FrostJobs      int    `json:"frost_jobs"`
	FrostWithdraws int    `json:"frost_withdraws"`
}

// ForceRescanResponse 强制重扫响应
type ForceRescanResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

var startTime = time.Now()

// RescanCallback 重扫回调函数
type RescanCallback func(chain string, fromHeight uint64) error

var (
	rescanCallback     RescanCallback
	rescanCallbackOnce sync.Once
	rescanCallbackMu   sync.RWMutex
)

// SetRescanCallback 设置重扫回调函数
func SetRescanCallback(cb RescanCallback) {
	rescanCallbackMu.Lock()
	defer rescanCallbackMu.Unlock()
	rescanCallback = cb
}

// GetRescanCallback 获取重扫回调函数
func GetRescanCallback() RescanCallback {
	rescanCallbackMu.RLock()
	defer rescanCallbackMu.RUnlock()
	return rescanCallback
}

// HandleGetHealth 健康检查
func (hm *HandlerManager) HandleGetHealth(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("GetHealth")

	resp := HealthResponse{
		Status:    "healthy",
		Uptime:    time.Since(startTime).String(),
		Version:   "1.0.0",
		GoVersion: runtime.Version(),
		NumCPU:    runtime.NumCPU(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// HandleGetMetrics 获取指标
func (hm *HandlerManager) HandleGetMetrics(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("GetMetrics")

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	// 统计 Frost 相关数据
	frostJobs := 0
	frostWithdraws := 0

	// 扫描 jobs
	jobResults, _ := hm.dbManager.Scan("v1_frost_job_")
	if jobResults != nil {
		frostJobs = len(jobResults)
	}

	// 扫描 withdraws
	withdrawResults, _ := hm.dbManager.Scan("v1_frost_withdraw_")
	if withdrawResults != nil {
		frostWithdraws = len(withdrawResults)
	}

	resp := MetricsResponse{
		HeapAlloc:      m.HeapAlloc,
		HeapSys:        m.HeapSys,
		NumGoroutine:   runtime.NumGoroutine(),
		FrostJobs:      frostJobs,
		FrostWithdraws: frostWithdraws,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ForceRescanRequest 强制重扫请求
type ForceRescanRequest struct {
	Chain      string `json:"chain"`       // 链标识（可选，空表示所有链）
	FromHeight uint64 `json:"from_height"` // 起始高度（可选，0 表示从最新）
}

// HandleForceRescan 强制重扫
func (hm *HandlerManager) HandleForceRescan(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("ForceRescan")

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 解析请求
	var req ForceRescanRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
	}

	// 获取重扫回调
	callback := GetRescanCallback()
	if callback == nil {
		resp := ForceRescanResponse{
			Success: false,
			Message: "rescan callback not configured",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	// 触发重扫
	if err := callback(req.Chain, req.FromHeight); err != nil {
		resp := ForceRescanResponse{
			Success: false,
			Message: "rescan failed: " + err.Error(),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	resp := ForceRescanResponse{
		Success: true,
		Message: "rescan triggered for chain: " + req.Chain,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
