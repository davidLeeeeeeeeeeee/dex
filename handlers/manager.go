package handlers

import (
	"dex/consensus"
	"dex/db"
	"dex/sender"
	"dex/stats"
	"dex/txpool"
	"dex/types"
	"net/http"
	"strings"
	"time"

	lru "github.com/hashicorp/golang-lru"
)

// FrostMsgHandler Frost 消息处理回调
type FrostMsgHandler func(msg types.Message) error

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
	Stats *stats.Stats
	// Frost 消息处理器
	frostMsgHandler FrostMsgHandler
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
		Stats:            stats.NewStats(),
		seenBlocksCache:  seenBlocksCache,
	}
}

type ChallengeInfo struct {
	Challenge   string
	CreatedTime time.Time
}

// SetFrostMsgHandler 设置 Frost 消息处理器
func (hm *HandlerManager) SetFrostMsgHandler(handler FrostMsgHandler) {
	hm.frostMsgHandler = handler
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
	mux.HandleFunc("/getblockbyid", hm.HandleGet)
	mux.HandleFunc("/put", hm.HandlePut)
	// Frost P2P
	mux.HandleFunc("/frostmsg", hm.HandleFrostMsg)
	// Frost 只读查询 API
	mux.HandleFunc("/frost/config", hm.HandleGetFrostConfig)
	mux.HandleFunc("/frost/withdraw/status", hm.HandleGetWithdrawStatus)
	mux.HandleFunc("/frost/withdraws", hm.HandleListWithdraws)
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
