package main

import (
	"dex/app"
	"dex/config"
	"dex/consensus"
	"dex/db"
	"dex/execution"
	"dex/interfaces"
	"dex/logs"
	"dex/network"
	"dex/txpool"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	// 1. 解析命令行参数
	var (
		dataPath   = flag.String("data", "./data", "database directory")
		port       = flag.Int("port", 8080, "server port")
		runMode    = flag.String("mode", "full", "run mode: full|consensus-only")
		configFile = flag.String("config", "", "config file path")
	)
	flag.Parse()

	// 2. 加载配置
	cfg := loadConfig(*dataPath, *port, *configFile)

	// 3. 根据运行模式启动
	switch *runMode {
	case "consensus-only":
		runConsensusOnly(cfg)
	case "full":
		runFullNode(cfg)
	default:
		logs.Error("Unknown run mode: %s", *runMode)
		os.Exit(1)
	}
}

// loadConfig 加载配置
func loadConfig(dataPath string, port int, configFile string) *config.Config {
	cfg := &config.Config{
		DataPath: dataPath,
		Port:     port,
		DB: config.DBConfig{
			Path:           dataPath,
			WriteQueueSize: 1000,
			FlushInterval:  5 * time.Second,
		},
		TxPool: config.TxPoolConfig{
			MaxPendingTxs:    100000,
			MaxShortCacheTxs: 100000,
			SyncInterval:     3 * time.Second,
			QueueSize:        10000,
		},
		Network: config.NetworkConfig{
			MaxPeers:         100,
			HandshakeTimeout: 30 * time.Second,
		},
		Consensus: config.ConsensusConfig{
			BlockInterval: 10 * time.Second,
			MaxTxPerBlock: 2500,
			UseSnowman:    false,
		},
	}

	// 如果提供了配置文件，可以从文件加载并覆盖默认值
	if configFile != "" {
		logs.Info("Loading config from file: %s", configFile)
	}

	return cfg
}

// waitForShutdown 等待关闭信号
func waitForShutdown(application *app.App) {
	// 创建信号通道
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// 等待信号
	sig := <-sigChan
	logs.Info("Received signal: %v, shutting down...", sig)

	// 优雅关闭
	application.Stop()
}

// 创建API服务器（占位实现）
func NewAPIServer(container *app.Container) interface{} {
	return &struct{ Name string }{Name: "API Server"}
}

// 创建RPC服务器（占位实现）
func NewRPCServer(container *app.Container) interface{} {
	return &struct{ Name string }{Name: "RPC Server"}
}

// 仅运行共识模块（用于测试）
func runConsensusOnly(cfg *config.Config) {
	logs.Info("Starting in consensus-only mode")
	consensus.RunLoop()
}

// 运行完整节点
func runFullNode(cfg *config.Config) {
	logs.Info("Starting full node")

	// 创建依赖注入容器
	container := app.NewContainer(cfg)

	// 初始化所有模块
	if err := initializeModules(container); err != nil {
		logs.Error("Failed to initialize: %v", err)
		os.Exit(1)
	}

	// 创建应用实例
	application := app.NewApp(container)

	// 启动应用
	if err := application.Start(); err != nil {
		logs.Error("Failed to start: %v", err)
		os.Exit(1)
	}

	// 等待退出信号
	waitForShutdown(application)
}

func initializeModules(container *app.Container) error {
	// 1. 数据库层（最底层）
	dbManager, err := db.NewManager(container.Config.DataPath)
	if err != nil {
		return fmt.Errorf("db init failed: %w", err)
	}
	dbManager.InitWriteQueue(
		container.Config.DB.WriteQueueSize,
		container.Config.DB.FlushInterval,
	)
	container.DB = interfaces.DBManager(dbManager) // 显式转换为接口类型

	// 2. 网络层（独立模块）
	networkMgr, err := network.NewNetwork(container.Config.Network, dbManager)
	if err != nil {
		return fmt.Errorf("network init failed: %w", err)
	}
	container.Network = interfaces.Network(networkMgr) // 显式转换为接口类型

	// 3. 共识层（可以独立运行）
	var consensusMgr interfaces.Consensus
	if container.Config.Consensus.UseSnowman {
		// 使用已经测试好的 Snowman 共识
		consensusMgr = consensus.NewSnowmanConsensus(
			container.Config.Consensus,
			dbManager,
			networkMgr,
		)
	} else {
		// 其他共识实现
		consensusMgr = &consensus.SimpleConsensus{}
		consensusMgr = consensus.NewSimpleConsensus(
			container.Config.Consensus,
			dbManager,
		)
	}
	container.Consensus = consensusMgr

	// 4. 交易池（依赖共识层做验证）
	// 将 config.TxPoolConfig 转换为 txpool.Config
	txPoolCfg := &txpool.Config{
		MaxPendingTxs:    container.Config.TxPool.MaxPendingTxs,
		MaxShortCacheTxs: container.Config.TxPool.MaxShortCacheTxs,
		SyncInterval:     container.Config.TxPool.SyncInterval,
		EnableAutoLoad:   true,
	}

	// 创建网络管理器适配器
	networkAdapter := &NetworkManagerAdapter{network: networkMgr}

	// 创建验证器适配器
	validatorAdapter := &TxValidatorAdapter{consensus: consensusMgr}

	txPoolMgr, err := txpool.NewTxPool(
		txPoolCfg,
		dbManager,
		networkAdapter,
		validatorAdapter,
	)
	if err != nil {
		return fmt.Errorf("txpool init failed: %w", err)
	}
	container.TxPool = interfaces.TxPool(txPoolMgr) // 显式转换为接口类型

	// 5. 执行层（依赖数据库）
	executorMgr := execution.NewExecutor(dbManager)
	container.Executor = interfaces.Executor(executorMgr) // 显式转换为接口类型

	// 6. 注册其他服务到容器
	container.Register("api", NewAPIServer(container))
	container.Register("rpc", NewRPCServer(container))

	return nil
}

// NetworkManagerAdapter 适配器，将 interfaces.Network 转换为 txpool.NetworkManager
type NetworkManagerAdapter struct {
	network interfaces.Network
}

func (n *NetworkManagerAdapter) IsKnownNode(pubKey string) bool {
	return n.network.IsKnownNode(pubKey)
}

func (n *NetworkManagerAdapter) AddOrUpdateNode(pubKey, ip string, isKnown bool) {
	// 将 isKnown 参数转换为 isOnline
	// 这里假设 known 节点就是 online 节点
	n.network.AddOrUpdateNode(pubKey, ip, isKnown)
}

// TxValidatorAdapter 适配器，将 interfaces.Consensus 转换为 txpool.TxValidator
type TxValidatorAdapter struct {
	consensus interfaces.Consensus
}

func (v *TxValidatorAdapter) CheckAnyTx(tx *db.AnyTx) error {
	return v.consensus.CheckAnyTx(tx)
}
