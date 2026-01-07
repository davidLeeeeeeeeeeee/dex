// handlers/frost_admin_handlers.go
// Frost 管理接口

package handlers

import (
	"dex/pb"
	"io"
	"net/http"
	"runtime"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
)

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

	resp := &pb.HealthResponse{
		Status:    "healthy",
		Uptime:    time.Since(startTime).String(),
		Version:   "1.0.0",
		GoVersion: runtime.Version(),
		NumCpu:    int32(runtime.NumCPU()),
	}

	// 序列化为 protobuf
	data, err := proto.Marshal(resp)
	if err != nil {
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(data)
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

	resp := &pb.MetricsResponse{
		HeapAlloc:      m.HeapAlloc,
		HeapSys:        m.HeapSys,
		NumGoroutine:   int32(runtime.NumGoroutine()),
		FrostJobs:      int32(frostJobs),
		FrostWithdraws: int32(frostWithdraws),
	}

	// 序列化为 protobuf
	data, err := proto.Marshal(resp)
	if err != nil {
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(data)
}

// HandleForceRescan 强制重扫
func (hm *HandlerManager) HandleForceRescan(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("ForceRescan")

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 解析请求（protobuf）
	var req pb.ForceRescanRequest
	if r.ContentLength > 0 {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		if len(body) > 0 {
			if err := proto.Unmarshal(body, &req); err != nil {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
		}
	}

	// 获取重扫回调
	callback := GetRescanCallback()
	if callback == nil {
		resp := &pb.ForceRescanResponse{
			Success: false,
			Message: "rescan callback not configured",
		}
		data, err := proto.Marshal(resp)
		if err != nil {
			http.Error(w, "failed to marshal response", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write(data)
		return
	}

	// 触发重扫
	if err := callback(req.Chain, req.FromHeight); err != nil {
		resp := &pb.ForceRescanResponse{
			Success: false,
			Message: "rescan failed: " + err.Error(),
		}
		data, err := proto.Marshal(resp)
		if err != nil {
			http.Error(w, "failed to marshal response", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write(data)
		return
	}

	resp := &pb.ForceRescanResponse{
		Success: true,
		Message: "rescan triggered for chain: " + req.Chain,
	}

	// 序列化为 protobuf
	data, err := proto.Marshal(resp)
	if err != nil {
		http.Error(w, "failed to marshal response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(data)
}
