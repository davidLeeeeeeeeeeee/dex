package handlers

import (
	"dex/consensus" // 假设你已经有共识实例
	"dex/db"
	"dex/logs"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
)

/* -------------------------------------------------------------------------- */
/* ---------------------------  /pushquery  --------------------------------- */
/* -------------------------------------------------------------------------- */

func HandlePushQuery(w http.ResponseWriter, r *http.Request) {
	logs.Info("[HandlePushQuery] received request from %s", r.RemoteAddr)
	if !CheckAuth(r) { // 与其他 handler 一致的鉴权风格:contentReference[oaicite:2]{index=2}:contentReference[oaicite:3]{index=3}
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body err", http.StatusBadRequest)
		return
	}
	var pq db.PushQuery
	if err := proto.Unmarshal(body, &pq); err != nil {
		http.Error(w, "bad proto", http.StatusBadRequest)
		return
	}

	// 把消息交给共识层（此处示例）
	mgr, err := db.NewManager("")
	if err != nil {
		http.Error(w, "Database not available", http.StatusInternalServerError)
		return
	}
	cons := consensus.NewDefaultConsensus(mgr)
	chits := cons.OnPushQuery(&pq) // 返回 *db.Chits

	// 直接把 Chits 回给请求方
	respBytes, _ := proto.Marshal(chits)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(respBytes)
}

/* -------------------------------------------------------------------------- */
/* ---------------------------  /pullquery  --------------------------------- */
/* -------------------------------------------------------------------------- */

func HandlePullQuery(w http.ResponseWriter, r *http.Request) {
	logs.Info("[HandlePullQuery] ")
	if !CheckAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body err", http.StatusBadRequest)
		return
	}
	var pq db.PullQuery
	if err := proto.Unmarshal(body, &pq); err != nil {
		http.Error(w, "bad proto", http.StatusBadRequest)
		return
	}

	mgr, err := db.NewManager("")
	if err != nil {
		http.Error(w, "Database not available", http.StatusInternalServerError)
		return
	}
	cons := consensus.NewDefaultConsensus(mgr)
	chits := cons.OnPullQuery(&pq)

	respBytes, _ := proto.Marshal(chits)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(respBytes)
}

/* -------------------------------------------------------------------------- */
/* -------------------------  /pullcontainer  ------------------------------- */
/* -------------------------------------------------------------------------- */

func HandlePullContainer(w http.ResponseWriter, r *http.Request) {
	if !CheckAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body err", http.StatusBadRequest)
		return
	}

	var pq db.PullQuery
	if err := proto.Unmarshal(body, &pq); err != nil {
		http.Error(w, "bad proto", http.StatusBadRequest)
		return
	}

	// 从数据库或内存中查找对应的区块
	mgr, err := db.NewManager("")
	if err != nil {
		http.Error(w, "Database not available", http.StatusInternalServerError)
		return
	}

	cons := consensus.NewDefaultConsensus(mgr)

	// 尝试从共识层获取完整区块数据
	pushQuery := cons.GetBlockContainer(pq.BlockId, pq.RequestedHeight)
	if pushQuery == nil {
		http.Error(w, "Block not found", http.StatusNotFound)
		return
	}

	respBytes, _ := proto.Marshal(pushQuery)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(respBytes)
}
