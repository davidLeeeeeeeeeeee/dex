package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"dex/config"
	"dex/consensus"
	"dex/db"
	"dex/handlers"
	"dex/logs"
	"dex/middleware"
	"dex/sender"
	"dex/txpool"
	"dex/types"
	"dex/utils"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
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
	HandlerManager   *handlers.HandlerManager // 新增
}

// TestValidator 简单的交易验证器
type TestValidator struct{}

func (v *TestValidator) CheckAnyTx(tx *db.AnyTx) error {
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
	return nil
}
func main() {
	// 加载配置
	cfg := config.DefaultConfig()
	// 配置参数
	numNodes := cfg.Network.DefaultNumNodes
	basePort := cfg.Network.BasePort

	fmt.Printf("🚀 Starting %d real consensus nodes...\n", numNodes)

	// 生成节点私钥
	privateKeys := generatePrivateKeys(numNodes)

	// 创建所有节点实例
	nodes := make([]*NodeInstance, numNodes)
	var wg sync.WaitGroup

	// 第一阶段：初始化所有节点（创建数据库和基础设施）
	fmt.Println("📦 Phase 1: Initializing all nodes...")
	for i := 0; i < numNodes; i++ {
		node := &NodeInstance{
			Address:    fmt.Sprintf("0x000%d", i),
			ID:         i,
			PrivateKey: privateKeys[i],
			Port:       fmt.Sprintf("%d", basePort+i),
			DataPath:   fmt.Sprintf("./data/data_node_%d", i),
		}

		// 清理旧数据
		os.RemoveAll(node.DataPath)

		// 初始化节点
		if err := initializeNode(node); err != nil {
			logs.Error("Failed to initialize node %d: %v", i, err)
			continue
		}

		nodes[i] = node
		fmt.Printf("  ✔ Node %d initialized (port %s)\n", i, node.Port)
	}

	// 等待一下让所有数据库完成初始化
	time.Sleep(2 * time.Second)

	// 第二阶段：注册所有节点到数据库（让节点互相知道）
	fmt.Println("🔗 Phase 2: Registering all nodes...")
	registerAllNodes(nodes)

	// 第三阶段：启动所有HTTP服务器
	fmt.Println("🌐 Phase 3: Starting HTTP servers...")

	// 创建一个channel来收集服务器启动完成的信号
	serverReadyChan := make(chan int, numNodes)
	serverErrorChan := make(chan error, numNodes)

	for _, node := range nodes {
		if node == nil {
			continue
		}
		wg.Add(1)
		go func(n *NodeInstance) {
			defer wg.Done()

			// 启动HTTP服务器并发送就绪信号
			if err := startHTTPServerWithSignal(n, serverReadyChan, serverErrorChan); err != nil {
				serverErrorChan <- fmt.Errorf("node %d failed to start: %v", n.ID, err)
			}
		}(node)

		// 稍微错开启动时间，避免资源争抢
		time.Sleep(50 * time.Millisecond)
	}

	// 等待所有HTTP服务器启动完成
	fmt.Println("⏳ Waiting for all HTTP/3 servers to be ready...")
	readyCount := 0
	successfulNodes := 0

	// 设置超时时间
	timeout := time.After(30 * time.Second)

	for readyCount < len(nodes) {
		select {
		case nodeID := <-serverReadyChan:
			successfulNodes++
			fmt.Printf("  ✅ Node %d HTTP/3 server is ready (%d/%d)\n",
				nodeID, successfulNodes, len(nodes))

		case err := <-serverErrorChan:
			fmt.Printf("  ❌ Error: %v\n", err)

		case <-timeout:
			fmt.Printf("  ⚠️ Timeout waiting for servers. %d/%d started successfully\n",
				successfulNodes, len(nodes))
			goto CONTINUE_WITH_CONSENSUS
		}

		readyCount++
	}

	fmt.Printf("✅ All %d HTTP/3 servers are ready!\n", successfulNodes)

CONTINUE_WITH_CONSENSUS:
	// 额外等待一小段时间确保服务器完全稳定
	time.Sleep(1 * time.Second)
	// 新增：第3.5阶段 - 启动共识引擎
	fmt.Println("🔧 Phase 3.5: Starting consensus engines...")
	for _, node := range nodes {
		if node != nil && node.ConsensusManager != nil {
			node.ConsensusManager.Start()
			fmt.Printf("  ✓ Node %d consensus engine started\n", node.ID)
		}
	}
	// Create initial transactions
	fmt.Println("📝 Creating initial transactions...")
	for _, node := range nodes {
		if node != nil && node.ConsensusManager != nil {
			generateTransactions(node)
		}
	}
	time.Sleep(2 * time.Second)

	// 第四阶段：启动共识
	fmt.Println("🎯 Phase 4: Starting consensus engines...")
	for _, node := range nodes {
		if node != nil && node.ConsensusManager != nil {
			// 触发初始查询
			go func(n *NodeInstance) {
				time.Sleep(time.Duration(n.ID*100) * time.Millisecond) // 错开启动
				n.ConsensusManager.StartQuery()
			}(node)
		}
	}

	// 监控进度
	go monitorProgress(nodes)

	// 等待信号退出
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	fmt.Println("\n✅ All nodes started! Press Ctrl+C to stop...")
	fmt.Println("📊 Monitoring consensus progress...")

	<-sigChan

	// 优雅关闭
	fmt.Println("\n🛑 Shutting down all nodes...")
	shutdownAllNodes(nodes)

	wg.Wait()
	fmt.Println("👋 All nodes stopped. Goodbye!")
}

