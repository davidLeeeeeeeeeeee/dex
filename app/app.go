// app/app.go
package app

import (
	"context"
	"dex/config"
	"dex/interfaces"
	"dex/logs"
	"fmt"
	"sync"
)

// Container 依赖注入容器
type Container struct {
	Config    *config.Config
	DB        interfaces.DBManager
	TxPool    interfaces.TxPool
	Network   interfaces.Network
	Executor  interfaces.Executor
	Consensus interfaces.Consensus

	// 其他服务
	services map[string]interface{}
	mu       sync.RWMutex
}

// NewContainer 创建新的依赖容器
func NewContainer(cfg *config.Config) *Container {
	return &Container{
		Config:   cfg,
		services: make(map[string]interface{}),
	}
}

// Register 注册服务
func (c *Container) Register(name string, service interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.services[name] = service
}

// Get 获取服务
func (c *Container) Get(name string) interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.services[name]
}

// App 主应用结构
type App struct {
	container *Container
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// NewApp 创建应用实例
func NewApp(container *Container) *App {
	ctx, cancel := context.WithCancel(context.Background())
	return &App{
		container: container,
		ctx:       ctx,
		cancel:    cancel,
	}
}

func (a *App) Start() error {
	// 按依赖顺序启动服务
	startOrder := []string{
		"db",        // 数据库服务
		"network",   // 网络服务
		"consensus", // 共识服务
		"txpool",    // 交易池
		"executor",  // 执行器
		"api",       // API服务
		"rpc",       // RPC服务
	}

	for _, serviceName := range startOrder {
		if err := a.startService(serviceName); err != nil {
			return fmt.Errorf("failed to start %s: %w", serviceName, err)
		}
		logs.Info("Service %s started", serviceName)
	}

	return nil
}

func (a *App) startService(name string) error {
	switch name {
	case "network":
		return a.container.Network.Start()
	case "consensus":
		// 在独立的 goroutine 中运行共识
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			a.container.Consensus.Run(a.ctx)
		}()
	case "txpool":
		return a.container.TxPool.Start()
		// ... 其他服务
	}
	return nil
}

// Stop 停止应用
func (a *App) Stop() {
	a.cancel()
	a.wg.Wait()
}

// startServices 初始化并启动所有服务
func (a *App) startServices() error {
	// 这里会调用各个模块的启动方法
	// 例如：加载数据库、初始化网络连接等
	return nil
}

// runHTTPServer 运行HTTP服务器
func (a *App) runHTTPServer() {
	defer a.wg.Done()
	// HTTP服务器逻辑
}

// runConsensusLoop 运行共识循环
func (a *App) runConsensusLoop() {
	defer a.wg.Done()
	// 共识循环逻辑
}

// GetContainer 获取容器（用于测试或特殊场景）
func (a *App) GetContainer() *Container {
	return a.container
}
