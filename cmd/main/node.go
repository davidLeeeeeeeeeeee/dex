package main

import (
	"context"
	"crypto/tls"
	"dex/config"
	"dex/consensus"
	"dex/db"
	frostrt "dex/frost/runtime"
	"dex/handlers"
	"dex/logs"
	"dex/middleware"
	"dex/sender"
	"dex/txpool"
	"dex/types"
	"dex/utils"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// 表示一个节点实例
type NodeInstance struct {
	ID               int
	PrivateKey       string
	Address          string
	Port             string
	DataPath         string
	Server           *http.Server
	ConsensusManager *consensus.ConsensusNodeManager
	DBManager        *db.Manager
	Cancel           context.CancelFunc
	TxPool           *txpool.TxPool
	SenderManager    *sender.SenderManager
	HandlerManager   *handlers.HandlerManager
	FrostRuntime     *frostrt.Manager // FROST 门限签名 Runtime（可选）
	Logger           logs.Logger
}

// 初始化单个节点
func initializeNode(node *NodeInstance, cfg *config.Config) error {
	// 1. 初始化密钥管理器
	keyMgr := utils.GetKeyManager()
	if err := keyMgr.InitKey(node.PrivateKey); err != nil {
		return fmt.Errorf("failed to init key: %v", err)
	}
	node.Address = keyMgr.GetAddress()

	// 2. 设置环境变量（某些模块可能需要）
	utils.Port = node.Port

	// 3. 初始化数据库
	dbManager, err := db.NewManager(node.DataPath, node.Logger)
	if err != nil {
		return fmt.Errorf("failed to init db: %v", err)
	}
	node.DBManager = dbManager

	// 初始化数据库写队列
	dbManager.InitWriteQueue(100, 200*time.Millisecond)

	// 4. 创建验证器
	validator := &TestValidator{}

	// 5. 创建并启动TxPool（不再使用单例）
	txPool, err := txpool.NewTxPool(dbManager, validator, node.Address, node.Logger)
	if err != nil {
		return fmt.Errorf("failed to create TxPool: %v", err)
	}
	if err := txPool.Start(); err != nil {
		return fmt.Errorf("failed to start TxPool: %v", err)
	}
	node.TxPool = txPool

	// 6. 创建发送管理器
	senderManager := sender.NewSenderManager(dbManager, node.Address, txPool, node.ID, node.Logger)
	node.SenderManager = senderManager

	// 7. 初始化共识系统
	consCfg := consensus.DefaultConfig()
	// 调整配置
	consCfg.Consensus.NumHeights = 10     // 运行10个高度
	consCfg.Consensus.BlocksPerHeight = 3 // 每个高度3个候选块
	consCfg.Consensus.K = 15              // 采样 75% 节点
	consCfg.Consensus.Alpha = 12          // 需要 80% 的 K 同意
	consCfg.Consensus.Beta = 10           // 更多轮次确认
	consCfg.Node.ProposalInterval = 5 * time.Second

	// 网络模拟配置
	packetLossRate := cfg.Network.PacketLossRate
	minLatency := cfg.Network.MinLatency
	maxLatency := cfg.Network.MaxLatency

	consensusManager := consensus.InitConsensusManagerWithSimulation(
		types.NodeID(strconv.Itoa(node.ID)),
		dbManager,
		consCfg,
		senderManager,
		txPool,
		node.Logger,
		packetLossRate,
		minLatency,
		maxLatency,
		cfg,
	)
	node.ConsensusManager = consensusManager

	// 8. 创建Handler管理器
	handlerManager := handlers.NewHandlerManager(
		dbManager,
		consensusManager,
		node.Port,
		node.Address,
		senderManager,
		txPool,
		node.Logger,
	)
	node.HandlerManager = handlerManager

	return nil
}

// 新增：带信号的HTTP服务器启动函数
func startHTTPServerWithSignal(node *NodeInstance, readyChan chan<- int, errorChan chan<- error) error {
	// 创建HTTP路由
	mux := http.NewServeMux()

	// 将该 Logger 绑定到当前主线程(协程)
	logs.SetThreadLogger(node.Logger)

	// 记录当前 Goroutine 的节点上下文
	logs.SetThreadNodeContext(node.Address)

	// 使用HandlerManager注册路由
	node.HandlerManager.RegisterRoutes(mux)

	// 应用中间件
	handler := middleware.RateLimit(mux)

	// 生成自签名证书
	certFile := fmt.Sprintf("server_%d.crt", node.ID)
	keyFile := fmt.Sprintf("server_%d.key", node.ID)

	if err := generateSelfSignedCert(certFile, keyFile); err != nil {
		errorChan <- fmt.Errorf("Node %d: Failed to generate certificate: %v", node.ID, err)
		return err
	}

	// 创建TLS配置
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{},
		MinVersion:   tls.VersionTLS13,
		MaxVersion:   tls.VersionTLS13,
		// 添加ALPN协议支持 - 这是关键修复
		NextProtos: []string{"h3", "h3-29", "h3-28", "h3-27"}, // HTTP/3协议标识符
	}

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		errorChan <- fmt.Errorf("Node %d: Failed to load certificate: %v", node.ID, err)
		return err
	}
	tlsConfig.Certificates = append(tlsConfig.Certificates, cert)

	// 创建QUIC配置
	quicConfig := &quic.Config{
		KeepAlivePeriod: 10 * time.Second,
		MaxIdleTimeout:  5 * time.Minute,
		Allow0RTT:       true,
	}

	// 创建HTTP/3服务器
	server := &http3.Server{
		Addr:       ":" + node.Port,
		Handler:    handler,
		TLSConfig:  tlsConfig,
		QUICConfig: quicConfig,
	}

	node.Server = &http.Server{
		Addr:    ":" + node.Port,
		Handler: handler,
	}

	// 创建QUIC监听器
	listener, err := quic.ListenAddr(":"+node.Port, tlsConfig, quicConfig)
	if err != nil {
		errorChan <- fmt.Errorf("Node %d: Failed to create QUIC listener: %v", node.ID, err)
		return err
	}

	logs.Info("Node %d: Starting HTTP/3 server on port %s", node.ID, node.Port)

	// 服务器成功创建监听器，发送就绪信号
	readyChan <- node.ID

	// 启动服务器（这是阻塞调用）
	if err := server.ServeListener(listener); err != nil {
		logs.Error("Node %d: HTTP/3 Server error: %v", node.ID, err)
		return err
	}

	return nil
}

// 关闭所有节点
func shutdownAllNodes(nodes []*NodeInstance) {
	for _, node := range nodes {
		if node == nil {
			continue
		}
		node.Logger.Info("Stopping node %d...", node.ID)

		// 1. 停止 FROST Runtime
		if node.FrostRuntime != nil {
			node.FrostRuntime.Stop()
		}

		// 2. 停止共识引擎
		if node.ConsensusManager != nil {
			node.ConsensusManager.Stop()
		}

		// 3. 停止 TxPool
		if node.TxPool != nil {
			node.TxPool.Stop()
		}

		// 4. 关闭 HTTP 服务器
		if node.Server != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			node.Server.Shutdown(ctx)
			cancel()
		}

		// 5. 停止发送管理器
		if node.SenderManager != nil {
			node.SenderManager.Stop()
		}

		// 6. 最后关闭数据库
		if node.DBManager != nil {
			node.DBManager.Close()
		}

		node.Logger.Info("Node %d stopped.", node.ID)
	}
}
