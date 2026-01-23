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
	"dex/frost/chain"
	"dex/frost/chain/btc"
	"dex/frost/chain/evm"
	"dex/frost/chain/solana"
	frostrt "dex/frost/runtime"
	"dex/frost/runtime/adapters"
	"dex/frost/runtime/committee"
	"dex/handlers"
	"dex/keys"
	"dex/logs"
	"dex/middleware"
	"dex/pb"
	"dex/sender"
	"dex/txpool"
	"dex/types"
	"dex/utils"
	"dex/vm"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	mrand "math/rand"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"google.golang.org/protobuf/proto"
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

// TestValidator 简单的交易验证器
type TestValidator struct{}

// 接口调用统计结构体
type APICallStats struct {
	sync.RWMutex
	// 记录每个接口的累计调用次数
	CallCounts map[string]uint64
	// 记录每个节点每个接口的调用次数
	NodeCallCounts map[int]map[string]uint64
}

// 全局接口调用统计
var globalAPIStats = &APICallStats{
	CallCounts:     make(map[string]uint64),
	NodeCallCounts: make(map[int]map[string]uint64),
}

func monitorMetrics(nodes []*NodeInstance) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	// 用于记录每个节点上次的调用次数，计算增量
	lastCallCounts := make(map[int]map[string]uint64)

	for range ticker.C {
		// 临时存储当前周期的统计数据
		currentStats := make(map[string]uint64)
		nodeStats := make(map[int]map[string]uint64)

		for _, node := range nodes {
			if node == nil || node.SenderManager == nil {
				continue
			}

			// 1. 发送队列长度（控制面+数据面）
			sendQueueLen := node.SenderManager.SendQueue.QueueLen()

			// 2. 每目标在途请求
			node.SenderManager.SendQueue.InflightMutex.RLock()
			inflightCopy := make(map[string]int32)
			totalInflight := int32(0)
			for k, v := range node.SenderManager.SendQueue.InflightMap {
				inflightCopy[k] = v
				totalInflight += v
			}
			node.SenderManager.SendQueue.InflightMutex.RUnlock()

			// 3. 接收队列长度
			recvQueueLen := 0
			if node.ConsensusManager != nil && node.ConsensusManager.Transport != nil {
				// 根据你的实际Transport类型调整
				// if rt, ok := node.ConsensusManager.Transport.(*consensus.ReliableTransport); ok {
				//     recvQueueLen = rt.GetRecvQueueLength()
				// }
			}

			// 4. 接口调用统计（新增）
			if node.HandlerManager != nil {
				apiStats := node.HandlerManager.Stats.GetAPICallStats()

				// 记录当前节点的API调用统计
				nodeStats[node.ID] = apiStats

				// 计算增量并更新全局统计
				if lastCallCounts[node.ID] == nil {
					lastCallCounts[node.ID] = make(map[string]uint64)
				}

				for apiName, currentCount := range apiStats {
					// 计算这个周期的增量
					delta := currentCount
					if lastCount, exists := lastCallCounts[node.ID][apiName]; exists {
						delta = currentCount - lastCount
					}

					// 更新全局统计
					currentStats[apiName] += delta

					// 更新上次记录
					lastCallCounts[node.ID][apiName] = currentCount
				}
			}

			// 打印节点指标
			if sendQueueLen > 0 || totalInflight > 0 || recvQueueLen > 0 {
				fmt.Printf("[Metrics] Node %d: SendQ=%d, Inflight=%d, RecvQ=%d\n",
					node.ID, sendQueueLen, totalInflight, recvQueueLen)
			}
		}

		// 更新全局API调用统计
		globalAPIStats.Lock()
		for apiName, delta := range currentStats {
			globalAPIStats.CallCounts[apiName] += delta
		}
		for nodeID, apis := range nodeStats {
			globalAPIStats.NodeCallCounts[nodeID] = apis
		}
		globalAPIStats.Unlock()

		printAPICallStatistics()
	}
}

// 打印API调用统计
func printAPICallStatistics() {
	globalAPIStats.RLock()
	defer globalAPIStats.RUnlock()

	if len(globalAPIStats.CallCounts) == 0 {
		return
	}

	fmt.Println("\n========== API Call Statistics ==========")
	fmt.Println("Global API Call Counts:")

	// 按接口名称排序
	var apiNames []string
	for apiName := range globalAPIStats.CallCounts {
		apiNames = append(apiNames, apiName)
	}
	sort.Strings(apiNames)

	// 打印全局统计
	totalCalls := uint64(0)
	for _, apiName := range apiNames {
		count := globalAPIStats.CallCounts[apiName]
		totalCalls += count
		fmt.Printf("  %-30s: %10d calls\n", apiName, count)
	}
	fmt.Printf("  %-30s: %10d calls\n", "TOTAL", totalCalls)

	// 打印每个节点的统计（可选）
	if len(globalAPIStats.NodeCallCounts) > 0 {
		//fmt.Println("\nPer-Node API Call Distribution:")

		// 按节点ID排序
		var nodeIDs []int
		for nodeID := range globalAPIStats.NodeCallCounts {
			nodeIDs = append(nodeIDs, nodeID)
		}
		sort.Ints(nodeIDs)

		for _, nodeID := range nodeIDs {
			apis := globalAPIStats.NodeCallCounts[nodeID]
			if len(apis) == 0 {
				continue
			}

			nodeTotalCalls := uint64(0)
			//fmt.Printf("\n  Node %d:\n", nodeID)

			// 按接口名称排序
			var nodeAPINames []string
			for apiName := range apis {
				nodeAPINames = append(nodeAPINames, apiName)
			}
			sort.Strings(nodeAPINames)

			for _, apiName := range nodeAPINames {
				count := apis[apiName]
				nodeTotalCalls += count
				//fmt.Printf("    %-28s: %8d\n", apiName, count)
			}
			//fmt.Printf("    %-28s: %8d\n", "Node Total", nodeTotalCalls)
		}
	}

	// 打印API调用频率分析
	fmt.Println("\nAPI Call Frequency Analysis:")
	if totalCalls > 0 {
		for _, apiName := range apiNames {
			count := globalAPIStats.CallCounts[apiName]
			percentage := float64(count) * 100.0 / float64(totalCalls)

			// 创建一个简单的条形图
			barLength := int(percentage / 2)
			if barLength > 40 {
				barLength = 40
			}
			bar := strings.Repeat("█", barLength)

			fmt.Printf("  %-25s: %6.2f%% %s\n", apiName, percentage, bar)
		}
	}

	fmt.Println("==========================================")
}
func (v *TestValidator) CheckAnyTx(tx *pb.AnyTx) error {
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

// 全局创世配置
var genesisConfig *config.GenesisConfig

func main() {
	// 加载配置
	cfg, _ := config.LoadFromFile("")
	// 配置参数
	numNodes := cfg.Network.DefaultNumNodes
	basePort := cfg.Network.BasePort

	// 加载创世配置
	var err error
	genesisConfig, err = config.LoadGenesisConfig("config/genesis.json")
	if err != nil {
		fmt.Printf("⚠️  Warning: Failed to load genesis config: %v, using defaults\n", err)
		genesisConfig = config.DefaultGenesisConfig()
	} else {
		fmt.Println("📜 Loaded genesis config from config/genesis.json")
	}

	fmt.Printf("🚀 Starting %d real consensus nodes...\n", numNodes)

	// 生成节点私钥
	privateKeys := generatePrivateKeys(numNodes)

	// 创建所有节点实例
	nodes := make([]*NodeInstance, numNodes)
	var wg sync.WaitGroup

	// 第一阶段：初始化所有节点（创建数据库和基础设施）
	fmt.Println("📦 Phase 1: Initializing all nodes...")
	for i := 0; i < numNodes; i++ {

		// Pre-derive address to ensure Logger is registered with the correct address
		normalizedKey, err := normalizeSecpPrivKey(privateKeys[i])
		if err != nil {
			logs.Error("Failed to normalize private key for node %d: %v", i, err)
			continue
		}
		privK, err := utils.ParseSecp256k1PrivateKey(normalizedKey)
		if err != nil {
			logs.Error("Failed to parse private key for node %d: %v", i, err)
			continue
		}
		address, err := utils.DeriveBtcBech32Address(privK)
		if err != nil {
			logs.Error("Failed to derive address for node %d: %v", i, err)
			continue
		}

		node := &NodeInstance{
			Address:    address, // Use the correct address immediately
			ID:         i,
			PrivateKey: normalizedKey,
			Port:       fmt.Sprintf("%d", basePort+i),
			DataPath:   fmt.Sprintf("./data/data_node_%d", i),
		}

		// 清理旧数据
		os.RemoveAll(node.DataPath)

		// 第一步：创建该节点的私有 Logger (using the correct address)
		node.Logger = logs.NewNodeLogger(node.Address, 2000)

		// 初始化节点
		if err := initializeNode(node, cfg); err != nil {
			node.Logger.Error("Failed to initialize node %d: %v", i, err)
			continue
		}

		nodes[i] = node
		fmt.Printf("  ✔ Node %d initialized (port %s)\n", i, node.Port)
	}

	// 等待一下让所有数据库完成初始化
	time.Sleep(2 * time.Second)

	// 第二阶段：注册所有节点到数据库（让节点互相知道）
	fmt.Println("🔗 Phase 2: Registering all nodes...")
	registerAllNodes(nodes, cfg.Frost)

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
			goto ContinueWithConsensus
		}

		readyCount++
	}

	fmt.Printf("✅ All %d HTTP/3 servers are ready!\n", successfulNodes)

