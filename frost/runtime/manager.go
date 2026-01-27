package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"dex/frost/chain"
	frostrtnet "dex/frost/runtime/net"
	"dex/frost/runtime/planning"
	"dex/frost/runtime/roast"
	"dex/frost/runtime/services"
	"dex/frost/runtime/session"
	"dex/frost/runtime/types"
	"dex/frost/runtime/workers"
	"dex/logs"
	"dex/pb"
)

// planningReaderAdapter 适配器：将runtime.ChainStateReader转换为planning.ChainStateReader
type planningReaderAdapter struct {
	reader ChainStateReader
}

func (a *planningReaderAdapter) Get(key string) ([]byte, bool, error) {
	return a.reader.Get(key)
}

func (a *planningReaderAdapter) Scan(prefix string, fn func(k string, v []byte) bool) error {
	return a.reader.Scan(prefix, fn)
}

// vaultProviderAdapter 适配器：将runtime.VaultCommitteeProvider转换为roast.VaultCommitteeProvider
type vaultProviderAdapter struct {
	provider VaultCommitteeProvider
}

func (a *vaultProviderAdapter) VaultCommittee(chain string, vaultID uint32, epoch uint64) ([]roast.SignerInfo, error) {
	committee, err := a.provider.VaultCommittee(chain, vaultID, epoch)
	if err != nil {
		return nil, err
	}
	result := make([]roast.SignerInfo, len(committee))
	for i, member := range committee {
		result[i] = roast.SignerInfo{
			ID:        roast.NodeID(member.ID),
			Index:     member.Index,
			PublicKey: member.PublicKey,
			Weight:    member.Weight,
		}
	}
	return result, nil
}

func (a *vaultProviderAdapter) VaultCurrentEpoch(chain string, vaultID uint32) uint64 {
	return a.provider.VaultCurrentEpoch(chain, vaultID)
}

func (a *vaultProviderAdapter) VaultGroupPubkey(chain string, vaultID uint32, epoch uint64) ([]byte, error) {
	return a.provider.VaultGroupPubkey(chain, vaultID, epoch)
}

// cryptoFactoryAdapter 适配器：将runtime.CryptoExecutorFactory转换为roast.CryptoExecutorFactory
type cryptoFactoryAdapter struct {
	factory CryptoExecutorFactory
}

func (a *cryptoFactoryAdapter) NewROASTExecutor(signAlgo int32) (roast.ROASTExecutor, error) {
	return a.factory.NewROASTExecutor(signAlgo)
}

func (a *cryptoFactoryAdapter) NewDKGExecutor(signAlgo int32) (roast.DKGExecutor, error) {
	return a.factory.NewDKGExecutor(signAlgo)
}

// workers适配器：将runtime类型转换为workers类型
type workersStateReaderAdapter struct {
	reader ChainStateReader
}

func (a *workersStateReaderAdapter) Get(key string) ([]byte, bool, error) {
	return a.reader.Get(key)
}

func (a *workersStateReaderAdapter) Scan(prefix string, fn func(k string, v []byte) bool) error {
	return a.reader.Scan(prefix, fn)
}

type workersTxSubmitterAdapter struct {
	submitter TxSubmitter
}

func (a *workersTxSubmitterAdapter) Submit(tx any) (txID string, err error) {
	return a.submitter.Submit(tx)
}

func (a *workersTxSubmitterAdapter) SubmitDkgCommitTx(ctx context.Context, tx *pb.FrostVaultDkgCommitTx) error {
	return a.submitter.SubmitDkgCommitTx(ctx, tx)
}

func (a *workersTxSubmitterAdapter) SubmitDkgShareTx(ctx context.Context, tx *pb.FrostVaultDkgShareTx) error {
	return a.submitter.SubmitDkgShareTx(ctx, tx)
}

