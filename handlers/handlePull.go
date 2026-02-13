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

// 处理PullQuery请求（Snowman共识）
func (hm *HandlerManager) HandlePullQuery(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandlePullQuery")
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	var pullQuery pb.PullQuery
	if err := proto.Unmarshal(bodyBytes, &pullQuery); err != nil {
		http.Error(w, "Invalid PullQuery proto", http.StatusBadRequest)
		return
	}
	// 过期查询直接忽略，返回 200 避免触发对端重试风暴。
	if pullQuery.GetDeadline() > 0 && time.Now().UnixNano() > int64(pullQuery.GetDeadline()) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 构造消息并尝试入队
	msg := types.Message{
		RequestID: pullQuery.RequestId,
		Type:      types.MsgPullQuery,
		From:      types.NodeID(pullQuery.Address),
		BlockID:   pullQuery.BlockId,
		Height:    pullQuery.RequestedHeight,
	}

	if rt, ok := hm.consensusManager.Transport.(*consensus.RealTransport); ok {
		if err := rt.EnqueueReceivedMessage(msg); err != nil {
			hm.Logger.Warn("[Handler] Failed to enqueue PullQuery: %v", err)
			w.Header().Set("Retry-After", "1")
			http.Error(w, "Service temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
}
