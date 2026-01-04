// handlers/handleFrostMsg.go
// Frost P2P 消息处理端点

package handlers

import (
	"io"
	"net/http"

	"dex/logs"
	"dex/pb"
	"dex/types"

	"google.golang.org/protobuf/proto"
)

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

	// 注意：此处暂不做签名校验（P30 会添加）
	// 后续需要验证 envelope.Sig 对消息的签名

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

