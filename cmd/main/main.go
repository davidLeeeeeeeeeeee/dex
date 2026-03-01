package main

import (
	"context"
	"dex/config"
	"dex/frost/chain"
	"dex/frost/chain/btc"
	"dex/frost/chain/evm"
	"dex/frost/chain/solana"
	frostrt "dex/frost/runtime"
	"dex/frost/runtime/adapters"
	"dex/frost/runtime/committee"
	"dex/logs"
	"dex/pb"
	"dex/types"
	"dex/utils"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"google.golang.org/protobuf/proto"
)

var (
	// 全局创世配置
	genesisConfig *config.GenesisConfig
	// 恢复开关：false=每次清理 data 从 0 开始；true=尝试复用已有数据继续模拟
	resumeFromExistingProgress = true
	// Queue stats monitor is noisy; enable only when explicitly requested.
	enableQueueStatsMonitor = os.Getenv("DEX_MONITOR_QUEUE_STATS") == "1"
)

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

		// 根据开关决定是否清理旧数据
		if !resumeFromExistingProgress {
			os.RemoveAll(node.DataPath)
		}

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

	// 第二阶段：注册节点（让节点互相知道）
	shouldBootstrap := true
	if resumeFromExistingProgress {
		hasPersistedProgress, maxHeight := detectPersistedProgress(nodes)
		if hasPersistedProgress {
			shouldBootstrap = false
			fmt.Printf("♻️  Resume mode enabled: detected persisted progress (max height=%d), skip bootstrap writes.\n", maxHeight)
			registerNodeLogMappings(nodes)
		} else {
			fmt.Println("⚠️  Resume mode enabled but no persisted progress detected, fallback to fresh bootstrap.")
		}
	}
	if shouldBootstrap {
		fmt.Println("🔗 Phase 2: Registering all nodes...")
		registerAllNodes(nodes, cfg.Frost)
	} else {
		fmt.Println("🔗 Phase 2: Reusing existing node/account state from local data")
	}

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
				StateReader:     stateReader,
				TxSubmitter:     txSubmitter,
				Notifier:        notifier,
				P2P:             p2p,
				RoastMessenger:  roastMessenger,
				SignerProvider:  signerProvider,
				VaultProvider:   vaultProvider,
				AdapterFactory:  adapterFactory,
				PubKeyProvider:  pubKeyProvider,
				CryptoFactory:   cryptoFactory,
				LocalShareStore: adapters.NewDBLocalShareStore(node.DBManager),
				LocalPrivateKey: node.PrivateKey,
				LogReporter:     adapters.NewLocalLogReporter(node.DBManager),
				Logger:          node.Logger, // 修复：注入节点日志器，防止 Start 时出现空指针 Panic
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
			// 在开启查询前，重置提案计时器，确保高度 1 从 Window 0 (5% 概率) 开始，避免初始冲突
			node.ConsensusManager.ResetProposalTimer()

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
	// 队列状态监控（每 10s 打印一次）
	if enableQueueStatsMonitor {
		go monitorQueueStats(nodes)
	}
	// 监控进度
	go monitorProgress(nodes)
	go monitorMinerParticipantsByEpoch(nodes)

	// 等待信号退出
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	fmt.Println("\n✅ All nodes started! Press Ctrl+C to stop...")
	fmt.Println("📊 Monitoring consensus progress...")

	sig := <-sigChan

	// 优雅关闭
	fmt.Printf("\n🛑 Received signal %s, shutting down all nodes...\n", sig)
	shutdownAllNodes(nodes)

	wg.Wait()
	fmt.Println("👋 All nodes stopped. Goodbye!")
}

func detectPersistedProgress(nodes []*NodeInstance) (bool, uint64) {
	var maxHeight uint64
	for _, node := range nodes {
		if node == nil || node.ConsensusManager == nil {
			continue
		}
		_, height := node.ConsensusManager.GetLastAccepted()
		if height > maxHeight {
			maxHeight = height
		}
	}
	return maxHeight > 0, maxHeight
}

func registerNodeLogMappings(nodes []*NodeInstance) {
	for _, node := range nodes {
		if node == nil {
			continue
		}
		logs.RegisterNodeMapping(strconv.Itoa(node.ID), node.Address)
		logs.RegisterNodeMapping(node.Port, node.Address)
		logs.RegisterNodeMapping(fmt.Sprintf("127.0.0.1:%s", node.Port), node.Address)
		logs.RegisterNodeMapping(node.Address, node.Address)
	}
}
