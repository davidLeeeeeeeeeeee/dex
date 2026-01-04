// handlers/frost_admin_handlers.go
// Frost 管理接口

package handlers

import (
	"encoding/json"
	"net/http"
	"runtime"
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

// HandleForceRescan 强制重扫
func (hm *HandlerManager) HandleForceRescan(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("ForceRescan")

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// TODO: 触发 Scanner 重扫
	// 这里只是占位实现

	resp := ForceRescanResponse{
		Success: true,
		Message: "rescan triggered",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