ContinueWithConsensus:
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

	// 第3.6阶段：启动 FROST Runtime（如果配置开启）
	// 第3.6阶段：启动 FROST Runtime (配置已默认开启)
	if cfg.Frost.Enabled {
		fmt.Println("🔐 Phase 3.6: Initializing FROST Runtime & Handlers...")
		for _, node := range nodes {
			if node == nil {
				continue
			}

			// 创建真实的依赖适配器
			var stateReader frostrt.ChainStateReader
			if node.DBManager != nil {
				stateReader = adapters.NewStateDBReader(node.DBManager)
			}

			// 创建 ChainAdapterFactory 并注册链适配器
			adapterFactory := chain.NewDefaultAdapterFactory()
			adapterFactory.RegisterAdapter(btc.NewBTCAdapter("mainnet")) // BTC 适配器

			// 注册 EVM 适配器
			if evmAdapter := evm.NewETHAdapter(); evmAdapter != nil {
				adapterFactory.RegisterAdapter(evmAdapter)
			}
			if bnbAdapter := evm.NewBNBAdapter(); bnbAdapter != nil {
				adapterFactory.RegisterAdapter(bnbAdapter)
			}

			// 注册 Solana 适配器
			if solAdapter := solana.NewSolanaAdapter(""); solAdapter != nil {
				adapterFactory.RegisterAdapter(solAdapter)
			}

			// 创建 TxSubmitter（适配 txpool）
			var txSubmitter frostrt.TxSubmitter
			if node.TxPool != nil && node.SenderManager != nil {
				txPoolAdapter := adapters.NewTxPoolAdapter(node.TxPool, node.SenderManager)
				txSubmitter = adapters.NewTxPoolSubmitter(txPoolAdapter)
			}

			// 创建 FinalityNotifier（适配 EventBus）
			var notifier frostrt.FinalityNotifier
			if node.ConsensusManager != nil {
				eventBus := node.ConsensusManager.GetEventBus()
				if eventBus != nil {
					notifier = adapters.NewEventBusFinalityNotifier(eventBus)
				}
			}

			// 创建 P2P（适配 Transport）
			var p2p frostrt.P2P
			if node.ConsensusManager != nil && node.ConsensusManager.Transport != nil {
				p2p = adapters.NewTransportP2P(node.ConsensusManager.Transport, frostrt.NodeID(node.Address))
			}

			// 创建 SignerProvider（从 consensus 获取）
			var signerProvider frostrt.SignerSetProvider
			if node.DBManager != nil {
				dbAdapter := adapters.NewDBManagerAdapter(node.DBManager)
				signerProvider = adapters.NewConsensusSignerProvider(dbAdapter)
			}

			// 创建 VaultProvider（使用 DefaultVaultCommitteeProvider）
			var vaultProvider frostrt.VaultCommitteeProvider
			if stateReader != nil {
				vaultProvider = committee.NewDefaultVaultCommitteeProvider(stateReader, committee.DefaultVaultCommitteeProviderConfig())
			}
			var pubKeyProvider frostrt.MinerPubKeyProvider
			if stateReader != nil {
				pubKeyProvider = adapters.NewStatePubKeyProvider(stateReader)
			}
			cryptoFactory := adapters.NewDefaultCryptoExecutorFactory()

			// 加载 FrostConfig
			frostConfig := cfg.Frost

			// 从 FrostConfig 提取支持的链
			var supportedChains []frostrt.ChainAssetPair
			for chainName := range frostConfig.Chains {
				// 根据链名确定资产名（简化处理）
				asset := strings.ToUpper(chainName)
				if chainName == "eth" {
					asset = "ETH"
				} else if chainName == "bnb" {
					asset = "BNB"
				} else if chainName == "btc" {
					asset = "BTC"
				} else if chainName == "sol" {
					asset = "SOL"
				} else if chainName == "trx" {
					asset = "TRX"
				}
				supportedChains = append(supportedChains, frostrt.ChainAssetPair{
					Chain: chainName,
					Asset: asset,
				})
			}

			// 创建 FROST Runtime Manager
			frostCfg := frostrt.ManagerConfig{
				NodeID:          frostrt.NodeID(node.Address),
				ScanInterval:    1 * time.Second,
				SupportedChains: supportedChains,
			}
			var roastMessenger frostrt.RoastMessenger
			if node.SenderManager != nil {
				roastMessenger = adapters.NewSenderRoastMessenger(node.SenderManager)
			}
			frostDeps := frostrt.ManagerDeps{
				StateReader:    stateReader,
				TxSubmitter:    txSubmitter,
				Notifier:       notifier,
				P2P:            p2p,
				RoastMessenger: roastMessenger,
				SignerProvider: signerProvider,
				VaultProvider:  vaultProvider,
				AdapterFactory: adapterFactory,
				PubKeyProvider: pubKeyProvider,
				CryptoFactory:  cryptoFactory,
				Logger:         node.Logger, // 修复：注入节点日志器，防止 Start 时出现空指针 Panic
			}
			frostManager := frostrt.NewManager(frostCfg, frostDeps)
			node.FrostRuntime = frostManager

			if node.HandlerManager != nil {
				node.HandlerManager.SetFrostMsgHandler(func(msg types.Message) error {
					if node.FrostRuntime == nil {
						return nil
					}
					if len(msg.FrostPayload) == 0 {
						return fmt.Errorf("empty frost payload")
					}
					var env pb.FrostEnvelope
					if err := proto.Unmarshal(msg.FrostPayload, &env); err != nil {
						return err
					}
					return node.FrostRuntime.HandlePBEnvelope(&env)
				})
			}

			// 启动 FROST Runtime (异步等待区块产生后再正式工作)
			go func(n *NodeInstance, fm *frostrt.Manager) {
				logs.SetThreadNodeContext(n.Address)

				// 等待高度 > 0 且共识引擎就绪
				for {
					if n.ConsensusManager == nil {
						break
					}
					_, height := n.ConsensusManager.GetLastAccepted()
					if height >= 1 {
						break
					}
					time.Sleep(2 * time.Second)
				}

				if err := fm.Start(context.Background()); err != nil {
					logs.Error("[Frost] Failed to start runtime for node %d: %v", n.ID, err)
				} else {
					fmt.Printf(" ✅ Node %d FROST Runtime started (height >= 1)\n", n.ID)
				}
			}(node, frostManager)
		}
	}

	// Create initial transactions
	fmt.Println("📝 Creating initial transactions...")
	generateTransactions(nodes)
	time.Sleep(2 * time.Second)

	// 第四阶段：启动共识
	fmt.Println("🎯 Phase 4: Starting consensus engines...")
	for _, node := range nodes {
		if node != nil && node.ConsensusManager != nil {
			// 触发初始查询
			go func(n *NodeInstance) {
				logs.SetThreadNodeContext(n.Address)
				time.Sleep(time.Duration(n.ID*100) * time.Millisecond) // 错开启动
				n.ConsensusManager.StartQuery()
			}(node)
		}
	}
	// 启动指标监控
	go monitorMetrics(nodes)
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

	// 保存节点信息到数据库
	nodeInfo := &pb.NodeInfo{
		PublicKeys: &pb.PublicKeys{
			Keys: map[int32][]byte{
				int32(pb.SignAlgo_SIGN_ALGO_ECDSA_P256): []byte(keyMgr.GetPublicKey()),
			},
		},
		Ip:       fmt.Sprintf("127.0.0.1:%s", node.Port),
		IsOnline: true,
	}

	if err := dbManager.SaveNodeInfo(nodeInfo); err != nil {
		return fmt.Errorf("failed to save node info: %v", err)
	}

	// 创建账户
	account := &pb.Account{
		Address: node.Address,
		PublicKeys: &pb.PublicKeys{
			Keys: map[int32][]byte{
				int32(pb.SignAlgo_SIGN_ALGO_ECDSA_P256): []byte(keyMgr.GetPublicKey()),
			},
		},
		Ip:       fmt.Sprintf("127.0.0.1:%s", node.Port),
		Index:    uint64(node.ID),
		IsMiner:  true,
		Balances: make(map[string]*pb.TokenBalance),
	}

	// 从创世配置初始化余额
	applyGenesisBalances(account)

	if err := dbManager.SaveAccount(account); err != nil {
		return fmt.Errorf("failed to save account: %v", err)
	}
	// 保存索引映射
	indexKey := db.KeyIndexToAccount(account.Index)
	accountKey := db.KeyAccount(account.Address)
	dbManager.EnqueueSet(indexKey, accountKey)

	// 初始化创世代币（只在第一个节点时执行一次）
	if node.ID == 0 {
		if err := initGenesisTokens(dbManager); err != nil {
			logs.Error("Failed to init genesis tokens: %v", err)
		}
	}

	// Force flush to ensure miner registration is persisted
	dbManager.ForceFlush()
	return nil
}

