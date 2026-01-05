package runtime

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"dex/frost/chain"
	frostrtnet "dex/frost/runtime/net"
	"dex/frost/runtime/session"
	"dex/pb"
)

// ManagerConfig 管理器配置
type ManagerConfig struct {
	// NodeID 本节点 ID
	NodeID NodeID

	// 扫描间隔
	ScanInterval time.Duration

	// 支持的链和资产
	SupportedChains []ChainAssetPair
}

// ChainAssetPair 链资产对
type ChainAssetPair struct {
	Chain string
	Asset string
}

// DefaultManagerConfig 默认配置
func DefaultManagerConfig(nodeID NodeID) ManagerConfig {
	return ManagerConfig{
		NodeID:       nodeID,
		ScanInterval: 5 * time.Second,
		SupportedChains: []ChainAssetPair{
			{Chain: "btc", Asset: "BTC"},
			{Chain: "eth", Asset: "ETH"},
			{Chain: "bnb", Asset: "BNB"},
		},
	}
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
	adapterFactory chain.ChainAdapterFactory

	// 子组件
	scanner          *Scanner
	withdrawWorker   *WithdrawWorker
	transitionWorker *TransitionWorker
	coordinator      *Coordinator
	participant      *Participant
	roastDispatcher  *RoastDispatcher
	frostRouter      *frostrtnet.Router
	sessionStore     *session.SessionStore

	// 状态
	running  atomic.Bool
	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup

	// 最新 finalized 高度
	lastFinalizedHeight atomic.Uint64

	// 待处理的 finalized 高度队列
	finalizedCh chan uint64

	// 回调（用于测试）
	onBlockFinalized func(height uint64)
}

// ManagerDeps Manager 依赖集合
type ManagerDeps struct {
	StateReader    ChainStateReader
	TxSubmitter    TxSubmitter
	Notifier       FinalityNotifier
	P2P            P2P
	RoastMessenger RoastMessenger
	FrostRouter    *frostrtnet.Router
	SignerProvider SignerSetProvider
	VaultProvider  VaultCommitteeProvider
	AdapterFactory chain.ChainAdapterFactory
	PubKeyProvider MinerPubKeyProvider
	CryptoFactory  CryptoExecutorFactory // 密码学执行器工厂
}

// NewManager 创建新的 Manager 实例
func NewManager(config ManagerConfig, deps ManagerDeps) *Manager {
	sessionStore := session.NewSessionStore(nil)

	m := &Manager{
		config:         config,
		stateReader:    deps.StateReader,
		txSubmitter:    deps.TxSubmitter,
		notifier:       deps.Notifier,
		p2p:            deps.P2P,
		signerProvider: deps.SignerProvider,
		vaultProvider:  deps.VaultProvider,
		adapterFactory: deps.AdapterFactory,
		sessionStore:   sessionStore,
		stopCh:         make(chan struct{}),
		finalizedCh:    make(chan uint64, 100),
	}

	roastMessenger := deps.RoastMessenger
	if roastMessenger == nil && deps.P2P != nil {
		roastMessenger = NewP2PRoastMessenger(deps.P2P)
	}
	frostRouter := deps.FrostRouter
	if frostRouter == nil {
		frostRouter = frostrtnet.NewRouter()
		frostrtnet.RegisterDefaultHandlers(frostRouter)
	}

	// 初始化子组件
	m.scanner = NewScanner(deps.StateReader)
	m.withdrawWorker = NewWithdrawWorker(deps.StateReader, deps.AdapterFactory, deps.TxSubmitter)
	m.transitionWorker = NewTransitionWorker(deps.StateReader, deps.TxSubmitter, deps.PubKeyProvider, deps.CryptoFactory, string(config.NodeID))
	m.coordinator = NewCoordinator(config.NodeID, roastMessenger, deps.VaultProvider, deps.CryptoFactory, nil)
	m.participant = NewParticipant(config.NodeID, roastMessenger, deps.VaultProvider, deps.CryptoFactory, sessionStore, nil)
	m.roastDispatcher = NewRoastDispatcher(m.coordinator, m.participant)
	m.frostRouter = frostRouter

	return m
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

	// 发送到处理队列
	select {
	case m.finalizedCh <- height:
	default:
		log.Printf("[FrostManager] finalized channel full, dropping height=%d", height)
	}

	// 调用测试回调
	if m.onBlockFinalized != nil {
		m.onBlockFinalized(height)
	}
}

