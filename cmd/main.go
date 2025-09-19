package main

import (
	"context"
	"dex/consensus"
	"dex/db"
	"dex/handlers"
	"dex/logs"
	"dex/middleware"
	"dex/sender"
	"dex/txpool"
	"dex/types"
	"dex/utils"
	"fmt"
	"net/http"
	"os"
	"time"
)

// InitConsensusSystem 初始化共识系统
func InitConsensusSystem(privateKey string) error {
	// 1. 初始化密钥管理器
	keyMgr := utils.GetKeyManager()
	if err := keyMgr.InitKey(privateKey); err != nil {
		return fmt.Errorf("failed to init key: %v", err)
	}

	address := keyMgr.GetAddress()
	logs.MyAddress = address
	logs.Info("Node initialized with address: %s", address)

	// 2. 初始化数据库
	dbManager, err := db.NewManager("./data")
	if err != nil {
		return fmt.Errorf("failed to init db: %v", err)
	}

	// 初始化数据库写队列
	dbManager.InitWriteQueue(100, 200*time.Millisecond)

	// 3. 初始化TxPool队列
	validator := &SimpleValidator{} // 需要实现一个简单的验证器
	txpool.InitTxPoolQueue(dbManager, validator)

	// 4. 初始化发送队列
	sender.InitQueue(10, 10000)

	// 5. 初始化共识系统
	nodeID := getNodeIDFromAddress(address)
	config := consensus.DefaultConfig()

	// 调整配置参数
	config.Network.NumNodes = 100
	config.Network.NumByzantineNodes = 0 // 暂时设为0，生产环境需要处理
	config.Consensus.NumHeights = 1000   // 运行1000个高度
	config.Consensus.BlocksPerHeight = 3 // 每个高度最多3个区块候选

	// 初始化共识管理器
	consensusManager := consensus.InitConsensusManager(nodeID, dbManager, config)

	// 设置全局引用，供handlers使用
	handlers.ConsensusManager = consensusManager

	logs.Info("Consensus system initialized successfully")
	return nil
}

// RegisterConsensusRoutes 注册共识相关的HTTP路由
func RegisterConsensusRoutes(mux *http.ServeMux) {
	// Snowman共识消息处理
	mux.HandleFunc("/pushquery", handlers.HandlePushQuery)
	mux.HandleFunc("/pullquery", handlers.HandlePullQuery)
	mux.HandleFunc("/pullcontainer", handlers.HandlePullContainer)

	// 原有的路由
	mux.HandleFunc("/status", handlers.HandleStatus)
	mux.HandleFunc("/tx", handlers.HandleTx)
	mux.HandleFunc("/getblock", handlers.HandleGetBlock)
	mux.HandleFunc("/getdata", handlers.HandleGetData)
	mux.HandleFunc("/batchgetdata", handlers.HandleBatchGetTx)
	mux.HandleFunc("/nodes", handlers.HandleNodes)

	// Handshake路由
	mux.HandleFunc("/handshake_challenge", handlers.HandleGetChallenge)
	mux.HandleFunc("/handshake", handlers.HandleHandshake)

	// 共识状态查询（用于监控）
	mux.HandleFunc("/consensus/status", HandleConsensusStatus)
	mux.HandleFunc("/consensus/stats", HandleConsensusStats)

	logs.Info("All consensus routes registered")
}

// HandleConsensusStatus 处理共识状态查询
func HandleConsensusStatus(w http.ResponseWriter, r *http.Request) {
	manager := consensus.GetConsensusManager()
	if manager == nil {
		http.Error(w, "Consensus not initialized", http.StatusServiceUnavailable)
		return
	}

	lastAccepted, height := manager.GetLastAccepted()
	currentHeight := manager.GetCurrentHeight()
	isReady := manager.IsReady()

	status := map[string]interface{}{
		"ready":          isReady,
		"last_accepted":  lastAccepted,
		"last_height":    height,
		"current_height": currentHeight,
		"node_id":        getNodeIDFromAddress(utils.GetKeyManager().GetAddress()),
	}

	// 返回JSON响应
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{
		"ready": %t,
		"last_accepted": "%s",
		"last_height": %d,
		"current_height": %d,
		"node_id": %d
	}`, isReady, lastAccepted, height, currentHeight, status["node_id"])
}

// HandleConsensusStats 处理共识统计查询
func HandleConsensusStats(w http.ResponseWriter, r *http.Request) {
	manager := consensus.GetConsensusManager()
	if manager == nil {
		http.Error(w, "Consensus not initialized", http.StatusServiceUnavailable)
		return
	}

	stats := manager.GetStats()

	// 返回统计信息
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{
		"queries_sent": %d,
		"queries_received": %d,
		"chits_responded": %d,
		"blocks_proposed": %d,
		"gossips_received": %d
	}`,
		stats.QueriesSent,
		stats.QueriesReceived,
		stats.ChitsResponded,
		stats.BlocksProposed,
		stats.GossipsReceived)
}

