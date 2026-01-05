// handlers/handleFrostMsg.go
// Frost P2P 消息处理端点

package handlers

import (
	"crypto/ecdsa"
	"io"
	"net/http"
	"sync"

	"dex/frost/security"
	"dex/logs"
	"dex/pb"
	"dex/types"

	"google.golang.org/protobuf/proto"
)

// FrostMsgVerifier Frost 消息验证器
type FrostMsgVerifier struct {
	// 公钥查询函数（从地址获取公钥）
	GetPublicKey func(address string) (*ecdsa.PublicKey, error)
	// 序号防重放验证器
	ReplayGuard *security.SeqReplayGuard
	// 是否启用签名验证（可用于开发/测试时禁用）
	EnableSigVerify bool
	// 是否启用防重放（可用于开发/测试时禁用）
	EnableReplayGuard bool
}

var (
	// 全局 Frost 消息验证器
	frostMsgVerifier     *FrostMsgVerifier
	frostMsgVerifierOnce sync.Once
)

// GetFrostMsgVerifier 获取或创建 Frost 消息验证器
func GetFrostMsgVerifier() *FrostMsgVerifier {
	frostMsgVerifierOnce.Do(func() {
		frostMsgVerifier = &FrostMsgVerifier{
			ReplayGuard:       security.NewSeqReplayGuard(1000), // 允许最大 1000 的 seq 跳跃
			EnableSigVerify:   false,                            // TODO: 生产环境需要启用
			EnableReplayGuard: false,                            // TODO: 生产环境需要启用
		}
	})
	return frostMsgVerifier
}

// SetFrostMsgVerifier 设置 Frost 消息验证器（用于测试）
func SetFrostMsgVerifier(v *FrostMsgVerifier) {
	frostMsgVerifier = v
}

// HandleFrostMsg 处理 Frost P2P 消息
// POST /frostmsg
func (hm *HandlerManager) HandleFrostMsg(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleFrostMsg")

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	// 解析 FrostEnvelope protobuf
	var envelope pb.FrostEnvelope
	if err := proto.Unmarshal(bodyBytes, &envelope); err != nil {
		http.Error(w, "Invalid FrostEnvelope proto", http.StatusBadRequest)
		return
	}

	// 获取发送方地址
	senderAddress := envelope.From
	if senderAddress == "" {
		http.Error(w, "Missing sender address", http.StatusBadRequest)
		return
	}

	// 获取验证器
	verifier := GetFrostMsgVerifier()

	// 1. 签名验证
	if verifier.EnableSigVerify {
		if len(envelope.Sig) == 0 {
			logs.Warn("[HandleFrostMsg] Missing signature from %s", senderAddress)
			http.Error(w, "Missing signature", http.StatusUnauthorized)
			return
		}

		if verifier.GetPublicKey == nil {
			logs.Warn("[HandleFrostMsg] No public key lookup function configured")
			http.Error(w, "Signature verification not configured", http.StatusInternalServerError)
			return
		}

		pubKey, err := verifier.GetPublicKey(senderAddress)
		if err != nil {
			logs.Warn("[HandleFrostMsg] Failed to get public key for %s: %v", senderAddress, err)
			http.Error(w, "Unknown sender", http.StatusUnauthorized)
			return
		}

		if !security.VerifyFrostEnvelope(pubKey, &envelope) {
			logs.Warn("[HandleFrostMsg] Invalid signature from %s", senderAddress)
			http.Error(w, "Invalid signature", http.StatusUnauthorized)
			return
		}
	}

	// 2. 防重放验证
	if verifier.EnableReplayGuard && verifier.ReplayGuard != nil {
		valid, isReplay := verifier.ReplayGuard.Check(senderAddress, envelope.Seq)
		if !valid {
			if isReplay {
				logs.Warn("[HandleFrostMsg] Replay attack detected from %s, seq=%d", senderAddress, envelope.Seq)
				http.Error(w, "Replay detected", http.StatusConflict)
			} else {
				logs.Warn("[HandleFrostMsg] Seq jump too large from %s, seq=%d", senderAddress, envelope.Seq)
				http.Error(w, "Invalid sequence number", http.StatusBadRequest)
			}
			return
		}
	}

	// 构造 types.Message 并入队
	msg := types.Message{
		Type:         types.MsgFrost,
		From:         types.NodeID(senderAddress),
		FrostPayload: bodyBytes,
	}

	// 如果有 FrostMessageHandler，入队处理
	if hm.frostMsgHandler != nil {
		if err := hm.frostMsgHandler(msg); err != nil {
			logs.Warn("[HandleFrostMsg] Failed to handle frost message: %v", err)
			http.Error(w, "Failed to process message", http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
}
