package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
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
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// NodeInstance 表示一个节点实例
type NodeInstance struct {
	ID               int
	PrivateKey       string
	Address          string
	Port             string
	DataPath         string
	Server           *http.Server
	ConsensusManager *consensus.ConsensusNodeManager
	DBManager        *db.Manager
	TxPoolQueue      *txpool.TxPoolQueue
	SenderQueue      *sender.SendQueue
	Cancel           context.CancelFunc
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
	// 配置参数
	const numNodes = 100
	basePort := 6000

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
			ID:         i,
			PrivateKey: privateKeys[i],
			Port:       fmt.Sprintf("%d", basePort+i),
			DataPath:   fmt.Sprintf("./data_node_%d", i),
		}

		// 清理旧数据
		os.RemoveAll(node.DataPath)

		// 初始化节点
		if err := initializeNode(node); err != nil {
			logs.Error("Failed to initialize node %d: %v", i, err)
			continue
		}

		nodes[i] = node
		fmt.Printf("  ✓ Node %d initialized (port %s)\n", i, node.Port)
	}

	// 等待一下让所有数据库完成初始化
	time.Sleep(2 * time.Second)

	// 第二阶段：注册所有节点到数据库（让节点互相知道）
	fmt.Println("🔗 Phase 2: Registering all nodes...")
	registerAllNodes(nodes)

	// 第三阶段：启动所有HTTP服务器
	fmt.Println("🌐 Phase 3: Starting HTTP servers...")
	for _, node := range nodes {
		if node == nil {
			continue
		}
		wg.Add(1)
		go func(n *NodeInstance) {
			defer wg.Done()
			startHTTPServer(n)
		}(node)
		time.Sleep(50 * time.Millisecond) // 避免同时启动太多
	}

	// 等待服务器启动
	time.Sleep(5 * time.Second)

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

// initializeNode 初始化单个节点
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

	// 4. 初始化TxPool队列
	validator := &TestValidator{}
	txpool.InitTxPoolQueue(dbManager, validator)

	// 5. 初始化发送队列
	sender.InitQueue(10, 10000)

	// 6. 初始化共识系统
	nodeID := types.NodeID(fmt.Sprintf("%d", node.ID))
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
	consensusManager := consensus.InitConsensusManager(nodeID, dbManager, config)
	node.ConsensusManager = consensusManager

	// 保存节点信息到数据库
	nodeInfo := &db.NodeInfo{
		PublicKey: keyMgr.GetPublicKey(),
		Ip:        fmt.Sprintf("127.0.0.1:%s", node.Port),
		IsOnline:  true,
	}

	if err := db.SaveNodeInfo(dbManager, nodeInfo); err != nil {
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

	if err := db.SaveAccount(dbManager, account); err != nil {
		return fmt.Errorf("failed to save account: %v", err)
	}

	return nil
}

// registerAllNodes 注册所有节点信息到每个节点的数据库
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

			db.SaveAccount(node.DBManager, account)

			// 保存节点信息
			nodeInfo := &db.NodeInfo{
				PublicKey: fmt.Sprintf("node_%d_pub", j),
				Ip:        fmt.Sprintf("127.0.0.1:%s", otherNode.Port),
				IsOnline:  true,
			}
			db.SaveNodeInfo(node.DBManager, nodeInfo)
		}
	}
}

// startHTTPServer 启动HTTP服务器
func startHTTPServer(node *NodeInstance) {
	// 创建HTTP路由
	mux := http.NewServeMux()

	// 设置handlers的共识管理器
	handlers.ConsensusManager = node.ConsensusManager

	// 注册路由
	mux.HandleFunc("/pushquery", handlers.HandlePushQuery)
	mux.HandleFunc("/pullquery", handlers.HandlePullQuery)
	mux.HandleFunc("/pullcontainer", handlers.HandlePullContainer)
	mux.HandleFunc("/status", handlers.HandleStatus)
	mux.HandleFunc("/tx", handlers.HandleTx)
	mux.HandleFunc("/getblock", handlers.HandleGetBlock)
	mux.HandleFunc("/getdata", handlers.HandleGetData)
	mux.HandleFunc("/batchgetdata", handlers.HandleBatchGetTx)
	mux.HandleFunc("/nodes", handlers.HandleNodes)

	// 应用中间件
	handler := middleware.RateLimit(mux)

	// 创建服务器
	server := &http.Server{
		Addr:         ":" + node.Port,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	node.Server = server

	// 生成自签名证书
	certFile := fmt.Sprintf("server_%d.crt", node.ID)
	keyFile := fmt.Sprintf("server_%d.key", node.ID)

	if err := generateSelfSignedCert(certFile, keyFile); err != nil {
		logs.Error("Node %d: Failed to generate certificate: %v", node.ID, err)
		return
	}

	// 启动HTTPS服务
	logs.Info("Node %d: Starting HTTPS server on port %s", node.ID, node.Port)
	if err := server.ListenAndServeTLS(certFile, keyFile); err != http.ErrServerClosed {
		logs.Error("Node %d: Server error: %v", node.ID, err)
	}
}

// generateSelfSignedCert 生成自签名证书
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

// monitorProgress 监控共识进度
func monitorProgress(nodes []*NodeInstance) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		var minHeight, maxHeight uint64
		activeNodes := 0

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
		for i := 0; i < 3 && i < len(nodes); i++ {
			if nodes[i] != nil && nodes[i].ConsensusManager != nil {
				accepted, height := nodes[i].ConsensusManager.GetLastAccepted()
				fmt.Printf("  Node %d: height=%d, block=%s\n", i, height, accepted)
			}
		}
	}
}

// shutdownAllNodes 关闭所有节点
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
