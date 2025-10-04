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

// 处理PullQuery请求（Snowman共识）
func (hm *HandlerManager) HandlePullQuery(w http.ResponseWriter, r *http.Request) {
	hm.recordAPICall("HandlePullQuery")
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	var pullQuery db.PullQuery
	if err := proto.Unmarshal(bodyBytes, &pullQuery); err != nil {
		http.Error(w, "Invalid PullQuery proto", http.StatusBadRequest)
		return
	}

	// 构造消息并尝试入队
	msg := types.Message{
		Type:    types.MsgPullQuery,
		From:    types.NodeID(pullQuery.Address),
		BlockID: pullQuery.BlockId,
		Height:  pullQuery.RequestedHeight,
	}

	if rt, ok := hm.consensusManager.Transport.(*consensus.RealTransport); ok {
		if err := rt.EnqueueReceivedMessage(msg); err != nil {
			logs.Warn("[Handler] Failed to enqueue PullQuery: %v", err)
			http.Error(w, "Service temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
}