func (a *workersTxSubmitterAdapter) SubmitDkgValidationSignedTx(ctx context.Context, tx *pb.FrostVaultDkgValidationSignedTx) error {
	return a.submitter.SubmitDkgValidationSignedTx(ctx, tx)
}

func (a *workersTxSubmitterAdapter) SubmitWithdrawSignedTx(ctx context.Context, tx *pb.FrostWithdrawSignedTx) error {
	return a.submitter.SubmitWithdrawSignedTx(ctx, tx)
}

type workersLogReporterAdapter struct {
	reporter LogReporter
}

func (a *workersLogReporterAdapter) ReportWithdrawPlanningLog(ctx context.Context, log *pb.FrostWithdrawPlanningLogTx) error {
	return a.reporter.ReportWithdrawPlanningLog(ctx, log)
}

type workersPubKeyProviderAdapter struct {
	provider MinerPubKeyProvider
}

func (a *workersPubKeyProviderAdapter) GetMinerSigningPubKey(minerID string, signAlgo pb.SignAlgo) ([]byte, error) {
	return a.provider.GetMinerSigningPubKey(minerID, signAlgo)
}

type workersCryptoFactoryAdapter struct {
	factory CryptoExecutorFactory
}

func (a *workersCryptoFactoryAdapter) NewROASTExecutor(signAlgo int32) (workers.ROASTExecutor, error) {
	return a.factory.NewROASTExecutor(signAlgo)
}

func (a *workersCryptoFactoryAdapter) NewDKGExecutor(signAlgo int32) (workers.DKGExecutor, error) {
	return a.factory.NewDKGExecutor(signAlgo)
}

// workers适配器2：将runtime类型转换为workers类型（用于WithdrawWorker）
type workersStateReaderAdapter2 struct {
	reader ChainStateReader
}

func (a *workersStateReaderAdapter2) Get(key string) ([]byte, bool, error) {
	return a.reader.Get(key)
}

func (a *workersStateReaderAdapter2) Scan(prefix string, fn func(k string, v []byte) bool) error {
	return a.reader.Scan(prefix, fn)
}

type workersTxSubmitterAdapter2 struct {
	submitter TxSubmitter
}

func (a *workersTxSubmitterAdapter2) Submit(tx any) (txID string, err error) {
	return a.submitter.Submit(tx)
}

func (a *workersTxSubmitterAdapter2) SubmitDkgCommitTx(ctx context.Context, tx *pb.FrostVaultDkgCommitTx) error {
	return a.submitter.SubmitDkgCommitTx(ctx, tx)
}

func (a *workersTxSubmitterAdapter2) SubmitDkgShareTx(ctx context.Context, tx *pb.FrostVaultDkgShareTx) error {
	return a.submitter.SubmitDkgShareTx(ctx, tx)
}

func (a *workersTxSubmitterAdapter2) SubmitDkgValidationSignedTx(ctx context.Context, tx *pb.FrostVaultDkgValidationSignedTx) error {
	return a.submitter.SubmitDkgValidationSignedTx(ctx, tx)
}

func (a *workersTxSubmitterAdapter2) SubmitWithdrawSignedTx(ctx context.Context, tx *pb.FrostWithdrawSignedTx) error {
	return a.submitter.SubmitWithdrawSignedTx(ctx, tx)
}

type workersVaultProviderAdapter struct {
	provider VaultCommitteeProvider
}

func (a *workersVaultProviderAdapter) VaultCommittee(chain string, vaultID uint32, epoch uint64) ([]workers.SignerInfo, error) {
	committee, err := a.provider.VaultCommittee(chain, vaultID, epoch)
	if err != nil {
		return nil, err
	}
	result := make([]workers.SignerInfo, len(committee))
	for i, member := range committee {
		result[i] = workers.SignerInfo{
			ID:        member.ID,
			Index:     member.Index,
			PublicKey: member.PublicKey,
			Weight:    member.Weight,
		}
	}
	return result, nil
}

