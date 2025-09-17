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
			UseSnowman:    false, // 添加这个字段到 ConsensusConfig
		},
	}

	// 如果提供了配置文件，可以从文件加载并覆盖默认值
	if configFile != "" {
		// TODO: 实现从文件加载配置
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
	container.DB = dbManager

	// 2. 网络层（独立模块）
	networkMgr, _ := network.NewNetwork(container.Config.Network, dbManager)
	container.Network = networkMgr

	// 3. 共识层（可以独立运行）
	var consensusMgr interfaces.Consensus
	if container.Config.Consensus.UseSnowman {
		// 使用你已经测试好的 Snowman 共识
		consensusMgr = consensus.NewSnowmanConsensus(
			container.Config.Consensus,
			dbManager,
			networkMgr,
		)
	} else {
		// 其他共识实现
		consensusMgr = consensus.NewSimpleConsensus(
			container.Config.Consensus,
			dbManager,
		)
	}
	container.Consensus = consensusMgr

	// 4. 交易池（依赖共识层做验证）
	txPoolMgr, err := txpool.NewTxPool(
		&container.Config.TxPool,
		dbManager,
		networkMgr,
		consensusMgr, // 作为 validator
	)
	if err != nil {
		return fmt.Errorf("txpool init failed: %w", err)
	}
	container.TxPool = txPoolMgr

	// 5. 执行层（依赖数据库）
	executorMgr := execution.NewExecutor(dbManager)
	container.Executor = executorMgr

	// 6. 注册其他服务到容器
	container.Register("api", NewAPIServer(container))
	container.Register("rpc", NewRPCServer(container))

	return nil
}
