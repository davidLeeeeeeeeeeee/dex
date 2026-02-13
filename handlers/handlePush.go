package handlers

import (
	"dex/consensus"
	"dex/pb"
	"dex/types"
	"io"
	"net/http"
	"time"

	"google.golang.org/protobuf/proto"
)

// 处理PushQuery请求（Snowman共识）
func (hm *HandlerManager) HandlePushQuery(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandlePushQuery")
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	var pushQuery pb.PushQuery
	if err := proto.Unmarshal(bodyBytes, &pushQuery); err != nil {
		http.Error(w, "Invalid PushQuery proto", http.StatusBadRequest)
		return
	}
	// 过期查询直接忽略，返回 200 避免触发对端重试风暴。
	if pushQuery.GetDeadline() > 0 && time.Now().UnixNano() > int64(pushQuery.GetDeadline()) {
		w.WriteHeader(http.StatusOK)
		return
	}
	// 使用 pushQuery.Address 而不是从 IP 推断
	senderAddress := pushQuery.Address
	// 验证发送方身份（通过签名或其他机制）
	if !hm.verifyNodeIdentity(senderAddress, &pushQuery) {
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
			hm.Logger.Warn("[Handler] Failed to enqueue message: %v", err)
			// 返回 503 Service Unavailable，促使发送方退避重试
			w.Header().Set("Retry-After", "1")
			http.Error(w, "Service temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
	}

	// 5、生成响应直接返回 200 OK，不返回 chits
	w.WriteHeader(http.StatusOK)
}
