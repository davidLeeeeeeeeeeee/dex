package handlers

import (
	"compress/gzip"
	"dex/consensus"
	"dex/db"
	"dex/logs"
	"dex/sender"
	"dex/txpool"
	"dex/types"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

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
}

// NewHandlerManager 创建新的处理器管理器
func NewHandlerManager(
	dbMgr *db.Manager,
	consensusMgr *consensus.ConsensusNodeManager,
	port, address string,
	senderMgr *sender.SenderManager,
	txPool *txpool.TxPool, // 只注入TxPool
) *HandlerManager {
	return &HandlerManager{
		dbManager:        dbMgr,
		consensusManager: consensusMgr,
		txPool:           txPool,
		senderManager:    senderMgr,
		port:             port,
		address:          address,
		adapter:          consensus.NewConsensusAdapter(dbMgr),
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

}

func (hm *HandlerManager) HandleChits(w http.ResponseWriter, r *http.Request) {
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
func (hm *HandlerManager) HandleBlockGossip(w http.ResponseWriter, r *http.Request) {
	// 1. 读取请求体
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	// 2. 尝试解析为 types.Block（JSON格式）
	var block types.Block
	if err := json.Unmarshal(bodyBytes, &block); err != nil {
		// 如果不是JSON格式，可能是protobuf格式的db.Block
		var dbBlock db.Block
		if protoErr := proto.Unmarshal(bodyBytes, &dbBlock); protoErr == nil {
			// 转换db.Block为types.Block
			consensusBlock, convertErr := hm.adapter.DBBlockToConsensus(&dbBlock)
			if convertErr != nil {
				http.Error(w, "Failed to convert block", http.StatusBadRequest)
				return
			}
			block = *consensusBlock
		} else {
			http.Error(w, "Invalid block format", http.StatusBadRequest)
			return
		}
	}

	// 3. 验证区块基本信息
	if block.ID == "" || block.ID == "genesis" {
		http.Error(w, "Invalid block ID", http.StatusBadRequest)
		return
	}

	// 4. 构造共识层的Gossip消息
	gossipMsg := types.Message{
		Type:    types.MsgGossip,
		From:    types.NodeID(block.Proposer),
		Block:   &block,
		BlockID: block.ID,
		Height:  block.Height,
	}

	// 5. 如果有共识管理器，将消息传递给它处理
	if hm.consensusManager != nil {
		// 添加区块到共识存储
		dbBlock := hm.adapter.ConsensusBlockToDB(&block, nil)
		if err := hm.consensusManager.AddBlock(dbBlock); err != nil {
			logs.Debug("[HandleBlockGossip] Failed to add block to consensus: %v", err)
			// 不返回错误，因为区块可能已存在
		}

		// 如果使用RealTransport，通过消息队列处理
		if rt, ok := hm.consensusManager.Transport.(*consensus.RealTransport); ok {
			if err := rt.EnqueueReceivedMessage(gossipMsg); err != nil {
				logs.Warn("[HandleBlockGossip] Failed to enqueue gossip message: %v", err)
			}
		} else {
			// 直接处理消息
			hm.consensusManager.ProcessMessage(gossipMsg)
		}

		logs.Debug("[HandleBlockGossip] Received block %s at height %d Proposer %s",
			block.ID, block.Height, block.Proposer)
	}

	// 7. 如果区块包含交易，处理交易
	if dbBlock, ok := consensus.GetCachedBlock(block.ID); ok && len(dbBlock.Body) > 0 {
		// 将交易添加到交易池
		for _, tx := range dbBlock.Body {
			if err := hm.txPool.StoreAnyTx(tx); err != nil {
				logs.Debug("[HandleBlockGossip] Failed to store tx %s: %v",
					tx.GetTxId(), err)
			}
		}
	}

	// 8. 返回成功响应
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// shouldRebroadcast 决定是否应该继续传播这个区块
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

// HandleStatus 处理状态查询
func (hm *HandlerManager) HandleStatus(w http.ResponseWriter, r *http.Request) {
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

// HandleGetBlock 处理获取区块请求
func (hm *HandlerManager) HandleGetBlock(w http.ResponseWriter, r *http.Request) {
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

// HandleNodes 处理节点列表请求
func (hm *HandlerManager) HandleNodes(w http.ResponseWriter, r *http.Request) {
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

// HandlePushQuery 处理PushQuery请求（Snowman共识）
func (hm *HandlerManager) HandlePushQuery(w http.ResponseWriter, r *http.Request) {
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

// 处理PullQuery请求（Snowman共识）
func (hm *HandlerManager) HandlePullQuery(w http.ResponseWriter, r *http.Request) {
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
