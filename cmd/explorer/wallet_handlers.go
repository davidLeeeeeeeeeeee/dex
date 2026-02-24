package main

// wallet_handlers.go — 钱包专用转发接口
// 接收钱包的普通 JSON 请求，转发给节点（Protobuf），返回 JSON 响应
// 钱包无需处理 Protobuf 和 HTTP/3，全部在此处适配

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"dex/pb"

	"google.golang.org/protobuf/proto"
)

// ─── /api/wallet/submittx ────────────────────────────────────────────────────
// 接收已签名的 AnyTx（base64 编码的 Protobuf 字节），转发给节点 /tx
// 请求: { "node": "127.0.0.1:6000", "tx": "<base64>" }
// 响应: { "ok": true } 或 { "error": "..." }

type walletSubmitTxRequest struct {
	Node string `json:"node"`
	Tx   string `json:"tx"` // base64 编码的 AnyTx protobuf 字节
}

type walletSubmitTxResponse struct {
	Ok    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func (s *server) handleWalletSubmitTx(w http.ResponseWriter, r *http.Request) {
	addWalletCORS(w, r)
	if r.Method == http.MethodOptions {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req walletSubmitTxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, walletSubmitTxResponse{Error: "invalid request: " + err.Error()})
		return
	}

	txBytes, err := base64.StdEncoding.DecodeString(req.Tx)
	if err != nil {
		writeJSON(w, walletSubmitTxResponse{Error: "invalid base64: " + err.Error()})
		return
	}

	// 验证是合法的 AnyTx
	var anyTx pb.AnyTx
	if err := proto.Unmarshal(txBytes, &anyTx); err != nil {
		writeJSON(w, walletSubmitTxResponse{Error: "invalid AnyTx protobuf: " + err.Error()})
		return
	}

	node := strings.TrimSpace(req.Node)
	if node == "" && len(s.defaultNodes) > 0 {
		node = s.defaultNodes[0]
	}
	if node == "" {
		writeJSON(w, walletSubmitTxResponse{Error: "no node available"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()

	nodeURL := "https://" + node + "/tx"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, nodeURL, bytes.NewReader(txBytes))
	if err != nil {
		writeJSON(w, walletSubmitTxResponse{Error: err.Error()})
		return
	}
	httpReq.Header.Set("Content-Type", "application/x-protobuf")

	resp, err := s.client.Do(httpReq)
	if err != nil {
		writeJSON(w, walletSubmitTxResponse{Error: "node request failed: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		writeJSON(w, walletSubmitTxResponse{Error: "node rejected tx, status: " + resp.Status})
		return
	}

	writeJSON(w, walletSubmitTxResponse{Ok: true})
}

// ─── /api/wallet/nonce ───────────────────────────────────────────────────────
// 查询账户 nonce（即下一笔交易应使用的序号）
// 请求: { "node": "127.0.0.1:6000", "address": "0x..." }
// 响应: { "address": "...", "nonce": 5 }

type walletNonceRequest struct {
	Node    string `json:"node"`
	Address string `json:"address"`
}

type walletNonceResponse struct {
	Address string `json:"address"`
	Nonce   uint64 `json:"nonce"`
	Error   string `json:"error,omitempty"`
}

func (s *server) handleWalletNonce(w http.ResponseWriter, r *http.Request) {
	addWalletCORS(w, r)
	if r.Method == http.MethodOptions {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req walletNonceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, walletNonceResponse{Error: "invalid request"})
		return
	}
	if req.Address == "" {
		writeJSON(w, walletNonceResponse{Error: "address is required"})
		return
	}

	node := strings.TrimSpace(req.Node)
	if node == "" && len(s.defaultNodes) > 0 {
		node = s.defaultNodes[0]
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()

	var nonce uint64
	if node != "" {
		protoReq := &pb.GetAccountRequest{Address: req.Address}
		var protoResp pb.GetAccountResponse
		if err := s.fetchProto(ctx, node, "/getaccount", protoReq, &protoResp); err == nil && protoResp.Account != nil {
			nonce = protoResp.Account.Nonce
		}
	} else if s.nodeDB != nil {
		if acc, err := localGetAccount(s.nodeDB, req.Address); err == nil && acc != nil {
			nonce = acc.Nonce
		}
	}

	writeJSON(w, walletNonceResponse{Address: req.Address, Nonce: nonce})
}

// ─── /api/wallet/receipt ─────────────────────────────────────────────────────
// 查询交易收据
// 请求: { "node": "127.0.0.1:6000", "tx_id": "abc123" }
// 响应: { "tx_id": "...", "status": "SUCCEED", "error": "" }

type walletReceiptRequest struct {
	Node string `json:"node"`
	TxID string `json:"tx_id"`
}

type walletReceiptResponse struct {
	TxID        string `json:"tx_id"`
	Status      string `json:"status"`
	BlockHeight uint64 `json:"block_height,omitempty"`
	Error       string `json:"error,omitempty"`
}

func (s *server) handleWalletReceipt(w http.ResponseWriter, r *http.Request) {
	addWalletCORS(w, r)
	if r.Method == http.MethodOptions {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req walletReceiptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, walletReceiptResponse{Error: "invalid request"})
		return
	}
	if req.TxID == "" {
		writeJSON(w, walletReceiptResponse{Error: "tx_id is required"})
		return
	}

	node := strings.TrimSpace(req.Node)
	if node == "" && len(s.defaultNodes) > 0 {
		node = s.defaultNodes[0]
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()

	if node != "" {
		receipt, err := s.fetchReceipt(ctx, node, req.TxID)
		if err != nil {
			writeJSON(w, walletReceiptResponse{TxID: req.TxID, Error: err.Error()})
			return
		}
		if receipt == nil {
			writeJSON(w, walletReceiptResponse{TxID: req.TxID, Error: "receipt not found"})
			return
		}
		writeJSON(w, walletReceiptResponse{
			TxID:        receipt.TxId,
			Status:      receipt.Status,
			BlockHeight: receipt.BlockHeight,
			Error:       receipt.Error,
		})
		return
	}

	// fallback: 从本地 nodeDB 查
	if s.nodeDB != nil {
		receipt, err := localGetTxReceipt(s.nodeDB, req.TxID)
		if err == nil && receipt != nil {
			writeJSON(w, walletReceiptResponse{
				TxID:        receipt.TxId,
				Status:      receipt.Status,
				BlockHeight: receipt.BlockHeight,
				Error:       receipt.Error,
			})
			return
		}
	}

	writeJSON(w, walletReceiptResponse{TxID: req.TxID, Error: "receipt not found"})
}

// ─── 工具函数 ─────────────────────────────────────────────────────────────────

// addWalletCORS 添加 CORS 头，允许钱包插件（chrome-extension://...）跨域请求
func addWalletCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = "*"
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}