func (a *workersVaultProviderAdapter) VaultCurrentEpoch(chain string, vaultID uint32) uint64 {
	return a.provider.VaultCurrentEpoch(chain, vaultID)
}

func (a *workersVaultProviderAdapter) VaultGroupPubkey(chain string, vaultID uint32, epoch uint64) ([]byte, error) {
	return a.provider.VaultGroupPubkey(chain, vaultID, epoch)
}

func (a *workersVaultProviderAdapter) CalculateThreshold(chain string, vaultID uint32) (int, error) {
	return a.provider.CalculateThreshold(chain, vaultID)
}

// signingServiceAdapter 适配器：将services.SigningService转换为workers.SigningService
type signingServiceAdapter struct {
	service services.SigningService
}

func (a *signingServiceAdapter) StartSigningSession(ctx context.Context, params *workers.SigningSessionParams) (string, error) {
	// 转换参数
	serviceParams := &services.SigningSessionParams{
		JobID:     params.JobID,
		Chain:     params.Chain,
		VaultID:   params.VaultID,
		KeyEpoch:  params.KeyEpoch,
		SignAlgo:  params.SignAlgo,
		Messages:  params.Messages,
		Threshold: params.Threshold,
	}
	return a.service.StartSigningSession(ctx, serviceParams)
}

func (a *signingServiceAdapter) GetSessionStatus(sessionID string) (*workers.SessionStatus, error) {
	status, err := a.service.GetSessionStatus(sessionID)
	if err != nil {
		return nil, err
	}
	// 转换状态
	return &workers.SessionStatus{
		SessionID:   status.SessionID,
		JobID:       status.JobID,
		State:       status.State,
		Progress:    status.Progress,
		StartedAt:   status.StartedAt,
		CompletedAt: status.CompletedAt,
		Error:       status.Error,
	}, nil
}

func (a *signingServiceAdapter) CancelSession(sessionID string) error {
	return a.service.CancelSession(sessionID)
}

