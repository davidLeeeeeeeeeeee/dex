package handlers

import (
	"dex/pb"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// 处理节点列表请求
func (hm *HandlerManager) HandleNodes(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleNodes")
	if !hm.checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	nodes, err := hm.dbManager.GetAllNodeInfos()
	if err != nil {
		http.Error(w, "Failed to get nodes", http.StatusInternalServerError)
		return
	}

	nodeList := &pb.NodeList{
		Nodes: nodes,
	}
	data, _ := proto.Marshal(nodeList)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(data)
}