// getNodeIDFromAddress 从地址生成NodeID
func getNodeIDFromAddress(address string) types.NodeID {
	// 简单的哈希映射，实际应该有更好的方案
	hash := utils.Sha256Hash([]byte(address))
	id := uint64(0)
	for i := 0; i < 8 && i < len(hash); i++ {
		id = (id << 8) | uint64(hash[i])
	}
	return types.NodeID(id % 10000) // 限制在合理范围
}

// SimpleValidator 简单的交易验证器
type SimpleValidator struct{}

func (v *SimpleValidator) CheckAnyTx(tx *db.AnyTx) error {
	// 基本验证逻辑
	if tx == nil {
		return fmt.Errorf("nil transaction")
	}

	base := tx.GetBase()
	if base == nil {
		return fmt.Errorf("missing base message")
	}

	if base.TxId == "" {
		return fmt.Errorf("empty tx id")
	}

	if base.FromAddress == "" {
		return fmt.Errorf("empty from address")
	}

	// TODO: 添加签名验证、余额检查等

	return nil
}

// 主函数示例
func main() {
	// 从环境变量或配置文件读取私钥
	privateKey := os.Getenv("NODE_PRIVATE_KEY")
	if privateKey == "" {
		logs.Error("NODE_PRIVATE_KEY not set")
		return
	}

	// 初始化共识系统
	if err := InitConsensusSystem(privateKey); err != nil {
		logs.Error("Failed to init consensus system: %v", err)
		return
	}

	// 创建HTTP服务器
	mux := http.NewServeMux()

	// 注册路由
	RegisterConsensusRoutes(mux)

	// 应用中间件
	handler := middleware.RateLimit(mux)

	// 启动IP清理
	go middleware.StartIPCleanup()

	// 获取端口
	port := os.Getenv("NODE_PORT")
	if port == "" {
		port = "6123"
	}
	utils.Port = port

	// 启动HTTPS服务器
	server := &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	logs.Info("Starting consensus node on port %s", port)

	// 生成或加载TLS证书
	certFile := "server.crt"
	keyFile := "server.key"

	if _, err := os.Stat(certFile); os.IsNotExist(err) {
		// 生成自签名证书
		if err := generateSelfSignedCert(certFile, keyFile); err != nil {
			logs.Error("Failed to generate certificate: %v", err)
			return
		}
	}

	// 启动HTTPS服务
	if err := server.ListenAndServeTLS(certFile, keyFile); err != nil {
		logs.Error("Server failed: %v", err)
	}
}

// generateSelfSignedCert 生成自签名证书
func generateSelfSignedCert(certFile, keyFile string) error {
	// 这里应该调用crt包的证书生成函数
	// 简化处理，实际实现请参考crt包
	logs.Info("Generated self-signed certificate")
	return nil
}

// 优雅关闭
func gracefulShutdown(server *http.Server) {
	// 设置超时上下文
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 停止共识系统
	if manager := consensus.GetConsensusManager(); manager != nil {
		manager.Stop()
	}

	// 停止TxPool队列
	if txpool.GlobalTxPoolQueue != nil {
		txpool.GlobalTxPoolQueue.Stop()
	}

	// 停止发送队列
	if sender.GlobalQueue != nil {
		sender.GlobalQueue.Stop()
	}

	// 关闭HTTP服务器
	if err := server.Shutdown(ctx); err != nil {
		logs.Error("Server shutdown error: %v", err)
	}

	// 关闭数据库
	if mgr, err := db.NewManager(""); err == nil {
		mgr.Close()
	}

	logs.Info("Server gracefully stopped")
}
