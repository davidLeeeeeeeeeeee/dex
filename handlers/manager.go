package handlers

import (
	"dex/config"
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

	// API 速率限制器
	rateLimiter *RateLimiter
}

// NewHandlerManager 创建新的处理器管理器
func NewHandlerManager(
	dbMgr *db.Manager,
	consensusMgr *consensus.ConsensusNodeManager,
	port, address string,
	senderMgr *sender.SenderManager,
	txPool *txpool.TxPool, // 只注入TxPool
	logger logs.Logger,
	cfg *config.Config,
) *HandlerManager {
	// 创建 LRU 缓存，容量设为 10000
	seenBlocksCache, _ := lru.New(100)
	var rateLimits map[string]float64
	if cfg != nil {
		rateLimits = cfg.Server.RateLimits
	}
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
		rateLimiter:      NewRateLimiter(rateLimits),
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
	// 辅助绑定函数，自带速率限制
	bind := func(path string, handler http.HandlerFunc) {
		mux.HandleFunc(path, hm.rateLimiter.LimitMiddleware(path, handler, hm.Logger))
	}

	// Snowman共识相关
	bind("/pushquery", hm.HandlePushQuery)
	bind("/pullquery", hm.HandlePullQuery)
	bind("/gossipAnyMsg", hm.HandleBlockGossip)
	bind("/chits", hm.HandleChits)
	bind("/heightquery", hm.HandleHeightQuery)
	bind("/statedb/snapshot/shards", hm.HandleStateSnapshotShards)
	bind("/statedb/snapshot/page", hm.HandleStateSnapshotPage)
	bind("/pendingblocks", hm.HandlePendingBlocks)
	// 基本功能
	bind("/status", hm.HandleStatus)
	bind("/tx", hm.HandleTx)
	bind("/getblock", hm.HandleGetBlock)
	bind("/getsyncblocks", hm.HandleGetSyncBlocks)
	bind("/getchits", hm.HandleGetChits) // 获取区块最终化投票信息
	bind("/getrecentblocks", hm.HandleGetRecentBlocks)
	bind("/getdata", hm.HandleGetData)
	bind("/gettxreceipt", hm.HandleGetTxReceipt)
	bind("/batchgetdata", hm.HandleBatchGetTx)
	bind("/getaccount", hm.HandleGetAccount)
	bind("/getaccountbalances", hm.HandleGetAccountBalances)
	bind("/nodes", hm.HandleNodes)
	bind("/getblockbyid", hm.HandleGet)
	bind("/put", hm.HandlePut)
	bind("/logs", hm.HandleLogs)
	// Frost P2P
	bind("/frostmsg", hm.HandleFrostMsg)
	// Frost 只读查询 API
	bind("/frost/config", hm.HandleGetFrostConfig)
	bind("/frost/withdraw/status", hm.HandleGetWithdrawStatus)
	bind("/frost/withdraws", hm.HandleListWithdraws)
	bind("/frost/vault/group_pubkey", hm.HandleGetVaultGroupPubKey)
	bind("/frost/vault/transition_status", hm.HandleGetVaultTransitionStatus)
	bind("/frost/vault/dkg_commitments", hm.HandleGetVaultDkgCommitments)
	bind("/frost/signed_package", hm.HandleDownloadSignedPackage)
	// Frost 查询接口（供 Explorer 使用）
	bind("/frost/withdraw/list", hm.HandleFrostWithdrawList)
	bind("/frost/dkg/list", hm.HandleFrostDkgList)
	bind("/witness/requests", hm.HandleWitnessRequests)
	bind("/witness/list", hm.HandleWitnessList)
	bind("/witness/challenges", hm.HandleWitnessChallenges)
	// 订单簿和成交记录
	bind("/orderbook", hm.HandleOrderBook)
	bind("/orderbook/debug", hm.HandleOrderBookDebug)
	bind("/trades", hm.HandleTrades)
	// Token 查询
	bind("/gettoken", hm.HandleGetToken)
	bind("/gettokenregistry", hm.HandleGetTokenRegistry)
	// Frost 管理接口
	bind("/frost/health", hm.HandleGetHealth)
	bind("/frost/metrics", hm.HandleGetMetrics)
	bind("/frost/rescan", hm.HandleForceRescan)
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
