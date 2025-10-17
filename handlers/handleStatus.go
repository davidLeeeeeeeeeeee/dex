package handlers

import (
	"dex/db"
	"fmt"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// 处理状态查询
func (hm *HandlerManager) HandleStatus(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleStatus")
	if !hm.checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	resProto := &db.StatusResponse{
		Status: "ok",
		Info:   fmt.Sprintf("Server is running on port %s", hm.port),
	}
	data, err := proto.Marshal(resProto)
	if err != nil {
		http.Error(w, "Failed to marshal status response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}
