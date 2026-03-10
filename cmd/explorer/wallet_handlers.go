package main

// wallet_handlers.go — 钱包专用转发接口
// 接收钱包的普通 JSON 请求，转发给节点（Protobuf），返回 JSON 响应
// 钱包无需处理 Protobuf 和 HTTP/3，全部在此处适配

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"

	"dex/pb"
	"dex/utils"

	"github.com/btcsuite/btcd/chaincfg"
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
	if node == "" && s.nodeDB == nil {
		writeJSON(w, walletNonceResponse{Address: req.Address, Error: "no node available"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()

	var nonce uint64
	if node != "" {
		protoReq := &pb.GetAccountRequest{Address: req.Address}
		var protoResp pb.GetAccountResponse
		if err := s.fetchProto(ctx, node, "/getaccount", protoReq, &protoResp); err == nil {
			if protoResp.Error != "" && !strings.Contains(strings.ToLower(protoResp.Error), "not found") {
				writeJSON(w, walletNonceResponse{
					Address: req.Address,
					Error:   "failed to fetch nonce from node: " + protoResp.Error,
				})
				return
			}
			if protoResp.Account != nil {
				nonce = protoResp.Account.Nonce
			}
		} else if s.nodeDB != nil {
			if acc, localErr := localGetAccount(s.nodeDB, req.Address); localErr == nil && acc != nil {
				nonce = acc.Nonce
			} else {
				writeJSON(w, walletNonceResponse{
					Address: req.Address,
					Error:   "failed to fetch nonce from node: " + err.Error(),
				})
				return
			}
		} else {
			writeJSON(w, walletNonceResponse{
				Address: req.Address,
				Error:   "failed to fetch nonce from node: " + err.Error(),
			})
			return
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

// ─── /api/wallet/deposit-address ─────────────────────────────────────────────
// 为用户查询指定链的充值地址（从激活的 Vault 中随机选一个，计算 tweaked taproot 地址）
// 请求: { "node": "127.0.0.1:6000", "chain": "BTC", "receiver_address": "bc1q..." }
// 响应: { "vault_id": 0, "vault_pubkey": "02...", "deposit_address": "bc1p...", "script_pubkey": "5120..." }

type depositAddressRequest struct {
	Node            string `json:"node"`
	Chain           string `json:"chain"`
	ReceiverAddress string `json:"receiver_address"`
}

type depositAddressResponse struct {
	VaultID        uint32 `json:"vault_id"`
	VaultPubkey    string `json:"vault_pubkey"`    // GroupPubkey hex（原始，用于展示/验证）
	DepositAddress string `json:"deposit_address"` // bc1p... tweaked taproot 地址
	ScriptPubkey   string `json:"script_pubkey"`   // 5120... hex（34 字节 P2TR scriptPubKey）
	Error          string `json:"error,omitempty"`
}

const maxVaultProbe = 200 // 最多探测 200 个 vault slot

func (s *server) handleWalletDepositAddress(w http.ResponseWriter, r *http.Request) {
	addWalletCORS(w, r)
	if r.Method == http.MethodOptions {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req depositAddressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, depositAddressResponse{Error: "invalid request: " + err.Error()})
		return
	}
	if req.Chain == "" {
		writeJSON(w, depositAddressResponse{Error: "chain is required"})
		return
	}
	if req.ReceiverAddress == "" {
		writeJSON(w, depositAddressResponse{Error: "receiver_address is required"})
		return
	}
	// 规范化链名，尝试大小写两种格式
	chainCandidates := []string{strings.ToUpper(req.Chain), strings.ToLower(req.Chain), req.Chain}

	type vaultCandidate struct {
		id          uint32
		groupPubkey []byte
		chain       string
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()

	var candidates []vaultCandidate
	for _, chain := range chainCandidates {
		for vaultID := uint32(0); vaultID < maxVaultProbe; vaultID++ {
			var vaultResp pb.GetVaultGroupPubKeyResponse
			path := fmt.Sprintf("/frost/vault/group_pubkey?chain=%s&vault_id=%d", chain, vaultID)
			err := s.fetchProto(ctx, req.Node, path, nil, &vaultResp)
			if err != nil {
				// 获取不到该 vault，说明该编号无数据，终止该链下探测
				break
			}
			if vaultResp.Status == "ACTIVE" || vaultResp.Status == "KEY_READY" {
				groupPubkey, err := hex.DecodeString(vaultResp.GroupPubkey)
				if err == nil && len(groupPubkey) > 0 {
					candidates = append(candidates, vaultCandidate{
						id:          vaultID,
						groupPubkey: groupPubkey,
						chain:       chain,
					})
				}
			}
		}
		if len(candidates) > 0 {
			break // 找到后不继续探测其他链名格式
		}
	}

	if len(candidates) == 0 {
		writeJSON(w, depositAddressResponse{Error: fmt.Sprintf("no active vault found for chain %s", req.Chain)})
		return
	}

	// 随机选一个
	chosen := candidates[rand.Intn(len(candidates))]

	// 计算 tweaked taproot scriptPubKey
	scriptPubKey, err := utils.BuildTweakedTaprootScriptPubKey(chosen.groupPubkey, req.ReceiverAddress)
	if err != nil {
		writeJSON(w, depositAddressResponse{Error: "failed to compute tweaked pubkey: " + err.Error()})
		return
	}

	// 从 scriptPubKey 提取 x-only pubkey，生成 taproot 地址
	xOnly, err := utils.ParseTaprootScriptPubKeyXOnly(scriptPubKey)
	if err != nil {
		writeJSON(w, depositAddressResponse{Error: "failed to parse tweaked scriptPubKey: " + err.Error()})
		return
	}
	depositAddr, err := utils.TaprootAddressFromXOnly(xOnly, &chaincfg.TestNet3Params)
	if err != nil {
		writeJSON(w, depositAddressResponse{Error: "failed to generate taproot address: " + err.Error()})
		return
	}

	writeJSON(w, depositAddressResponse{
		VaultID:        chosen.id,
		VaultPubkey:    hex.EncodeToString(chosen.groupPubkey),
		DepositAddress: depositAddr,
		ScriptPubkey:   hex.EncodeToString(scriptPubKey),
	})
}
