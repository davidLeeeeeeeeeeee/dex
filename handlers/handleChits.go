package handlers

import (
	"dex/consensus"
	"dex/logs"
	"dex/pb"
	"dex/types"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

func (hm *HandlerManager) HandleChits(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleChits")
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	var chits pb.Chits
	if err := proto.Unmarshal(bodyBytes, &chits); err != nil {
		http.Error(w, "Invalid Chits proto", http.StatusBadRequest)
		return
	}

	// 转换为共识消息并处理
	if hm.adapter != nil {
		// 使用 chits.Address 而不是从 RemoteAddr 推断
		senderAddress := chits.Address

		// 验证发送方
		if !hm.verifyNodeIdentity(senderAddress, &chits) {
			http.Error(w, "Invalid sender", http.StatusUnauthorized)
			return
		}

		from := types.NodeID(senderAddress)
		msg := hm.adapter.ChitsToConsensusMessage(&chits, from)

		// 如果是RealTransport，使用队列方式处理
		if rt, ok := hm.consensusManager.Transport.(*consensus.RealTransport); ok {
			if err := rt.EnqueueReceivedMessage(msg); err != nil {
				logs.Warn("[Handler] Failed to enqueue chits message: %v", err)
				// 控制面消息入队失败，返回 503
				http.Error(w, "Service temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
		}
	}

	w.WriteHeader(http.StatusOK)
}