func (a *signingServiceAdapter) WaitForCompletion(ctx context.Context, sessionID string, timeout time.Duration) (*workers.SignedPackage, error) {
	pkg, err := a.service.WaitForCompletion(ctx, sessionID, timeout)
	if err != nil {
		return nil, err
	}
	// 转换签名包
	return &workers.SignedPackage{
		SessionID:    pkg.SessionID,
		JobID:        pkg.JobID,
		Signature:    pkg.Signature,
		RawTx:        pkg.RawTx,
		TemplateHash: pkg.TemplateHash,
	}, nil
}

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
	Logger         logs.Logger

	// 子组件
	scanner          *planning.Scanner
	withdrawWorker   *workers.WithdrawWorker
	transitionWorker *workers.TransitionWorker
	coordinator      *roast.Coordinator
	participant      *roast.Participant
	roastDispatcher  *roast.Dispatcher
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
	LogReporter    LogReporter           // 规划日志汇报器
	Logger         logs.Logger
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
		Logger:         deps.Logger,
	}

	var roastMessenger types.RoastMessenger
	if deps.RoastMessenger != nil {
		// 使用提供的RoastMessenger，但需要适配为roast包使用的类型
		roastMessenger = &roastMessengerAdapter{messenger: deps.RoastMessenger}
	} else if deps.P2P != nil {
		// 创建RoastMessenger适配器，将runtime.P2P适配为roast包使用的类型
		roastMessenger = &roastMessengerAdapter{p2p: deps.P2P}
	}
	frostRouter := deps.FrostRouter
	if frostRouter == nil {
		frostRouter = frostrtnet.NewRouter()
		frostrtnet.RegisterDefaultHandlers(frostRouter)
	}

	// 初始化子组件
	// 适配runtime.ChainStateReader到planning.ChainStateReader
	planningReader := &planningReaderAdapter{reader: deps.StateReader}
	m.scanner = planning.NewScanner(planningReader)

	// 创建 Coordinator 和 Participant（用于 SigningService）
	// 由于接口已统一，直接使用runtime包中的类型
	m.coordinator = roast.NewCoordinator(config.NodeID, roastMessenger, deps.VaultProvider, deps.CryptoFactory, deps.Logger)
	m.participant = roast.NewParticipant(config.NodeID, roastMessenger, deps.VaultProvider, deps.CryptoFactory, sessionStore, deps.Logger)

	// 创建 SigningService
	roastSigningService := services.NewRoastSigningService(m.coordinator, m.participant, deps.VaultProvider)

	// 创建适配器，将 services.SigningService 适配为 workers.SigningService
	signingServiceAdapter := &signingServiceAdapter{service: roastSigningService}

	// 创建 WithdrawWorker（使用 SigningService）
	// TODO: 从配置读取 maxInFlightPerChainAsset
	maxInFlight := 1 // 默认值，应该从配置读取
	m.withdrawWorker = workers.NewWithdrawWorker(
		&workersStateReaderAdapter2{reader: deps.StateReader},
		deps.AdapterFactory,
		&workersTxSubmitterAdapter2{submitter: deps.TxSubmitter},
		&workersLogReporterAdapter{reporter: deps.LogReporter},
		signingServiceAdapter,
		deps.VaultProvider,
		maxInFlight,
		string(config.NodeID),
		deps.Logger,
	)

	// 创建适配器，将 chain.ChainAdapterFactory 适配为 workers.ChainAdapterFactory
	adapterFactoryAdapter := &chainAdapterFactoryAdapter{factory: deps.AdapterFactory}
	m.transitionWorker = workers.NewTransitionWorker(
		&workersStateReaderAdapter2{reader: deps.StateReader},
		&workersTxSubmitterAdapter2{submitter: deps.TxSubmitter},
		deps.PubKeyProvider,
		deps.CryptoFactory,
		deps.VaultProvider,
		deps.SignerProvider,
		adapterFactoryAdapter,
		string(config.NodeID),
		deps.Logger,
	)
	m.roastDispatcher = roast.NewDispatcher(m.coordinator, m.participant)
	m.frostRouter = frostRouter

	return m
}

// Start 启动 Manager
func (m *Manager) Start(ctx context.Context) error {
	if m.running.Swap(true) {
		return nil // 已经在运行
	}

	m.Logger.Info("[FrostManager] Starting with NodeID: %s", m.config.NodeID)

	// 订阅 finalized 事件
	if m.notifier != nil {
		m.notifier.SubscribeBlockFinalized(m.handleBlockFinalized)
	}

	// 启动主循环
	m.wg.Add(1)
	go m.runLoop(ctx)

	m.Logger.Info("[FrostManager] Started")
	return nil
}

// Stop 停止 Manager
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		m.Logger.Info("[FrostManager] Stopping...")
		m.running.Store(false)
		close(m.stopCh)
		m.wg.Wait()
		m.Logger.Info("[FrostManager] Stopped")
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
	m.Logger.Info("[FrostManager] Block finalized: height=%d", height)

	// 发送到处理队列
	select {
	case m.finalizedCh <- height:
	default:
		m.Logger.Warn("[FrostManager] finalized channel full, dropping height=%d", height)
	}

	// 调用测试回调
	if m.onBlockFinalized != nil {
		m.onBlockFinalized(height)
	}
}

// runLoop 主循环
func (m *Manager) runLoop(ctx context.Context) {
	defer m.wg.Done()
	logs.SetThreadNodeContext(string(m.config.NodeID))

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
			m.Logger.Info("[FrostManager] Context cancelled")
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
	m.Logger.Trace("[FrostManager] Processing finalized block: height=%d", height)

	// 检查是否有需要启动的 DKG 会话（触发条件检测）
	if m.transitionWorker != nil {
		if err := m.transitionWorker.CheckTriggerConditions(ctx, height); err != nil {
			m.Logger.Error("[FrostManager] CheckTriggerConditions error: %v", err)
		}
	}
}

