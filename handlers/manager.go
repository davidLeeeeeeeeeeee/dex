package handlers

import (
	"compress/gzip"
	"dex/consensus"
	"dex/db"
	"dex/logs"
	"dex/sender"
	"dex/txpool"
	"dex/types"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru"
	"google.golang.org/protobuf/proto"
)

// HandlerManager 管理所有HTTP处理器及其依赖
type HandlerManager struct {
	dbManager        *db.Manager
	consensusManager *consensus.ConsensusNodeManager
	senderManager    *sender.SenderManager
	txPool           *txpool.TxPool
	port             string // 当前节点端口
	address          string // 当前节点地址
	adapter          *consensus.ConsensusAdapter

	// 添加已知块缓存
	seenBlocksCache *lru.Cache // 用于记录已处理的区块ID
	// 统计相关字段
	statsLock     sync.RWMutex
	apiCallCounts map[string]uint64
}

// NewHandlerManager 创建新的处理器管理器
func NewHandlerManager(
	dbMgr *db.Manager,
	consensusMgr *consensus.ConsensusNodeManager,
	port, address string,
	senderMgr *sender.SenderManager,
	txPool *txpool.TxPool, // 只注入TxPool
) *HandlerManager {
	// 创建 LRU 缓存，容量设为 10000
	seenBlocksCache, _ := lru.New(100)
	return &HandlerManager{
		dbManager:        dbMgr,
		consensusManager: consensusMgr,
		txPool:           txPool,
		senderManager:    senderMgr,
		port:             port,
		address:          address,
		adapter:          consensus.NewConsensusAdapter(dbMgr),
		apiCallCounts:    make(map[string]uint64),
		seenBlocksCache:  seenBlocksCache,
	}
}

type ChallengeInfo struct {
	Challenge   string
	CreatedTime time.Time
}

// RegisterRoutes 注册所有路由
func (hm *HandlerManager) RegisterRoutes(mux *http.ServeMux) {
	// Snowman共识相关
	mux.HandleFunc("/pushquery", hm.HandlePushQuery)
	mux.HandleFunc("/pullquery", hm.HandlePullQuery)
	mux.HandleFunc("/gossipAnyMsg", hm.HandleBlockGossip)
	mux.HandleFunc("/chits", hm.HandleChits)
	mux.HandleFunc("/heightquery", hm.HandleHeightQuery)
	// 基本功能
	mux.HandleFunc("/status", hm.HandleStatus)
	mux.HandleFunc("/tx", hm.HandleTx)
	mux.HandleFunc("/getblock", hm.HandleGetBlock)
	mux.HandleFunc("/getdata", hm.HandleGetData)
	mux.HandleFunc("/batchgetdata", hm.HandleBatchGetTx)
	mux.HandleFunc("/nodes", hm.HandleNodes)
	mux.HandleFunc("/getblockbyid", hm.HandleGetBlockByID)
	mux.HandleFunc("/put", hm.HandlePut)

}

