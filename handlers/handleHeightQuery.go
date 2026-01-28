package handlers

import (
	"dex/pb"
	"net/http"

	"google.golang.org/protobuf/proto"
)

func (hm *HandlerManager) HandleHeightQuery(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleHeightQuery")
	// 返回当前节点的高度信息
	_, height := hm.consensusManager.GetLastAccepted()
	currentHeight := hm.consensusManager.GetCurrentHeight()
	pendingCount := hm.consensusManager.GetPendingBlocksCount()

	resp := &pb.HeightResponse{ // 需要在proto中定义
		LastAcceptedHeight: height,
		CurrentHeight:      currentHeight,
		PendingBlocksCount: uint32(pendingCount),
	}

	data, _ := proto.Marshal(resp)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(data)
}