// 新增：带信号的HTTP服务器启动函数
func startHTTPServerWithSignal(node *NodeInstance, readyChan chan<- int, errorChan chan<- error) error {
	// 创建HTTP路由
	mux := http.NewServeMux()

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

// generatePrivateKeys 生成指定数量的私钥
func generatePrivateKeys(count int) []string {
	keys := make([]string, count)
	for i := 0; i < count; i++ {
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			logs.Error("Failed to generate key %d: %v", i, err)
			continue
		}

		// 转换为hex格式
		privBytes := priv.D.Bytes()
		keys[i] = hex.EncodeToString(privBytes)
	}
	return keys
}

// 初始化单个节点
func initializeNode(node *NodeInstance) error {
	// 1. 初始化密钥管理器
	keyMgr := utils.GetKeyManager()
	if err := keyMgr.InitKey(node.PrivateKey); err != nil {
		return fmt.Errorf("failed to init key: %v", err)
	}
	node.Address = keyMgr.GetAddress()

	// 2. 设置环境变量（某些模块可能需要）
	utils.Port = node.Port

	// 3. 初始化数据库
	dbManager, err := db.NewManager(node.DataPath)
	if err != nil {
		return fmt.Errorf("failed to init db: %v", err)
	}
	node.DBManager = dbManager

	// 初始化数据库写队列
	dbManager.InitWriteQueue(100, 200*time.Millisecond)

	// 4. 创建验证器
	validator := &TestValidator{}

	// 5. 创建并启动TxPool（不再使用单例）
	txPool, err := txpool.NewTxPool(dbManager, validator)
	if err != nil {
		return fmt.Errorf("failed to create TxPool: %v", err)
	}
	if err := txPool.Start(); err != nil {
		return fmt.Errorf("failed to start TxPool: %v", err)
	}
	node.TxPool = txPool

	// 6. 创建发送管理器
	senderManager := sender.NewSenderManager(dbManager, node.Address, txPool, node.ID)
	node.SenderManager = senderManager

	// 7. 初始化共识系统
	nodeID := types.NodeID(node.Address)
	config := consensus.DefaultConfig()

	// 调整配置
	config.Network.NumNodes = 100
	config.Network.NumByzantineNodes = 0
	config.Consensus.NumHeights = 10     // 运行10个高度
	config.Consensus.BlocksPerHeight = 3 // 每个高度3个候选块
	config.Consensus.K = 10              // 采样10个节点
	config.Consensus.Alpha = 7           // 需要7个回应
	config.Consensus.Beta = 5            // 5次连续投票确认
	config.Node.ProposalInterval = 5 * time.Second

	// 创建共识管理器
	consensusManager := consensus.InitConsensusManager(
		nodeID,
		dbManager,
		config,
		senderManager,
		txPool,
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
	)
	node.HandlerManager = handlerManager

	// 保存节点信息到数据库
	nodeInfo := &db.NodeInfo{
		PublicKey: keyMgr.GetPublicKey(),
		Ip:        fmt.Sprintf("127.0.0.1:%s", node.Port),
		IsOnline:  true,
	}

	if err := dbManager.SaveNodeInfo(nodeInfo); err != nil {
		return fmt.Errorf("failed to save node info: %v", err)
	}

	// 创建账户
	account := &db.Account{
		Address:   node.Address,
		PublicKey: keyMgr.GetPublicKey(),
		Ip:        fmt.Sprintf("127.0.0.1:%s", node.Port),
		Index:     uint64(node.ID),
		IsMiner:   true,
		Balances:  make(map[string]*db.TokenBalance),
	}

	// 初始化FB代币余额
	account.Balances["FB"] = &db.TokenBalance{
		Balance:            "1000000",
		MinerLockedBalance: "100000",
	}

	if err := dbManager.SaveAccount(account); err != nil {
		return fmt.Errorf("failed to save account: %v", err)
	}
	// 保存索引映射
	indexKey := fmt.Sprintf("indexToAccount_%d", account.Index)
	accountKey := fmt.Sprintf("account_%s", account.Address)
	dbManager.EnqueueSet(indexKey, accountKey)
	// Force flush to ensure miner registration is persisted
	dbManager.ForceFlush()
	return nil
}

