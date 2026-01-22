package handlers

import (
	"dex/pb"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// HandleGetToken 处理获取 Token 信息请求
func (hm *HandlerManager) HandleGetToken(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleGetToken")
	if !hm.checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	var req pb.GetTokenRequest
	if err := proto.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, "Invalid GetTokenRequest proto", http.StatusBadRequest)
		return
	}

	token, err := hm.dbManager.GetToken(req.TokenAddress)
	if err != nil {
		http.Error(w, "Failed to get token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp := &pb.GetTokenResponse{
		Token: token,
	}

	respData, _ := proto.Marshal(resp)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(respData)
}

// HandleGetTokenRegistry 处理获取 Token 注册表请求
func (hm *HandlerManager) HandleGetTokenRegistry(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleGetTokenRegistry")
	if !hm.checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	registry, err := hm.dbManager.GetTokenRegistry()
	if err != nil {
		http.Error(w, "Failed to get token registry: "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp := &pb.GetTokenRegistryResponse{
		Registry: registry,
	}

	respData, _ := proto.Marshal(resp)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(respData)
}
