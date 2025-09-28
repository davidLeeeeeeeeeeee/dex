package handlers

import (
	"compress/gzip"
	"dex/db"
	"dex/logs"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
)

func HandleGetBlock(w http.ResponseWriter, r *http.Request) {
	// 1. 可选：权限校验
	//if !CheckAuth(r) {
	//	http.Error(w, "Unauthorized", http.StatusUnauthorized)
	//	return
	//}

	// 2. 读取请求体
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	// 3. 反序列化为 GetBlockRequest
	var req db.GetBlockRequest
	if err := proto.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, "Invalid GetBlockRequest proto", http.StatusBadRequest)
		return
	}
	height := req.Height

	// 4. 调用 DB 层的 GetBlock
	mgr, err := db.NewManager("")
	if err != nil {
		http.Error(w, "Database not available", http.StatusInternalServerError)
		return
	}
	// 这里已经拿到 block 了:
	block, err := mgr.GetBlock(height)
	if err != nil || block == nil {
		// 返回错误
		return
	}

	// 1. 构造 GetBlockResponse
	var resp *db.GetBlockResponse
	resp = &db.GetBlockResponse{
		Block: block,
	}

	respBytes, err := proto.Marshal(resp)
	if err != nil {
		http.Error(w, "Failed to marshal GetBlockResponse", http.StatusInternalServerError)
		return
	}

	// 2. 添加响应头：说明我们在 body 里放的是 gzip 压缩的数据
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)

	// 3. 创建一个 gzip.Writer，包裹住 w
	gz := gzip.NewWriter(w)
	defer gz.Close()

	// 4. 写入响应
	if _, err := gz.Write(respBytes); err != nil {
		logs.Error("HandleGetBlock: gzip write error: %v", err)
		return
	}

	// gz.Close() 会在 defer 里执行，刷新剩余数据并写到client
}
