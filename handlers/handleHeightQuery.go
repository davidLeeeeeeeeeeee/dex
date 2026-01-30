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

	// 获取 txpool 中 pending 交易数量
	var pendingTxCount uint32
	if hm.txPool != nil {
		pendingTxCount = uint32(hm.txPool.PendingLen())
	}

	// 获取同步状态
	syncStatus := hm.consensusManager.GetSyncStatus()

	resp := &pb.HeightResponse{ // 需要在proto中定义
		LastAcceptedHeight: height,
		CurrentHeight:      currentHeight,
		PendingBlocksCount: uint32(pendingCount),
		PendingTxCount:     pendingTxCount,
		IsSyncing:          syncStatus.IsSyncing,
		SyncTargetHeight:   syncStatus.SyncTargetHeight,
		IsSnapshotSync:     syncStatus.IsSnapshotSync,
	}

	data, _ := proto.Marshal(resp)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(data)
}