// Option 2: Generate transactions continuously
func generateTransactions(nodes []*NodeInstance) {
	simulator := NewTxSimulator(nodes)
	simulator.Start()
}

// 注册所有节点信息到每个节点的数据库
func registerAllNodes(nodes []*NodeInstance, frostCfg config.FrostConfig) {
	// 准备 Top10000 数据
	maxCount := len(nodes)
	if frostCfg.Committee.TopN > 0 && frostCfg.Committee.TopN < maxCount {
		maxCount = frostCfg.Committee.TopN
	}
	top10000 := &pb.FrostTop10000{
		Height:     0,
		Indices:    make([]uint64, maxCount),
		Addresses:  make([]string, maxCount),
		PublicKeys: make([][]byte, maxCount),
	}

	for i, node := range nodes {
		if i >= maxCount {
			break
		}
		top10000.Indices[i] = uint64(i)
		if node == nil {
			continue
		}
		top10000.Addresses[i] = node.Address

		normalizedKey, err := normalizeSecpPrivKey(node.PrivateKey)
		if err != nil {
			logs.Warn("Failed to normalize private key for node %d: %v", i, err)
			continue
		}
		privKey, err := utils.ParseSecp256k1PrivateKey(normalizedKey)
		if err != nil {
			logs.Warn("Failed to parse private key for node %d: %v", i, err)
			continue
		}
		top10000.PublicKeys[i] = privKey.PubKey().SerializeCompressed()
	}

	vaultConfigs := buildVaultConfigs(frostCfg, len(top10000.Indices))

	for i, node := range nodes {
		if node == nil || node.DBManager == nil {
			continue
		}

		// 注册节点 ID 和 端口 到地址的映射，用于日志归集（Explorer 仍需该映射）
		logs.RegisterNodeMapping(strconv.Itoa(node.ID), node.Address)
		logs.RegisterNodeMapping(node.Port, node.Address)
		logs.RegisterNodeMapping(fmt.Sprintf("127.0.0.1:%s", node.Port), node.Address) // host:port 格式
		logs.RegisterNodeMapping(node.Address, node.Address)                           // 地址本身也注册，确保日志缓冲区正确初始化

		if err := applyFrostBootstrap(node.DBManager, frostCfg, top10000, vaultConfigs); err != nil {
			logs.Warn("Failed to apply frost bootstrap for node %d: %v", node.ID, err)
		}

		// 在当前节点的数据库中注册所有其他节点
		for j, otherNode := range nodes {
			if otherNode == nil || i == j {
				continue
			}

			// 保存其他节点的账户信息
			account := &pb.Account{
				Address: otherNode.Address,
				PublicKeys: &pb.PublicKeys{
					Keys: map[int32][]byte{
						int32(pb.SignAlgo_SIGN_ALGO_ECDSA_P256): []byte(utils.GetKeyManager().GetPublicKey()),
					},
				},
				Ip:       fmt.Sprintf("127.0.0.1:%s", otherNode.Port),
				Index:    uint64(j),
				IsMiner:  true,
				Balances: make(map[string]*pb.TokenBalance),
			}

			// 从创世配置初始化余额
			applyGenesisBalances(account)

			node.DBManager.SaveAccount(account)

			// 保存节点信息
			nodeInfo := &pb.NodeInfo{
				PublicKeys: &pb.PublicKeys{
					Keys: map[int32][]byte{
						int32(pb.SignAlgo_SIGN_ALGO_ECDSA_P256): []byte(fmt.Sprintf("node_%d_pub", j)),
					},
				},
				Ip:       fmt.Sprintf("127.0.0.1:%s", otherNode.Port),
				IsOnline: true,
			}
			node.DBManager.SaveNodeInfo(nodeInfo)
			// 保存索引映射
			indexKey := db.KeyIndexToAccount(uint64(j))
			accountKey := db.KeyAccount(otherNode.Address)
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

func buildVaultConfigs(frostCfg config.FrostConfig, minerCount int) map[string]*pb.FrostVaultConfig {
	vaultConfigs := make(map[string]*pb.FrostVaultConfig)
	thresholdRatio := frostCfg.Vault.ThresholdRatio
	if thresholdRatio <= 0 {
		thresholdRatio = frostCfg.Committee.ThresholdRatio
	}
	if thresholdRatio <= 0 {
		thresholdRatio = 0.67
	}

	for chainName, chainCfg := range frostCfg.Chains {
		if chainCfg.FrostVariant == "" {
			continue
		}
		vaultCount := chainCfg.VaultsPerChain
		if vaultCount <= 0 {
			vaultCount = 1
		}
		committeeSize := frostCfg.Vault.DefaultK
		if committeeSize <= 0 {
			committeeSize = 1
		}
		if minerCount > 0 && committeeSize > minerCount {
			committeeSize = minerCount
		}

		signAlgo := parseSignAlgo(chainCfg.SignAlgo)
		if signAlgo == pb.SignAlgo_SIGN_ALGO_UNSPECIFIED {
			signAlgo = pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340
		}

		vaultConfigs[chainName] = &pb.FrostVaultConfig{
			Chain:          chainName,
			SignAlgo:       signAlgo,
			VaultCount:     uint32(vaultCount),
			CommitteeSize:  uint32(committeeSize),
			ThresholdRatio: float32(thresholdRatio),
		}
	}
	return vaultConfigs
}

func parseSignAlgo(raw string) pb.SignAlgo {
	if raw == "" {
		return pb.SignAlgo_SIGN_ALGO_UNSPECIFIED
	}
	value := strings.TrimSpace(strings.ToUpper(raw))
	value = strings.TrimPrefix(value, "SIGN_ALGO_")
	switch value {
	case "ECDSA_P256":
		return pb.SignAlgo_SIGN_ALGO_ECDSA_P256
	case "BLS_BN256_G2":
		return pb.SignAlgo_SIGN_ALGO_BLS_BN256_G2
	case "SCHNORR_SECP256K1_BIP340":
		return pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340
	case "SCHNORR_ALT_BN128":
		return pb.SignAlgo_SIGN_ALGO_SCHNORR_ALT_BN128
	case "ED25519":
		return pb.SignAlgo_SIGN_ALGO_ED25519
	case "ECDSA_SECP256K1":
		return pb.SignAlgo_SIGN_ALGO_ECDSA_SECP256K1
	default:
		return pb.SignAlgo_SIGN_ALGO_UNSPECIFIED
	}
}

func applyFrostBootstrap(dbManager *db.Manager, frostCfg config.FrostConfig, top10000 *pb.FrostTop10000, vaultConfigs map[string]*pb.FrostVaultConfig) error {
	if dbManager == nil || top10000 == nil || len(vaultConfigs) == 0 {
		return nil
	}

	readFn := func(key string) ([]byte, error) {
		val, err := dbManager.Get(key)
		if err != nil {
			if isNotFoundError(err) {
				return nil, nil
			}
			return nil, err
		}
		return val, nil
	}
	scanFn := func(prefix string) (map[string][]byte, error) {
		return dbManager.Scan(prefix)
	}
	sv := vm.NewStateView(readFn, scanFn)

	if existing, exists, err := sv.Get(keys.KeyFrostTop10000()); err != nil {
		return err
	} else if !exists || len(existing) == 0 {
		data, err := proto.Marshal(top10000)
		if err != nil {
			return err
		}
		sv.Set(keys.KeyFrostTop10000(), data)
	}

	epochID := uint64(1)
	triggerHeight := uint64(0)
	commitWindow := uint64(frostCfg.Transition.DkgCommitWindowBlocks)
	sharingWindow := uint64(frostCfg.Transition.DkgSharingWindowBlocks)
	disputeWindow := uint64(frostCfg.Transition.DkgDisputeWindowBlocks)

	for chainName, cfg := range vaultConfigs {
		if cfg == nil {
			continue
		}
		if err := vm.InitVaultConfig(sv, chainName, cfg); err != nil {
			logs.Warn("InitVaultConfig failed for chain %s: %v", chainName, err)
		}
		if err := vm.InitVaultStates(sv, chainName, epochID); err != nil {
			logs.Warn("InitVaultStates failed for chain %s: %v", chainName, err)
		}
		if err := vm.InitVaultTransitions(sv, chainName, epochID, triggerHeight, commitWindow, sharingWindow, disputeWindow); err != nil {
			logs.Warn("InitVaultTransitions failed for chain %s: %v", chainName, err)
		}
	}

	for _, op := range sv.Diff() {
		if op.Del {
			dbManager.EnqueueDel(op.Key)
			continue
		}
		dbManager.EnqueueSet(op.Key, string(op.Value))
	}

	return dbManager.ForceFlush()
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, badger.ErrKeyNotFound) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "key not found")
}

func normalizeSecpPrivKey(key string) (string, error) {
	normalized := strings.TrimSpace(key)
	if normalized == "" {
		return "", fmt.Errorf("empty private key")
	}
	if strings.HasPrefix(normalized, "0x") || strings.HasPrefix(normalized, "0X") {
		normalized = normalized[2:]
	}
	if !isHexString(normalized) {
		return "", fmt.Errorf("private key must be hex")
	}
	if len(normalized)%2 != 0 {
		normalized = "0" + normalized
	}
	if len(normalized) > 64 {
		normalized = normalized[len(normalized)-64:]
	}
	if len(normalized) < 64 {
		normalized = strings.Repeat("0", 64-len(normalized)) + normalized
	}
	return normalized, nil
}

func isHexString(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

// applyGenesisBalances 从创世配置应用余额到账户
func applyGenesisBalances(account *pb.Account) {
	if genesisConfig == nil {
		// 没有创世配置，使用默认值
		account.Balances["FB"] = &pb.TokenBalance{
			Balance:            "1000000",
			MinerLockedBalance: "100000",
		}
		return
	}

	// 查找账户特定的配置，如果没有则使用 "default"
	alloc, exists := genesisConfig.Alloc[account.Address]
	if !exists {
		alloc, exists = genesisConfig.Alloc["default"]
	}

	if !exists {
		// 没有配置，使用默认值
		account.Balances["FB"] = &pb.TokenBalance{
			Balance:            "1000000",
			MinerLockedBalance: "100000",
		}
		return
	}

	// 应用配置中的余额
	for tokenAddr, balance := range alloc.Balances {
		account.Balances[tokenAddr] = &pb.TokenBalance{
			Balance:            balance.Balance,
			MinerLockedBalance: balance.MinerLockedBalance,
		}
	}
}

// initGenesisTokens 初始化创世代币到数据库
func initGenesisTokens(dbManager *db.Manager) error {
	if genesisConfig == nil {
		return nil
	}

	// 获取或创建 TokenRegistry
	registry := &pb.TokenRegistry{
		Tokens: make(map[string]*pb.Token),
	}

	for _, tokenCfg := range genesisConfig.Tokens {
		token := &pb.Token{
			Address:     tokenCfg.Address,
			Symbol:      tokenCfg.Symbol,
			Name:        tokenCfg.Name,
			TotalSupply: tokenCfg.TotalSupply,
			Owner:       tokenCfg.Owner,
			CanMint:     tokenCfg.CanMint,
		}

		// 保存 Token
		tokenData, err := proto.Marshal(token)
		if err != nil {
			return fmt.Errorf("failed to marshal token %s: %w", tokenCfg.Symbol, err)
		}
		tokenKey := keys.KeyToken(tokenCfg.Address)
		dbManager.EnqueueSet(tokenKey, string(tokenData))

		// 添加到 registry
		registry.Tokens[tokenCfg.Address] = token
	}

	// 保存 TokenRegistry
	registryData, err := proto.Marshal(registry)
	if err != nil {
		return fmt.Errorf("failed to marshal token registry: %w", err)
	}
	registryKey := keys.KeyTokenRegistry()
	dbManager.EnqueueSet(registryKey, string(registryData))

	return nil
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
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		fmt.Println("\n========== Progress Monitor ==========")
		fmt.Printf("Time: %s\n", time.Now().Format("15:04:05"))

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

		// 打印每个节点的完整统计信息
		fmt.Println("\nNode Statistics:")
		for i, node := range nodes {
			if node == nil || node.ConsensusManager == nil {
				fmt.Printf("Node %d: inactive\n", i)
				continue
			}

			stats := node.ConsensusManager.GetStats()
			if stats == nil {
				fmt.Printf("Node %d: no stats\n", i)
				continue
			}

			lastAccepted, height := node.ConsensusManager.GetLastAccepted()

			// 获取所有统计数据
			stats.Mu.Lock()
			fmt.Printf("\nNode %d:\n", i)
			fmt.Printf("  Last Accepted: %s (height=%d)\n", lastAccepted, height)
			fmt.Printf("  Queries Sent: %d\n", stats.QueriesSent)
			fmt.Printf("  Queries Received: %d\n", stats.QueriesReceived)
			fmt.Printf("  Chits Responded: %d\n", stats.ChitsResponded)
			fmt.Printf("  Blocks Proposed: %d\n", stats.BlocksProposed)
			fmt.Printf("  Gossips Received: %d\n", stats.GossipsReceived)
			fmt.Printf("  Snapshots Used: %d\n", stats.SnapshotsUsed)
			fmt.Printf("  Snapshots Served: %d\n", stats.SnapshotsServed)
			fmt.Printf("  GetPreferenceSwitchHistory: %+v\n", stats.GetPreferenceSwitchHistory())
			fmt.Printf("  Consensus API handler: %+v\n", node.HandlerManager.Stats.GetAPICallStats())
			rt, ok := node.ConsensusManager.Transport.(*consensus.RealTransport) // 安全断言
			if !ok {
				return
			}
			if rt == nil {
				return
			}
			fmt.Printf("  Consensus API send: %+v\n", rt.Stats.GetAPICallStats())
			// 显示每个高度的查询数
			if len(stats.QueriesPerHeight) > 0 {
				fmt.Printf("  Queries Per Height:\n")
				for h, count := range stats.QueriesPerHeight {
					fmt.Printf("    Height %d: %d\n", h, count)
				}
			}
			stats.Mu.Unlock()
		}

		fmt.Println()
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

			// 停止 FROST Runtime
			if n.FrostRuntime != nil {
				n.FrostRuntime.Stop()
			}

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

// --- Merged from tx_generators.go ---

// generateTxID 生成正确格式的十六进制 TxId (0x + 64位十六进制)
func generateTxID(counter uint64) string {
	return fmt.Sprintf("0x%016x%016x", time.Now().UnixNano(), counter)
}

// generateTransferTx 生成转账交易
func generateTransferTx(from, to, token, amount, fee string, nonce uint64) *pb.AnyTx {
	tx := &pb.Transaction{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: from,
			Fee:         fee,
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		To:           to,
		TokenAddress: token,
		Amount:       amount,
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_Transaction{Transaction: tx},
	}
}

// generateIssueTokenTx 生成发币交易
func generateIssueTokenTx(from, name, symbol, supply string, canMint bool, nonce uint64) *pb.AnyTx {
	tx := &pb.IssueTokenTx{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: from,
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		TokenName:   name,
		TokenSymbol: symbol,
		TotalSupply: supply,
		CanMint:     canMint,
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_IssueTokenTx{IssueTokenTx: tx},
	}
}

// generateMinerTx 生成矿工注册交易
func generateMinerTx(from string, op pb.OrderOp, amount string, nonce uint64) *pb.AnyTx {
	tx := &pb.MinerTx{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: from,
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		Op:     op,
		Amount: amount,
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_MinerTx{MinerTx: tx},
	}
}

// generateWitnessStakeTx 生成见证者质押交易
func generateWitnessStakeTx(from string, op pb.OrderOp, amount string, nonce uint64) *pb.AnyTx {
	tx := &pb.WitnessStakeTx{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: from,
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		Op:     op,
		Amount: amount,
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_WitnessStakeTx{WitnessStakeTx: tx},
	}
}

// generateWitnessRequestTx 生成上账请求交易
func generateWitnessRequestTx(from, chain, nativeHash, token, amount, receiver, fee string, nonce uint64) *pb.AnyTx {
	tx := &pb.WitnessRequestTx{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: from,
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		NativeChain:     chain,
		NativeTxHash:    nativeHash,
		TokenAddress:    token,
		Amount:          amount,
		ReceiverAddress: receiver,
		RechargeFee:     fee,
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_WitnessRequestTx{WitnessRequestTx: tx},
	}
}

// generateWitnessVoteTx 生成见证投票交易
func generateWitnessVoteTx(witness, requestID string, voteType pb.WitnessVoteType, nonce uint64) *pb.AnyTx {
	tx := &pb.WitnessVoteTx{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: witness,
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		Vote: &pb.WitnessVote{
			RequestId:      requestID,
			WitnessAddress: witness,
			VoteType:       voteType,
			Timestamp:      uint64(time.Now().Unix()),
		},
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_WitnessVoteTx{WitnessVoteTx: tx},
	}
}

// generateWithdrawRequestTx 生成提现请求交易
func generateWithdrawRequestTx(from, chain, asset, to, amount string, nonce uint64) *pb.AnyTx {
	tx := &pb.FrostWithdrawRequestTx{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: from,
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		Chain:  chain,
		Asset:  asset,
		To:     to,
		Amount: amount,
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_FrostWithdrawRequestTx{FrostWithdrawRequestTx: tx},
	}
}

// generateDkgCommitTx 生成 DKG 承诺交易
func generateDkgCommitTx(from, chain string, vaultID uint32, epochID uint64, algo pb.SignAlgo, nonce uint64) *pb.AnyTx {
	tx := &pb.FrostVaultDkgCommitTx{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: from,
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		Chain:            chain,
		VaultId:          vaultID,
		EpochId:          epochID,
		SignAlgo:         algo,
		CommitmentPoints: [][]byte{[]byte("point1"), []byte("point2")}, // 模拟数据
		AI0:              []byte("ai0"),
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_FrostVaultDkgCommitTx{FrostVaultDkgCommitTx: tx},
	}
}

// generateDkgShareTx 生成 DKG Share 交易
func generateDkgShareTx(from, to, chain string, vaultID uint32, epochID uint64, nonce uint64) *pb.AnyTx {
	tx := &pb.FrostVaultDkgShareTx{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: from,
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		Chain:      chain,
		VaultId:    vaultID,
		EpochId:    epochID,
		DealerId:   from,
		ReceiverId: to,
		Ciphertext: []byte("encrypted_share"),
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_FrostVaultDkgShareTx{FrostVaultDkgShareTx: tx},
	}
}

// --- Merged from tx_simulator.go ---

// TxSimulator 交易模拟器
type TxSimulator struct {
	nodes []*NodeInstance
}

func NewTxSimulator(nodes []*NodeInstance) *TxSimulator {
	return &TxSimulator{nodes: nodes}
}

func (s *TxSimulator) Start() {
	go s.runRandomTransfers()
	go s.runWitnessScenario()
	go s.runWithdrawScenario()
	// go s.runDkgScenario()
	// 注释掉 Injector，让 VM Handler 驱动状态变化
	// go s.runProtocolStateInjector() // 直接注入 Protocol 状态数据
	go s.runOrderScenario() // 订单交易模拟
}

func (s *TxSimulator) runDkgScenario() {
	// 约每 60 秒触发一次 DKG 模拟
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	nonceMap := make(map[string]uint64)

	for range ticker.C {
		logs.Info("Simulator: Starting DKG Scenario...")

		chain := "btc"
		vaultID := uint32(1)
		epochID := uint64(time.Now().Unix())
		algo := pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340

		// 1. Committing Phase
		for _, node := range s.nodes {
			nonceMap[node.Address]++
			tx := generateDkgCommitTx(node.Address, chain, vaultID, epochID, algo, nonceMap[node.Address])
			_ = node.TxPool.StoreAnyTx(tx)
		}

		time.Sleep(10 * time.Second)

		// 2. Sharing Phase (模拟部分 Share)
		for i := 0; i < 3 && i < len(s.nodes); i++ {
			fromNode := s.nodes[i]
			for j := 0; j < 3 && j < len(s.nodes); j++ {
				if i == j {
					continue
				}
				toNode := s.nodes[j]
				nonceMap[fromNode.Address]++
				tx := generateDkgShareTx(fromNode.Address, toNode.Address, chain, vaultID, epochID, nonceMap[fromNode.Address])
				_ = fromNode.TxPool.StoreAnyTx(tx)
			}
		}

		logs.Info("Simulator: DKG Scenario transactions sent for Epoch %d", epochID)
	}
}

func (s *TxSimulator) runRandomTransfers() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	nonceMap := make(map[string]uint64)

	for range ticker.C {
		// 随机选择一个发送方和接收方
		fromIdx := mrand.Intn(len(s.nodes))
		toIdx := mrand.Intn(len(s.nodes))
		fromNode := s.nodes[fromIdx]
		toNode := s.nodes[toIdx]

		if fromNode == nil || toNode == nil {
			continue
		}

		nonceMap[fromNode.Address]++
		nonce := nonceMap[fromNode.Address]

		tx := generateTransferTx(fromNode.Address, toNode.Address, "FB", "10", "1", nonce)

		if err := fromNode.TxPool.StoreAnyTx(tx); err == nil {
			logs.Trace("Simulator: Added random transfer %s from %s to %s", tx.GetBase().TxId, fromNode.Address, toNode.Address)
		}
	}
}

func (s *TxSimulator) runWitnessScenario() {
	// 周期性模拟完整的见证流程
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	nonceMap := make(map[string]uint64)
	witnessesRegistered := false

	for range ticker.C {
		logs.Info("Simulator: Starting Witness Scenario...")

		// 首先确保有见证者注册
		if !witnessesRegistered {
			logs.Info("Simulator: Registering witnesses...")
			// 让节点 1-4 注册成为见证者
			for i := 1; i < 5 && i < len(s.nodes); i++ {
				witnessNode := s.nodes[i]
				nonceMap[witnessNode.Address]++

				stakeTx := generateWitnessStakeTx(
					witnessNode.Address,
					pb.OrderOp_ADD,
					"1000000000000000000000", // 质押 1000 Token (18位小数)
					nonceMap[witnessNode.Address],
				)
				if err := witnessNode.TxPool.StoreAnyTx(stakeTx); err != nil {
					logs.Error("Simulator: Failed to submit witness stake tx: %v", err)
				} else {
					logs.Info("Simulator: Witness %s staked 10000 FB", witnessNode.Address)
				}
			}
			witnessesRegistered = true
			// 等待质押交易被打包
			time.Sleep(15 * time.Second)
			logs.Info("Simulator: Witnesses registered, continuing...")
			continue
		}

		// 1. 用户发起上账请求
		userNode := s.nodes[0]
		nonceMap[userNode.Address]++

		reqTx := generateWitnessRequestTx(
			userNode.Address,
			"btc",
			"tx_hash_"+time.Now().Format("150405"),
			"BTC",
			"500",
			userNode.Address,
			"5",
			nonceMap[userNode.Address],
		)

		reqID := reqTx.GetBase().TxId
		if err := userNode.TxPool.StoreAnyTx(reqTx); err != nil {
			continue
		}

		// 等待几秒让共识完成请求入链
		time.Sleep(10 * time.Second)

		// 2. 模拟见证者投票
		for i := 1; i < 5 && i < len(s.nodes); i++ {
			witnessNode := s.nodes[i]
			nonceMap[witnessNode.Address]++

			voteTx := generateWitnessVoteTx(
				witnessNode.Address,
				reqID,
				0, // VOTE_PASS
				nonceMap[witnessNode.Address],
			)
			_ = witnessNode.TxPool.StoreAnyTx(voteTx)
		}

		logs.Info("Simulator: Witness Scenario transactions sent for Request %s", reqID)
	}
}

func (s *TxSimulator) runWithdrawScenario() {
	ticker := time.NewTicker(45 * time.Second)
	defer ticker.Stop()

	nonceMap := make(map[string]uint64)

	for range ticker.C {
		logs.Info("Simulator: Starting Withdraw Scenario...")

		userNode := s.nodes[mrand.Intn(len(s.nodes))]
		if userNode == nil {
			continue
		}

		nonceMap[userNode.Address]++

		withdrawTx := generateWithdrawRequestTx(
			userNode.Address,
			"btc",
			"BTC",
			"bc1qtestaddress",
			"50",
			nonceMap[userNode.Address],
		)

		if err := userNode.TxPool.StoreAnyTx(withdrawTx); err == nil {
			logs.Info("Simulator: Withdraw Request %s sent", withdrawTx.GetBase().TxId)
		}
	}
}

// runProtocolStateInjector 直接注入 Protocol 状态数据到数据库
// 这确保 Explorer 的 Protocol 页面有数据可显示
func (s *TxSimulator) runProtocolStateInjector() {
	// 首次启动时等待节点初始化完成
	time.Sleep(5 * time.Second)

	// 初始注入一批数据
	s.injectProtocolStates()

	// 然后周期性更新
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.injectProtocolStates()
	}
}

// injectProtocolStates 注入各种 Protocol 状态
func (s *TxSimulator) injectProtocolStates() {
	if len(s.nodes) == 0 || s.nodes[0] == nil || s.nodes[0].DBManager == nil {
		return
	}

	node := s.nodes[0]
	now := time.Now().Unix()

	logs.Info("Simulator: Injecting Protocol states...")

	// 1. 注入 Withdraw 状态
	s.injectWithdrawStates(node, now)

	// 2. 注入 Recharge Request 状态
	s.injectRechargeRequests(node, now)

	// 3. 注入 DKG/Vault Transition 状态
	s.injectDkgTransitions(node, now)

	node.DBManager.ForceFlush()
	logs.Info("Simulator: Protocol states injected successfully")
}

func (s *TxSimulator) injectWithdrawStates(node *NodeInstance, timestamp int64) {
	chains := []string{"btc", "eth", "sol"}
	assets := []string{"BTC", "USDT", "SOL"}
	statuses := []string{"QUEUED", "SIGNED", "QUEUED", "QUEUED"}

	for i := 0; i < 8; i++ {
		withdrawID := fmt.Sprintf("wd_%d_%d", timestamp, i)
		chain := chains[i%len(chains)]
		asset := assets[i%len(assets)]
		status := statuses[i%len(statuses)]

		state := &pb.FrostWithdrawState{
			WithdrawId:    withdrawID,
			TxId:          fmt.Sprintf("0x%016x%04d", timestamp, i),
			Seq:           uint64(i + 1),
			Chain:         chain,
			Asset:         asset,
			To:            fmt.Sprintf("0x%040d", i+1),
			Amount:        fmt.Sprintf("%d00000000", (i+1)*10), // 10, 20, 30...
			RequestHeight: uint64(1000 + i*10),
			Status:        status,
			VaultId:       uint32(i % 3),
		}

		if status == "SIGNED" {
			state.JobId = fmt.Sprintf("job_%d_%d", timestamp, i)
		}

		data, _ := proto.Marshal(state)
		key := keys.KeyFrostWithdraw(withdrawID)
		node.DBManager.EnqueueSet(key, string(data))
	}
}

func (s *TxSimulator) injectRechargeRequests(node *NodeInstance, timestamp int64) {
	chains := []string{"btc", "eth", "sol"}
	statuses := []pb.RechargeRequestStatus{
		pb.RechargeRequestStatus_RECHARGE_PENDING,
		pb.RechargeRequestStatus_RECHARGE_VOTING,
		pb.RechargeRequestStatus_RECHARGE_VOTING,
		pb.RechargeRequestStatus_RECHARGE_CHALLENGE_PERIOD,
		pb.RechargeRequestStatus_RECHARGE_FINALIZED,
	}

	for i := 0; i < 6; i++ {
		requestID := fmt.Sprintf("req_%d_%d", timestamp, i)
		chain := chains[i%len(chains)]
		status := statuses[i%len(statuses)]

		// 生成模拟见证者
		witnesses := make([]string, 3)
		for j := 0; j < 3; j++ {
			if j < len(s.nodes) && s.nodes[j] != nil {
				witnesses[j] = s.nodes[j].Address
			} else {
				witnesses[j] = fmt.Sprintf("0x%040d", j+100)
			}
		}

		// 生成模拟投票
		var votes []*pb.WitnessVote
		if status >= pb.RechargeRequestStatus_RECHARGE_VOTING {
			for j := 0; j < 2 && j < len(witnesses); j++ {
				votes = append(votes, &pb.WitnessVote{
					RequestId:      requestID,
					WitnessAddress: witnesses[j],
					VoteType:       pb.WitnessVoteType_VOTE_PASS,
					Timestamp:      uint64(timestamp) + uint64(j),
				})
			}
		}

		req := &pb.RechargeRequest{
			RequestId:         requestID,
			NativeChain:       chain,
			NativeTxHash:      fmt.Sprintf("0x%064d", timestamp+int64(i)),
			TokenAddress:      "FB",
			Amount:            fmt.Sprintf("%d00000000", (i+1)*50),
			ReceiverAddress:   witnesses[0],
			RequesterAddress:  witnesses[0],
			Status:            status,
			CreateHeight:      uint64(1000 + i*10),
			DeadlineHeight:    uint64(1100 + i*10),
			Round:             1,
			SelectedWitnesses: witnesses,
			Votes:             votes,
			PassCount:         uint32(len(votes)),
			VaultId:           uint32(i % 3),
		}

		data, _ := proto.Marshal(req)
		key := keys.KeyRechargeRequest(requestID)
		node.DBManager.EnqueueSet(key, string(data))
	}
}

func (s *TxSimulator) injectDkgTransitions(node *NodeInstance, timestamp int64) {
	chains := []string{"btc", "eth", "sol"}
	dkgStatuses := []string{"COMMITTING", "SHARING", "KEY_READY", "COMMITTING"}

	for i := 0; i < 4; i++ {
		chain := chains[i%len(chains)]
		vaultID := uint32(i)
		epochID := uint64(timestamp/1000 + int64(i))
		dkgStatus := dkgStatuses[i%len(dkgStatuses)]

		// 使用真实节点地址作为委员会成员
		members := make([]string, 0)
		for j := 0; j < 5 && j < len(s.nodes); j++ {
			if s.nodes[j] != nil {
				members = append(members, s.nodes[j].Address)
			}
		}

		state := &pb.VaultTransitionState{
			Chain:               chain,
			VaultId:             vaultID,
			EpochId:             epochID,
			SignAlgo:            pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
			TriggerHeight:       uint64(900 + i*100),
			OldCommitteeMembers: members,
			NewCommitteeMembers: members,
			DkgStatus:           dkgStatus,
			DkgSessionId:        fmt.Sprintf("dkg_%s_%d_%d", chain, vaultID, epochID),
			DkgThresholdT:       3,
			DkgN:                uint32(len(members)),
			DkgCommitDeadline:   uint64(950 + i*100),
			DkgDisputeDeadline:  uint64(980 + i*100),
			ValidationStatus:    "NOT_STARTED",
			Lifecycle:           "ACTIVE",
		}

		if dkgStatus == "KEY_READY" {
			state.NewGroupPubkey = []byte(fmt.Sprintf("pubkey_%d_%d", timestamp, i))
			state.ValidationStatus = "PASSED"
		}

		data, _ := proto.Marshal(state)
		key := keys.KeyFrostVaultTransition(chain, vaultID, epochID)
		node.DBManager.EnqueueSet(key, string(data))
	}
}

// runOrderScenario 模拟订单交易（买单/卖单）
// 现在节点有 FB 和 USDT 余额（通过 genesis.json 配置），可以双向交易
// - 卖单：用 FB 换 USDT（base_token=FB, quote_token=USDT）
// - 买单：用 USDT 换 FB（base_token=USDT, quote_token=FB）
func (s *TxSimulator) runOrderScenario() {
	// 延迟启动，等待账户有足够余额
	time.Sleep(5 * time.Second)

	// 周期性生成新订单（每 2 秒生成一笔订单）
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	nonceMap := make(map[string]uint64)

	for range ticker.C {
		if len(s.nodes) == 0 {
			continue
		}

		// 每次生成 5-10 笔订单
		orderCount := 5 + mrand.Intn(6)
		for i := 0; i < orderCount; i++ {
			// 随机选择一个节点
			nodeIdx := mrand.Intn(len(s.nodes))
			node := s.nodes[nodeIdx]
			if node == nil {
				continue
			}

			nonceMap[node.Address]++

			// 随机价格和数量（使用较小的数量，避免余额不足）
			basePrice := 1.0 + float64(mrand.Intn(10))*0.1
			amount := 1.0 + float64(mrand.Intn(5))

			// 随机决定买单还是卖单
			isBuyOrder := mrand.Intn(2) == 0

			var tx *pb.AnyTx
			if isBuyOrder {
				// 买单：用 USDT 买 FB
				// base_token=FB (想买的), quote_token=USDT (支付的)
				tx = generateOrderTx(
					node.Address,
					"FB",                           // base_token - 想要买入的代币
					"USDT",                         // quote_token - 用于支付的代币
					fmt.Sprintf("%.2f", amount),    // 想买入的 FB 数量
					fmt.Sprintf("%.2f", basePrice), // 每个 FB 的价格（以 USDT 计）
					nonceMap[node.Address],
					pb.OrderSide_BUY,
				)
				logs.Trace("Simulator: Added BUY order %s from %s, buy %.2f FB @ %.2f USDT",
					tx.GetBase().TxId, node.Address, amount, basePrice)
			} else {
				// 卖单：卖 FB 换 USDT
				// base_token=FB (要卖的), quote_token=USDT (想要获得的)
				tx = generateOrderTx(
					node.Address,
					"FB",                           // base_token - 要卖出的代币
					"USDT",                         // quote_token - 想要获得的代币
					fmt.Sprintf("%.2f", amount),    // 要卖出的 FB 数量
					fmt.Sprintf("%.2f", basePrice), // 每个 FB 的价格（以 USDT 计）
					nonceMap[node.Address],
					pb.OrderSide_SELL,
				)
				logs.Trace("Simulator: Added SELL order %s from %s, sell %.2f FB @ %.2f USDT",
					tx.GetBase().TxId, node.Address, amount, basePrice)
			}

			if err := node.TxPool.StoreAnyTx(tx); err != nil {
				logs.Error("Simulator: Failed to add order: %v", err)
			}
		}
	}
}

// generateOrderTx 生成订单交易
func generateOrderTx(from, baseToken, quoteToken, amount, price string, nonce uint64, side pb.OrderSide) *pb.AnyTx {
	tx := &pb.OrderTx{
		Base: &pb.BaseMessage{
			TxId:        generateTxID(nonce),
			FromAddress: from,
			Fee:         "1",
			Status:      pb.Status_PENDING,
			Nonce:       nonce,
		},
		BaseToken:  baseToken,
		QuoteToken: quoteToken,
		Op:         pb.OrderOp_ADD,
		Amount:     amount,
		Price:      price,
		Side:       side,
		// 注意: FilledBase, FilledQuote, IsFilled 已移至 OrderState
	}
	return &pb.AnyTx{
		Content: &pb.AnyTx_OrderTx{OrderTx: tx},
	}
}
