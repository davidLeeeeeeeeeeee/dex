package handlers

import (
	"dex/pb"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// HandleGetAccount 处理获取账户信息的请求
func (hm *HandlerManager) HandleGetAccount(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleGetAccount")

	// 读取请求体
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		resp := &pb.GetAccountResponse{Error: "failed to read request body"}
		data, _ := proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write(data)
		return
	}

	var req pb.GetAccountRequest
	if err := proto.Unmarshal(bodyBytes, &req); err != nil {
		resp := &pb.GetAccountResponse{Error: "invalid request proto"}
		data, _ := proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write(data)
		return
	}

	if req.Address == "" {
		resp := &pb.GetAccountResponse{Error: "address is required"}
		data, _ := proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write(data)
		return
	}

	// 从数据库获取账户信息
	account, err := hm.dbManager.GetAccount(req.Address)
	if err != nil {
		resp := &pb.GetAccountResponse{Error: "account not found: " + err.Error()}
		data, _ := proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write(data)
		return
	}

	// 返回账户信息
	resp := &pb.GetAccountResponse{Account: account}
	data, _ := proto.Marshal(resp)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(data)
}