func (hm *HandlerManager) HandleChits(w http.ResponseWriter, r *http.Request) {
	hm.recordAPICall("HandleChits")
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	var chits db.Chits
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

func (hm *HandlerManager) HandleHeightQuery(w http.ResponseWriter, r *http.Request) {
	hm.recordAPICall("HandleHeightQuery")
	// 返回当前节点的高度信息
	_, height := hm.consensusManager.GetLastAccepted()
	currentHeight := hm.consensusManager.GetCurrentHeight()

	resp := &db.HeightResponse{ // 需要在proto中定义
		LastAcceptedHeight: height,
		CurrentHeight:      currentHeight,
	}

	data, _ := proto.Marshal(resp)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(data)
}

// 决定是否应该继续传播这个区块
func (hm *HandlerManager) shouldRebroadcast(block *types.Block) bool {

	// 实现简单的重播保护逻辑
	// 例如：检查区块是否太旧，或者是否已经广播过

	// 获取当前高度
	_, currentHeight := hm.consensusManager.GetLastAccepted()

	// 如果区块高度太旧（比当前高度低超过10个区块），不再传播
	if block.Height < currentHeight-10 {
		return false
	}

	// 如果区块高度太新（比当前高度高超过100个区块），可能是恶意的，不传播
	if block.Height > currentHeight+100 {
		return false
	}

	return true
}

// 处理状态查询
func (hm *HandlerManager) HandleStatus(w http.ResponseWriter, r *http.Request) {
	hm.recordAPICall("HandleStatus")
	if !hm.checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	resProto := &db.StatusResponse{
		Status: "ok",
		Info:   fmt.Sprintf("Server is running on port %s", hm.port),
	}
	data, err := proto.Marshal(resProto)
	if err != nil {
		http.Error(w, "Failed to marshal status response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// 处理获取区块请求
func (hm *HandlerManager) HandleGetBlock(w http.ResponseWriter, r *http.Request) {
	hm.recordAPICall("HandleGetBlock")
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	var req db.GetBlockRequest
	if err := proto.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, "Invalid GetBlockRequest proto", http.StatusBadRequest)
		return
	}

	// 使用注入的dbManager而不是创建新实例
	block, err := hm.dbManager.GetBlock(req.Height)
	if err != nil || block == nil {
		resp := &db.GetBlockResponse{
			Error: fmt.Sprintf("Block not found at height %d", req.Height),
		}
		respBytes, _ := proto.Marshal(resp)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusNotFound)
		w.Write(respBytes)
		return
	}

	resp := &db.GetBlockResponse{
		Block: block,
	}
	respBytes, err := proto.Marshal(resp)
	if err != nil {
		http.Error(w, "Failed to marshal GetBlockResponse", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)

	gz := gzip.NewWriter(w)
	defer gz.Close()
	gz.Write(respBytes)
}

// 处理节点列表请求
func (hm *HandlerManager) HandleNodes(w http.ResponseWriter, r *http.Request) {
	hm.recordAPICall("HandleNodes")
	if !hm.checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	nodes, err := hm.dbManager.GetAllNodeInfos()
	if err != nil {
		http.Error(w, "Failed to get nodes", http.StatusInternalServerError)
		return
	}

	nodeList := &db.NodeList{
		Nodes: nodes,
	}
	data, _ := proto.Marshal(nodeList)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(data)
}

// 添加身份验证方法
func (hm *HandlerManager) verifyNodeIdentity(address string, message interface{}) bool {
	// 从数据库获取该地址的公钥
	//account, err := db.GetAccount(hm.dbManager, address)
	//if err != nil || account == nil {
	//	return false
	//}

	// TODO: 验证消息签名
	// 这里需要每个消息都包含签名字段
	return true
}

// 辅助方法

func (hm *HandlerManager) checkAuth(r *http.Request) bool {
	if !AUTH_ENABLED {
		return true
	}

	clientIP := strings.Split(r.RemoteAddr, ":")[0]
	info, err := hm.dbManager.GetClientInfo(clientIP)
	if err != nil {
		return false
	}
	return info.GetAuthed()
}

func (hm *HandlerManager) hasBlock(blockId string) bool {
	if hm.consensusManager != nil {
		return hm.consensusManager.HasBlock(blockId)
	}
	return hm.dbManager.BlockExists(blockId)
}

func (hm *HandlerManager) Stop() {
	if hm.senderManager != nil {
		hm.senderManager.Stop()
	}
	// 其他清理工作
}

// ============= 统计相关方法 =============

// 记录API调用
func (h *HandlerManager) recordAPICall(apiName string) {
	h.statsLock.Lock()
	defer h.statsLock.Unlock()

	if h.apiCallCounts == nil {
		h.apiCallCounts = make(map[string]uint64)
	}
	h.apiCallCounts[apiName]++
}

// 获取API调用统计
func (h *HandlerManager) GetAPICallStats() map[string]uint64 {
	h.statsLock.RLock()
	defer h.statsLock.RUnlock()

	// 复制统计数据
	stats := make(map[string]uint64)
	for api, count := range h.apiCallCounts {
		stats[api] = count
	}
	return stats
}

// GetAPICallCount 获取特定API的调用次数
func (h *HandlerManager) GetAPICallCount(apiName string) uint64 {
	h.statsLock.RLock()
	defer h.statsLock.RUnlock()
	return h.apiCallCounts[apiName]
}

// ResetAPIStats 重置统计数据
func (h *HandlerManager) ResetAPIStats() {
	h.statsLock.Lock()
	defer h.statsLock.Unlock()
	h.apiCallCounts = make(map[string]uint64)
}
