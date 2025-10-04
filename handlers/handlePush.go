package handlers

import (
	"dex/consensus"
	"dex/db"
	"dex/logs"
	"dex/types"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// 处理PushQuery请求（Snowman共识）
func (hm *HandlerManager) HandlePushQuery(w http.ResponseWriter, r *http.Request) {
	hm.recordAPICall("HandlePushQuery")
	bodyBytes, _ := io.ReadAll(r.Body)

	var pushQuery db.PushQuery
	proto.Unmarshal(bodyBytes, &pushQuery)
	// 使用 pushQuery.Address 而不是从 IP 推断
	senderAddress := pushQuery.Address
	// 验证发送方身份（通过签名或其他机制）
	if !hm.verifyNodeIdentity(senderAddress, pushQuery) {
		http.Error(w, "Invalid node identity", http.StatusUnauthorized)
		return
	}
	// 1、检查内存txpool是否存在
	// 2、校验合法性
	// 3、使用 adapter 转换
	msg, err := hm.adapter.PushQueryToConsensusMessage(&pushQuery, types.NodeID(senderAddress))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 4、丢进共识，处理消息
	// 如果transport是RealTransport，使用队列方式
	if rt, ok := hm.consensusManager.Transport.(*consensus.RealTransport); ok {
		if err := rt.EnqueueReceivedMessage(msg); err != nil {
			logs.Warn("[Handler] Failed to enqueue message: %v", err)
			// 返回 503 Service Unavailable，促使发送方退避重试
			http.Error(w, "Service temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
	}

	// 5、生成响应直接返回 200 OK，不返回 chits
	w.WriteHeader(http.StatusOK)
}