// runLoop 主循环
func (m *Manager) runLoop(ctx context.Context) {
	defer m.wg.Done()

	// 定时扫描 ticker
	scanInterval := m.config.ScanInterval
	if scanInterval <= 0 {
		scanInterval = 5 * time.Second // 默认 5 秒
	}
	scanTicker := time.NewTicker(scanInterval)
	defer scanTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[FrostManager] Context cancelled")
			return
		case <-m.stopCh:
			return
		case height := <-m.finalizedCh:
			m.onFinalized(ctx, height)
		case <-scanTicker.C:
			m.scanAndProcess(ctx)
		}
	}
}

// onFinalized 处理 finalized 事件
func (m *Manager) onFinalized(ctx context.Context, height uint64) {
	log.Printf("[FrostManager] Processing finalized block: height=%d", height)

	// 检查是否有需要启动的 DKG 会话
	// TODO: 从链上读取 DKG 触发条件
}

// scanAndProcess 扫描并处理提现请求
func (m *Manager) scanAndProcess(ctx context.Context) {
	for _, pair := range m.config.SupportedChains {
		m.processChainAsset(ctx, pair.Chain, pair.Asset)
	}
}

// processChainAsset 处理单个链资产对
func (m *Manager) processChainAsset(ctx context.Context, chainName, asset string) {
	// 扫描队首 QUEUED withdraw
	scanResult, err := m.scanner.ScanOnce(chainName, asset)
	if err != nil {
		log.Printf("[FrostManager] Scan error for %s/%s: %v", chainName, asset, err)
		return
	}

	if scanResult == nil {
		// 没有待处理的 withdraw
		return
	}

	log.Printf("[FrostManager] Found pending withdraw: chain=%s, asset=%s, id=%s, seq=%d",
		chainName, asset, scanResult.WithdrawID, scanResult.Seq)

	// 处理提现
	job, err := m.withdrawWorker.ProcessOnce(chainName, asset)
	if err != nil {
		log.Printf("[FrostManager] ProcessOnce error: %v", err)
		return
	}

	if job != nil {
		log.Printf("[FrostManager] Created job: id=%s, withdraws=%v", job.JobID, job.WithdrawIDs)
	}
}

// GetCoordinator 获取协调者（用于测试）
func (m *Manager) GetCoordinator() *Coordinator {
	return m.coordinator
}

// GetParticipant 获取参与者（用于测试）
func (m *Manager) GetParticipant() *Participant {
	return m.participant
}

// HandleRoastEnvelope routes a ROAST message to coordinator/participant.
func (m *Manager) HandleRoastEnvelope(env *RoastEnvelope) error {
	if m.roastDispatcher == nil {
		return nil
	}
	return m.roastDispatcher.Handle(env)
}

// HandleFrostEnvelope routes a transport envelope to ROAST handlers.
func (m *Manager) HandleFrostEnvelope(env *FrostEnvelope) error {
	if m.roastDispatcher == nil {
		return nil
	}
	return m.roastDispatcher.HandleFrostEnvelope(env)
}

// HandlePBEnvelope routes a protobuf envelope to ROAST or other handlers.
func (m *Manager) HandlePBEnvelope(env *pb.FrostEnvelope) error {
	if env == nil {
		return ErrInvalidPBEnvelope
	}

	switch env.Kind {
	case pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_ROAST_REQUEST,
		pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_ROAST_RESPONSE:
		roastEnv, err := RoastEnvelopeFromPB(env)
		if err != nil {
			return err
		}
		return m.HandleRoastEnvelope(roastEnv)
	default:
		if m.frostRouter == nil {
			return nil
		}
		if err := m.frostRouter.Route(env); err != nil {
			if errors.Is(err, frostrtnet.ErrNoHandlerRegistered) {
				return nil
			}
			return err
		}
		return nil
	}
}

// GetTransitionWorker 获取 TransitionWorker（用于测试）
func (m *Manager) GetTransitionWorker() *TransitionWorker {
	return m.transitionWorker
}