// scanAndProcess 扫描并处理提现请求
func (m *Manager) scanAndProcess(ctx context.Context) {
	if m.transitionWorker != nil {
		if err := m.transitionWorker.StartPendingSessions(ctx); err != nil {
			m.Logger.Error("[FrostManager] StartPendingSessions error: %v", err)
		}
	}
	for _, pair := range m.config.SupportedChains {
		m.processChainAsset(ctx, pair.Chain, pair.Asset)
	}
}

// processChainAsset 处理单个链资产对
func (m *Manager) processChainAsset(ctx context.Context, chainName, asset string) {
	// 扫描队首 QUEUED withdraw
	scanResult, err := m.scanner.ScanOnce(chainName, asset)
	if err != nil {
		m.Logger.Error("[FrostManager] Scan error for %s/%s: %v", chainName, asset, err)
		return
	}

	if scanResult == nil {
		// 没有待处理的 withdraw
		return
	}

	m.Logger.Trace("[FrostManager] Found pending withdraw: chain=%s, asset=%s, id=%s, seq=%d",
		chainName, asset, scanResult.WithdrawID, scanResult.Seq)

	// 处理提现（传入 context）
	job, err := m.withdrawWorker.ProcessOnce(ctx, chainName, asset)
	if err != nil {
		m.Logger.Info("[FrostManager] ProcessOnce error: %v", err)
		return
	}

	if job != nil {
		m.Logger.Info("[FrostManager] Created job: id=%s, withdraws=%v", job.JobID, job.WithdrawIDs)
	}
}

// GetCoordinator 获取协调者（用于测试）
func (m *Manager) GetCoordinator() *roast.Coordinator {
	return m.coordinator
}

// GetParticipant 获取参与者（用于测试）
func (m *Manager) GetParticipant() *roast.Participant {
	return m.participant
}

// HandleRoastEnvelope routes a ROAST message to coordinator/participant.
func (m *Manager) HandleRoastEnvelope(env *roast.Envelope) error {
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
	// 转换 runtime.FrostEnvelope 到 roast.FrostEnvelope
	roastEnv := &roast.FrostEnvelope{
		SessionID: env.SessionID,
		Kind:      env.Kind,
		From:      roast.NodeID(env.From),
		Chain:     env.Chain,
		VaultID:   env.VaultID,
		SignAlgo:  env.SignAlgo,
		Epoch:     env.Epoch,
		Round:     env.Round,
		Payload:   env.Payload,
		Sig:       env.Sig,
	}
	return m.roastDispatcher.HandleFrostEnvelope(roastEnv)
}

