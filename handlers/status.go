package handlers

import (
	"dex/db"
	"google.golang.org/protobuf/proto"
	"net/http"
)

func HandleStatus(w http.ResponseWriter, r *http.Request) {
	if !CheckAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// 构造 proto 响应
	resProto := &db.StatusResponse{
		Status: "ok",
		Info:   "Server is running",
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
