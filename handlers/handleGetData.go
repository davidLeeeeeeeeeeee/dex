package handlers

import (
	"dex/db"
	"dex/txpool"
	"dex/utils"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// HandleGetData 处理来自对等节点的 /getdata 请求。
// 客户端会发送 GetData 消息（包含 tx_id），服务器查找该 tx（例如 AnyTx 或 Transaction），
// 并将完整交易数据返回给请求者。

func HandleGetData(w http.ResponseWriter, r *http.Request) {
	// 1. 校验权限（可选）
	if !CheckAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// 2. 读取请求体
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read getdata request body", http.StatusBadRequest)
		return
	}

	// 3. 反序列化 GetData 消息
	var getDataMsg db.GetData
	if err := proto.Unmarshal(bodyBytes, &getDataMsg); err != nil {
		http.Error(w, "Invalid GetData proto", http.StatusBadRequest)
		return
	}
	if getDataMsg.TxId == "" {
		http.Error(w, "Empty tx_id in GetData", http.StatusBadRequest)
		return
	}

	// 4. 优先从 TxPool 按 txID 直接查
	pool := txpool.GetInstance()
	txFromPool := pool.GetTransactionById(getDataMsg.TxId)
	if txFromPool != nil {
		// 说明在内存池就找到了
		if respData, err := proto.Marshal(txFromPool); err == nil {
			w.Header().Set("Content-Type", "application/x-protobuf")
			w.WriteHeader(http.StatusOK)
			w.Write(respData)
			return
		} else {
			http.Error(w, "Failed to marshal transaction from TxPool", http.StatusInternalServerError)
			return
		}
	}

	// 5. 如果TxPool里没有，就去数据库查
	mgr, err := db.NewManager(utils.Port)
	if err != nil {
		http.Error(w, "Database not available", http.StatusInternalServerError)
		return
	}
	anyTx, err := db.GetAnyTxById(mgr, getDataMsg.TxId)
	if err != nil || anyTx == nil {
		http.Error(w, fmt.Sprintf("Transaction with tx_id %s not found", getDataMsg.TxId), http.StatusNotFound)
		return
	}

	// 6. 序列化返回
	respData, err := proto.Marshal(anyTx)
	if err != nil {
		http.Error(w, "Failed to marshal transaction", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(respData)
}
