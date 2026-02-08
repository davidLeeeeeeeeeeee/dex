package handlers

import (
	"dex/keys"
	"dex/pb"
	"io"
	"net/http"
	"strings"

	"google.golang.org/protobuf/proto"
)

// HandleGetAccountBalances 处理获取账户余额的请求
// 从分离存储 v1_balance_{address}_{token} 中扫描所有余额
func (hm *HandlerManager) HandleGetAccountBalances(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleGetAccountBalances")

	// 读取请求体
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		resp := &pb.GetAccountBalancesResponse{Error: "failed to read request body"}
		data, _ := proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write(data)
		return
	}

	var req pb.GetAccountBalancesRequest
	if err := proto.Unmarshal(bodyBytes, &req); err != nil {
		resp := &pb.GetAccountBalancesResponse{Error: "invalid request proto"}
		data, _ := proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write(data)
		return
	}

	if req.Address == "" {
		resp := &pb.GetAccountBalancesResponse{Error: "address is required"}
		data, _ := proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write(data)
		return
	}

	// 扫描该地址的所有余额
	balances := hm.scanAccountBalances(req.Address)

	resp := &pb.GetAccountBalancesResponse{Balances: balances}
	data, _ := proto.Marshal(resp)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(data)
}

// scanAccountBalances 从数据库扫描某地址所有代币余额
// 使用 KV 前缀扫描（余额数据双写到 KV 和 StateDB，从 KV 读取即可）
func (hm *HandlerManager) scanAccountBalances(address string) map[string]*pb.TokenBalance {
	balances := make(map[string]*pb.TokenBalance)
	prefix := keys.KeyBalancePrefix(address)

	// 从 KV 扫描（余额在 applyResult 时双写到 KV 和 StateDB）
	kvResults, err := hm.dbManager.ScanByPrefix(prefix, 100)
	if err != nil {
		return balances
	}
	for key, val := range kvResults {
		token := extractTokenFromBalanceKey(key, address)
		if token == "" {
			continue
		}
		var record pb.TokenBalanceRecord
		if err := proto.Unmarshal([]byte(val), &record); err == nil && record.Balance != nil {
			balances[token] = record.Balance
		}
	}
	return balances
}

// extractTokenFromBalanceKey 从余额 key 中提取 token 地址
// key 格式: v1_balance_{address}_{token}
func extractTokenFromBalanceKey(key, address string) string {
	// prefix = "v1_balance_{address}_"
	prefix := keys.KeyBalancePrefix(address)
	if !strings.HasPrefix(key, prefix) {
		return ""
	}
	return key[len(prefix):]
}
