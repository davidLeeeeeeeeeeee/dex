package handlers

import (
	"dex/consensus"
	"dex/db"
	"dex/logs"
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
	Logger          logs.Logger
}

// NewHandlerManager 创建新的处理器管理器
func NewHandlerManager(
	dbMgr *db.Manager,
	consensusMgr *consensus.ConsensusNodeManager,
	port, address string,
	senderMgr *sender.SenderManager,
	txPool *txpool.TxPool, // 只注入TxPool
	logger logs.Logger,
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
		Logger:           logger,
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
	mux.HandleFunc("/statedb/snapshot/shards", hm.HandleStateSnapshotShards)
	mux.HandleFunc("/statedb/snapshot/page", hm.HandleStateSnapshotPage)
	mux.HandleFunc("/pendingblocks", hm.HandlePendingBlocks)
	// 基本功能
	mux.HandleFunc("/status", hm.HandleStatus)
	mux.HandleFunc("/tx", hm.HandleTx)
	mux.HandleFunc("/getblock", hm.HandleGetBlock)
	mux.HandleFunc("/getsyncblocks", hm.HandleGetSyncBlocks)
	mux.HandleFunc("/getchits", hm.HandleGetChits) // 获取区块最终化投票信息
	mux.HandleFunc("/getrecentblocks", hm.HandleGetRecentBlocks)
	mux.HandleFunc("/getdata", hm.HandleGetData)
	mux.HandleFunc("/gettxreceipt", hm.HandleGetTxReceipt)
	mux.HandleFunc("/batchgetdata", hm.HandleBatchGetTx)
	mux.HandleFunc("/getaccount", hm.HandleGetAccount)
	mux.HandleFunc("/getaccountbalances", hm.HandleGetAccountBalances)
	mux.HandleFunc("/nodes", hm.HandleNodes)
	mux.HandleFunc("/getblockbyid", hm.HandleGet)
	mux.HandleFunc("/put", hm.HandlePut)
	mux.HandleFunc("/logs", hm.HandleLogs)
	// Frost P2P
	mux.HandleFunc("/frostmsg", hm.HandleFrostMsg)
	// Frost 只读查询 API
	mux.HandleFunc("/frost/config", hm.HandleGetFrostConfig)
	mux.HandleFunc("/frost/withdraw/status", hm.HandleGetWithdrawStatus)
	mux.HandleFunc("/frost/withdraws", hm.HandleListWithdraws)
	mux.HandleFunc("/frost/vault/group_pubkey", hm.HandleGetVaultGroupPubKey)
	mux.HandleFunc("/frost/vault/transition_status", hm.HandleGetVaultTransitionStatus)
	mux.HandleFunc("/frost/vault/dkg_commitments", hm.HandleGetVaultDkgCommitments)
	mux.HandleFunc("/frost/signed_package", hm.HandleDownloadSignedPackage)
	// Frost 查询接口（供 Explorer 使用）
	mux.HandleFunc("/frost/withdraw/list", hm.HandleFrostWithdrawList)
	mux.HandleFunc("/frost/dkg/list", hm.HandleFrostDkgList)
	mux.HandleFunc("/witness/requests", hm.HandleWitnessRequests)
	mux.HandleFunc("/witness/list", hm.HandleWitnessList)
	// 订单簿和成交记录
	mux.HandleFunc("/orderbook", hm.HandleOrderBook)
	mux.HandleFunc("/orderbook/debug", hm.HandleOrderBookDebug)
	mux.HandleFunc("/trades", hm.HandleTrades)
	// Token 查询
	mux.HandleFunc("/gettoken", hm.HandleGetToken)
	mux.HandleFunc("/gettokenregistry", hm.HandleGetTokenRegistry)
	// Frost 管理接口
	mux.HandleFunc("/frost/health", hm.HandleGetHealth)
	mux.HandleFunc("/frost/metrics", hm.HandleGetMetrics)
	mux.HandleFunc("/frost/rescan", hm.HandleForceRescan)
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