// Option 2: Generate transactions continuously
func generateTransactions(node *NodeInstance) {
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		txID := 0
		for range ticker.C {
			// Generate multiple transactions
			for i := 0; i < 2; i++ {
				tx := &db.Transaction{
					Base: &db.BaseMessage{
						TxId:        fmt.Sprintf("%d%d", i, time.Now().UnixNano()),
						FromAddress: node.Address,
						Status:      db.Status_PENDING,
						Nonce:       uint64(i),
					},
					To:           node.Address,
					TokenAddress: "FB",
					Amount:       "100",
				}
				anyTx := &db.AnyTx{
					Content: &db.AnyTx_Transaction{Transaction: tx},
				}
				// Add to transaction pool
				if err := node.TxPool.StoreAnyTx(anyTx); err == nil {
					logs.Trace("Added transaction %s to pool", tx.Base.TxId)
				}
			}
			txID++
		}
	}()
}

// 注册所有节点信息到每个节点的数据库
func registerAllNodes(nodes []*NodeInstance) {
	for i, node := range nodes {
		if node == nil || node.DBManager == nil {
			continue
		}

		// 在当前节点的数据库中注册所有其他节点
		for j, otherNode := range nodes {
			if otherNode == nil || i == j {
				continue
			}

			// 保存其他节点的账户信息
			account := &db.Account{
				Address:   otherNode.Address,
				PublicKey: utils.GetKeyManager().GetPublicKey(), // 这里简化处理
				Ip:        fmt.Sprintf("127.0.0.1:%s", otherNode.Port),
				Index:     uint64(j),
				IsMiner:   true,
				Balances:  make(map[string]*db.TokenBalance),
			}

			account.Balances["FB"] = &db.TokenBalance{
				Balance:            "1000000",
				MinerLockedBalance: "100000",
			}

			node.DBManager.SaveAccount(account)

			// 保存节点信息
			nodeInfo := &db.NodeInfo{
				PublicKey: fmt.Sprintf("node_%d_pub", j),
				Ip:        fmt.Sprintf("127.0.0.1:%s", otherNode.Port),
				IsOnline:  true,
			}
			node.DBManager.SaveNodeInfo(nodeInfo)
			// 保存索引映射
			indexKey := fmt.Sprintf("indexToAccount_%d", j)
			accountKey := fmt.Sprintf("account_%s", otherNode.Address)
			node.DBManager.EnqueueSet(indexKey, accountKey)

		}
		// Force flush to ensure all registrations are persisted
		node.DBManager.ForceFlush()
		time.Sleep(100 * time.Millisecond) // 确保写入完成

		// 重新扫描数据库重建 bitmap
		if err := node.DBManager.IndexMgr.RebuildBitmapFromDB(); err != nil {
			logs.Error("Failed to rebuild bitmap: %v", err)
		}
	}
}

