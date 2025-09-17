package handlers

import (
	"net/http"
	"strings"

	"dex/db"
	"dex/network"
	"google.golang.org/protobuf/proto"
)

func HandleNodes(w http.ResponseWriter, r *http.Request) { // 发送所有的节点信息给请求者
	if !CheckAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	clientIP := strings.Split(r.RemoteAddr, ":")[0]
	dbMgr, err := db.NewManager("")
	if err != nil {
		http.Error(w, "DB not available", http.StatusInternalServerError)
		return
	}
	clientIP = strings.Split(r.RemoteAddr, ":")[0]
	info, err := db.GetClientInfo(dbMgr, clientIP)
	if err != nil || !info.GetAuthed() {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	net := network.NewNetwork(dbMgr)

	nodes := net.GetAllNodes()

	// 设置 Content-Type 为 protobuf 类型
	w.Header().Set("Content-Type", "application/x-protobuf")

	nodeList := &db.NodeList{
		Nodes: nodes,
	}

	data, err := proto.Marshal(nodeList)
	if err != nil {
		http.Error(w, "Failed to marshal protobuf", http.StatusInternalServerError)
		return
	}

	w.Write(data)
}