// HandlePBEnvelope routes a protobuf envelope to ROAST or other handlers.
func (m *Manager) HandlePBEnvelope(env *pb.FrostEnvelope) error {
	if env == nil {
		return roast.ErrInvalidPBEnvelope
	}

	switch env.Kind {
	case pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_ROAST_REQUEST,
		pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_ROAST_RESPONSE:
		roastEnv, err := roast.EnvelopeFromPB(env)
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
func (m *Manager) GetTransitionWorker() *workers.TransitionWorker {
	return m.transitionWorker
}

// roastMessengerAdapter 适配器：将runtime.RoastMessenger适配为types.RoastMessenger
type roastMessengerAdapter struct {
	messenger RoastMessenger
	p2p       P2P
}

func (a *roastMessengerAdapter) Send(to types.NodeID, msg *types.RoastEnvelope) error {
	if a == nil || msg == nil {
		return nil
	}
	if a.messenger != nil {
		return a.messenger.Send(to, msg)
	}
	if a.p2p != nil {
		// 转换types.RoastEnvelope到FrostEnvelope
		frostEnv := &FrostEnvelope{
			SessionID: msg.SessionID,
			Kind:      msg.Kind,
			From:      msg.From,
			Chain:     msg.Chain,
			VaultID:   msg.VaultID,
			SignAlgo:  msg.SignAlgo,
			Epoch:     msg.Epoch,
			Round:     msg.Round,
			Payload:   msg.Payload,
		}
		return a.p2p.Send(to, frostEnv)
	}
	return nil
}

func (a *roastMessengerAdapter) Broadcast(peers []types.NodeID, msg *types.RoastEnvelope) error {
	if a == nil || msg == nil {
		return nil
	}
	if a.messenger != nil {
		return a.messenger.Broadcast(peers, msg)
	}
	if a.p2p != nil {
		// 转换types.RoastEnvelope到FrostEnvelope
		frostEnv := &FrostEnvelope{
			SessionID: msg.SessionID,
			Kind:      msg.Kind,
			From:      msg.From,
			Chain:     msg.Chain,
			VaultID:   msg.VaultID,
			SignAlgo:  msg.SignAlgo,
			Epoch:     msg.Epoch,
			Round:     msg.Round,
			Payload:   msg.Payload,
		}
		return a.p2p.Broadcast(peers, frostEnv)
	}
	return nil
}

// chainAdapterFactoryAdapter 适配器：将 chain.ChainAdapterFactory 适配为 workers.ChainAdapterFactory
type chainAdapterFactoryAdapter struct {
	factory chain.ChainAdapterFactory
}

func (a *chainAdapterFactoryAdapter) Adapter(chainName string) (workers.ChainAdapter, error) {
	chainAdapter, err := a.factory.Adapter(chainName)
	if err != nil {
		return nil, err
	}
	return &chainAdapterAdapter{adapter: chainAdapter}, nil
}

// chainAdapterAdapter 适配器：将 chain.ChainAdapter 适配为 workers.ChainAdapter
type chainAdapterAdapter struct {
	adapter chain.ChainAdapter
}

func (a *chainAdapterAdapter) BuildWithdrawTemplate(params workers.WithdrawTemplateParams) (*workers.TemplateResult, error) {
	// 转换参数
	chainParams := chain.WithdrawTemplateParams{
		Chain:         params.Chain,
		Asset:         params.Asset,
		VaultID:       params.VaultID,
		KeyEpoch:      params.KeyEpoch,
		WithdrawIDs:   params.WithdrawIDs,
		Outputs:       convertOutputsToChain(params.Outputs),
		Inputs:        convertUTXOsToChain(params.Inputs),
		ChangeAddress: params.ChangeAddress,
		Fee:           params.Fee,
		ChangeAmount:  params.ChangeAmount,
		ContractAddr:  params.ContractAddr,
		MethodID:      params.MethodID,
	}

	result, err := a.adapter.BuildWithdrawTemplate(chainParams)
	if err != nil {
		return nil, err
	}

	// 转换结果
	return &workers.TemplateResult{
		TemplateHash: result.TemplateHash,
		TemplateData: result.TemplateData,
		SigHashes:    result.SigHashes,
	}, nil
}

// convertOutputsToChain 转换输出列表
func convertOutputsToChain(outputs []workers.WithdrawOutput) []chain.WithdrawOutput {
	result := make([]chain.WithdrawOutput, len(outputs))
	for i, out := range outputs {
		result[i] = chain.WithdrawOutput{
			WithdrawID: out.WithdrawID,
			To:         out.To,
			Amount:     out.Amount,
		}
	}
	return result
}

// convertUTXOsToChain 转换 UTXO 列表
func convertUTXOsToChain(utxos []workers.UTXO) []chain.UTXO {
	result := make([]chain.UTXO, len(utxos))
	for i, utxo := range utxos {
		result[i] = chain.UTXO{
			TxID:          utxo.TxID,
			Vout:          utxo.Vout,
			Amount:        utxo.Amount,
			ScriptPubKey:  utxo.ScriptPubKey,
			ConfirmHeight: utxo.ConfirmHeight,
		}
	}
	return result
}
