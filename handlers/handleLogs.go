package handlers

import (
	"dex/logs"
	"dex/pb"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// HandleLogs 处理获取日志请求
func (hm *HandlerManager) HandleLogs(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleLogs")

	var req pb.LogsRequest
	if r.Method == http.MethodPost {
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil {
			proto.Unmarshal(bodyBytes, &req)
		}
	}

	logLines := logs.GetLogsForNode(hm.address)

	// 如果指定了最大行数，进行截断
	if req.MaxLines > 0 && int(req.MaxLines) < len(logLines) {
		logLines = logLines[len(logLines)-int(req.MaxLines):]
	}

	resp := &pb.LogsResponse{
		Logs: logLines,
	}

	data, err := proto.Marshal(resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(data)
}