// 生成自签名证书
func generateSelfSignedCert(certFile, keyFile string) error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		DNSNames:     []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(
		rand.Reader,
		&template,
		&template,
		&priv.PublicKey,
		priv,
	)
	if err != nil {
		return err
	}

	// 保存证书
	certOut, err := os.Create(certFile)
	if err != nil {
		return err
	}
	defer certOut.Close()

	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	// 保存私钥
	keyOut, err := os.Create(keyFile)
	if err != nil {
		return err
	}
	defer keyOut.Close()

	privBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return err
	}

	pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})

	return nil
}

// 监控共识进度
func monitorProgress(nodes []*NodeInstance) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		var minHeight, maxHeight uint64
		activeNodes := 0
		fmt.Println("[monitor] tick")
		for _, node := range nodes {
			if node == nil || node.ConsensusManager == nil {
				continue
			}

			activeNodes++
			_, height := node.ConsensusManager.GetLastAccepted()

			if minHeight == 0 || height < minHeight {
				minHeight = height
			}
			if height > maxHeight {
				maxHeight = height
			}
		}

		fmt.Printf("\n📈 Progress: Active=%d, MinHeight=%d, MaxHeight=%d\n",
			activeNodes, minHeight, maxHeight)

		// 打印一些节点的详细状态
		heightCount := map[uint64]int{}
		activeQueries := int64(0)
		for _, node := range nodes {
			if node == nil || node.ConsensusManager == nil {
				continue
			}
			_, h := node.ConsensusManager.GetLastAccepted()
			heightCount[h]++

			sent := int64(node.ConsensusManager.GetStats().QueriesSent)
			responded := int64(node.ConsensusManager.GetStats().ChitsResponded)

			// 每个节点的 pending = sent - 它收到的响应（不是它发出的响应）
			nodePending := sent - responded
			if nodePending > 0 {
				activeQueries += nodePending
			}
		}
		fmt.Printf("  Heights histogram: %+v\n", heightCount)
		fmt.Printf("  Pending queries (approx): %d\n", activeQueries)
		for i := 0; i < 3 && i < len(nodes); i++ {
			if nodes[i] != nil && nodes[i].ConsensusManager != nil {
				accepted, height := nodes[i].ConsensusManager.GetLastAccepted()
				fmt.Printf("  Node %d: height=%d, block=%s\n", i, height, accepted)
			}
		}
	}
}

// 关闭所有节点
func shutdownAllNodes(nodes []*NodeInstance) {
	var wg sync.WaitGroup

	for _, node := range nodes {
		if node == nil {
			continue
		}

		wg.Add(1)
		go func(n *NodeInstance) {
			defer wg.Done()

			// 停止共识
			if n.ConsensusManager != nil {
				n.ConsensusManager.Stop()
			}

			// 停止Handler管理器（新增）
			if n.HandlerManager != nil {
				n.HandlerManager.Stop()
			}

			// 停止Sender管理器
			if n.SenderManager != nil {
				n.SenderManager.Stop()
			}

			// 停止TxPool
			if n.TxPool != nil {
				n.TxPool.Stop()
			}

			// 停止HTTP服务器
			if n.Server != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				n.Server.Shutdown(ctx)
			}

			// 关闭数据库
			if n.DBManager != nil {
				n.DBManager.Close()
			}

			fmt.Printf("  ✓ Node %d stopped\n", n.ID)
		}(node)
	}

	wg.Wait()
}
