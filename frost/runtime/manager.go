package runtime

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
)

// ManagerConfig 管理器配置
type ManagerConfig struct {
	// NodeID 本节点 ID
	NodeID NodeID
}

// Manager Runtime 主入口与生命周期管理
type Manager struct {
	config ManagerConfig

	// 依赖注入
	stateReader    ChainStateReader
	txSubmitter    TxSubmitter
	notifier       FinalityNotifier
	p2p            P2P
	signerProvider SignerSetProvider
	vaultProvider  VaultCommitteeProvider
	adapterFactory ChainAdapterFactory

	// 状态
	running  atomic.Bool
	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup

	// 最新 finalized 高度
	lastFinalizedHeight atomic.Uint64

	// 回调（用于测试）
	onBlockFinalized func(height uint64)
}

// ManagerDeps Manager 依赖集合
type ManagerDeps struct {
	StateReader    ChainStateReader
	TxSubmitter    TxSubmitter
	Notifier       FinalityNotifier
	P2P            P2P
	SignerProvider SignerSetProvider
	VaultProvider  VaultCommitteeProvider
	AdapterFactory ChainAdapterFactory
}

// NewManager 创建新的 Manager 实例
func NewManager(config ManagerConfig, deps ManagerDeps) *Manager {
	return &Manager{
		config:         config,
		stateReader:    deps.StateReader,
		txSubmitter:    deps.TxSubmitter,
		notifier:       deps.Notifier,
		p2p:            deps.P2P,
		signerProvider: deps.SignerProvider,
		vaultProvider:  deps.VaultProvider,
		adapterFactory: deps.AdapterFactory,
		stopCh:         make(chan struct{}),
	}
}

// Start 启动 Manager
func (m *Manager) Start(ctx context.Context) error {
	if m.running.Swap(true) {
		return nil // 已经在运行
	}

	log.Printf("[FrostManager] Starting with NodeID: %s", m.config.NodeID)

	// 订阅 finalized 事件
	if m.notifier != nil {
		m.notifier.SubscribeBlockFinalized(m.handleBlockFinalized)
	}

	// 启动主循环
	m.wg.Add(1)
	go m.runLoop(ctx)

	log.Printf("[FrostManager] Started")
	return nil
}

// Stop 停止 Manager
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		log.Printf("[FrostManager] Stopping...")
		m.running.Store(false)
		close(m.stopCh)
		m.wg.Wait()
		log.Printf("[FrostManager] Stopped")
	})
}

// IsRunning 返回 Manager 是否正在运行
func (m *Manager) IsRunning() bool {
	return m.running.Load()
}

// LastFinalizedHeight 返回最新 finalized 高度
func (m *Manager) LastFinalizedHeight() uint64 {
	return m.lastFinalizedHeight.Load()
}

// SetOnBlockFinalized 设置 finalized 回调（用于测试）
func (m *Manager) SetOnBlockFinalized(fn func(height uint64)) {
	m.onBlockFinalized = fn
}

// handleBlockFinalized 处理 finalized 事件
func (m *Manager) handleBlockFinalized(height uint64) {
	if !m.running.Load() {
		return
	}

	m.lastFinalizedHeight.Store(height)
	log.Printf("[FrostManager] Block finalized: height=%d", height)

	// 调用测试回调
	if m.onBlockFinalized != nil {
		m.onBlockFinalized(height)
	}
}

// runLoop 主循环
func (m *Manager) runLoop(ctx context.Context) {
	defer m.wg.Done()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[FrostManager] Context cancelled")
			return
		case <-m.stopCh:
			return
		}
	}
}
