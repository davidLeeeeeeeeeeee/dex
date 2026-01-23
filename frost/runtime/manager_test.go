package runtime

import (
	"context"
	"crypto/sha256"
	"dex/frost/chain"
	"dex/frost/runtime/planning"
	"dex/frost/runtime/workers"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"fmt"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
)

// FakeNotifier 用于测试的 fake FinalityNotifier
type FakeNotifier struct {
	mu        sync.Mutex
	callbacks []func(height uint64)
}

func NewFakeNotifier() *FakeNotifier {
	return &FakeNotifier{
		callbacks: make([]func(height uint64), 0),
	}
}

// SubscribeBlockFinalized 实现 FinalityNotifier 接口
func (f *FakeNotifier) SubscribeBlockFinalized(fn func(height uint64)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callbacks = append(f.callbacks, fn)
}

// TriggerFinalized 触发 finalized 事件
func (f *FakeNotifier) TriggerFinalized(height uint64) {
	f.mu.Lock()
	callbacks := make([]func(height uint64), len(f.callbacks))
	copy(callbacks, f.callbacks)
	f.mu.Unlock()

	for _, cb := range callbacks {
		cb(height)
	}
}

func TestManager_StartStop(t *testing.T) {
	notifier := NewFakeNotifier()

	m := NewManager(
		ManagerConfig{NodeID: "test-node-1"},
		ManagerDeps{
			Notifier:       notifier,
			Logger:         logs.NewNodeLogger("test", 100),
			StateReader:    newFakeStateReader(),
			VaultProvider:  newFakeVaultProvider(),
			SignerProvider: newFakeVaultProvider(),
			TxSubmitter:    newFakeTxSubmitter(),
			AdapterFactory: newFakeAdapterFactory(),
		},
	)

	ctx := context.Background()

	// 验证初始状态
	if m.IsRunning() {
		t.Fatal("Manager should not be running before Start")
	}

	// 启动
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if !m.IsRunning() {
		t.Fatal("Manager should be running after Start")
	}

	// 停止
	m.Stop()

	if m.IsRunning() {
		t.Fatal("Manager should not be running after Stop")
	}
}

func TestManager_ReceiveFinalizedEvent(t *testing.T) {
	notifier := NewFakeNotifier()

	m := NewManager(
		ManagerConfig{NodeID: "test-node-1"},
		ManagerDeps{
			Notifier:       notifier,
			Logger:         logs.NewNodeLogger("test", 100),
			StateReader:    newFakeStateReader(),
			VaultProvider:  newFakeVaultProvider(),
			SignerProvider: newFakeVaultProvider(),
			TxSubmitter:    newFakeTxSubmitter(),
			AdapterFactory: newFakeAdapterFactory(),
		},
	)

	ctx := context.Background()

	// 用于同步的通道
	receivedHeights := make(chan uint64, 10)
	m.SetOnBlockFinalized(func(height uint64) {
		receivedHeights <- height
	})

	// 启动 Manager
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer m.Stop()

	// 触发 finalized 事件
	testHeights := []uint64{100, 101, 102, 200, 500}

	for _, h := range testHeights {
		notifier.TriggerFinalized(h)
	}

	// 验证收到的事件
	for _, expected := range testHeights {
		select {
		case received := <-receivedHeights:
			if received != expected {
				t.Errorf("Expected height %d, got %d", expected, received)
			}
		case <-time.After(time.Second):
			t.Fatalf("Timeout waiting for height %d", expected)
		}
	}

	// 验证 LastFinalizedHeight
	if got := m.LastFinalizedHeight(); got != 500 {
		t.Errorf("LastFinalizedHeight() = %d, want 500", got)
	}
}

func TestManager_IgnoreEventsAfterStop(t *testing.T) {
	notifier := NewFakeNotifier()

	m := NewManager(
		ManagerConfig{NodeID: "test-node-1"},
		ManagerDeps{
			Notifier:       notifier,
			Logger:         logs.NewNodeLogger("test", 100),
			StateReader:    newFakeStateReader(),
			VaultProvider:  newFakeVaultProvider(),
			SignerProvider: newFakeVaultProvider(),
			TxSubmitter:    newFakeTxSubmitter(),
			AdapterFactory: newFakeAdapterFactory(),
		},
	)

	ctx := context.Background()

	callCount := 0
	m.SetOnBlockFinalized(func(height uint64) {
		callCount++
	})

	// 启动并接收一个事件
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	notifier.TriggerFinalized(100)
	time.Sleep(10 * time.Millisecond) // 等待处理

	// 停止
	m.Stop()

	// 再触发事件
	notifier.TriggerFinalized(101)
	notifier.TriggerFinalized(102)
	time.Sleep(10 * time.Millisecond)

	// 验证只收到了停止前的事件
	if callCount != 1 {
		t.Errorf("Expected 1 call, got %d", callCount)
	}
}

// ======================== Scanner Tests ========================

// fakeStateReader 用于测试的 fake StateReader
type fakeStateReader struct {
	data map[string][]byte
}

func newFakeStateReader() *fakeStateReader {
	return &fakeStateReader{data: make(map[string][]byte)}
}

func (r *fakeStateReader) Set(key string, value []byte) {
	r.data[key] = value
}

func (r *fakeStateReader) Get(key string) ([]byte, bool, error) {
	v, ok := r.data[key]
	if !ok || len(v) == 0 {
		return nil, false, nil
	}
	return v, true, nil
}

func (r *fakeStateReader) Scan(prefix string, fn func(k string, v []byte) bool) error {
	for k, v := range r.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			if !fn(k, v) {
				break
			}
		}
	}
	return nil
}

// TestScanner 测试 Scanner
func TestScanner(t *testing.T) {
	reader := newFakeStateReader()
	scanner := planning.NewScanner(reader)

	// 测试空队列
	t.Run("EmptyQueue", func(t *testing.T) {
		result, err := scanner.ScanOnce("BTC", "native")
		if err != nil {
			t.Fatalf("ScanOnce failed: %v", err)
		}
		if result != nil {
			t.Errorf("Expected nil result for empty queue, got %+v", result)
		}
	})

	// 设置 withdraw 状态
	t.Run("FindQueuedWithdraw", func(t *testing.T) {
		// 设置 seq = 1
		reader.Set(keys.KeyFrostWithdrawFIFOSeq("BTC", "native"), []byte("1"))
		// 设置 index: seq=1 -> withdraw_id
		reader.Set(keys.KeyFrostWithdrawFIFOIndex("BTC", "native", 1), []byte("test_withdraw_1"))
		// 设置 withdraw 状态 (QUEUED)
		withdrawState := &pb.FrostWithdrawState{
			WithdrawId: "test_withdraw_1",
			Chain:      "BTC",
			Asset:      "native",
			To:         "bc1q...",
			Amount:     "1000000",
			Status:     "QUEUED",
			Seq:        1,
		}
		data, _ := proto.Marshal(withdrawState)
		reader.Set(keys.KeyFrostWithdraw("test_withdraw_1"), data)

		result, err := scanner.ScanOnce("BTC", "native")
		if err != nil {
			t.Fatalf("ScanOnce failed: %v", err)
		}
		if result == nil {
			t.Fatal("Expected non-nil result")
		}
		if result.WithdrawID != "test_withdraw_1" {
			t.Errorf("Expected withdraw_id 'test_withdraw_1', got '%s'", result.WithdrawID)
		}
		if result.Seq != 1 {
			t.Errorf("Expected seq 1, got %d", result.Seq)
		}
	})

	// 测试 SIGNED 状态的 withdraw 不返回
	t.Run("SkipSignedWithdraw", func(t *testing.T) {
		reader2 := newFakeStateReader()
		scanner2 := planning.NewScanner(reader2)

		reader2.Set(keys.KeyFrostWithdrawFIFOSeq("ETH", "native"), []byte("1"))
		reader2.Set(keys.KeyFrostWithdrawFIFOIndex("ETH", "native", 1), []byte("test_withdraw_2"))
		withdrawState := &pb.FrostWithdrawState{
			WithdrawId: "test_withdraw_2",
			Chain:      "ETH",
			Asset:      "native",
			Status:     "SIGNED", // 已签名
			Seq:        1,
		}
		data, _ := proto.Marshal(withdrawState)
		reader2.Set(keys.KeyFrostWithdraw("test_withdraw_2"), data)

		result, err := scanner2.ScanOnce("ETH", "native")
		if err != nil {
			t.Fatalf("ScanOnce failed: %v", err)
		}
		if result != nil {
			t.Errorf("Expected nil for SIGNED withdraw, got %+v", result)
		}
	})
}

// ======================== JobPlanner Tests ========================

// fakeChainAdapter 用于测试的 fake ChainAdapter
type fakeChainAdapter struct {
	chainName string
}

func (a *fakeChainAdapter) Chain() string {
	return a.chainName
}

func (a *fakeChainAdapter) SignAlgo() pb.SignAlgo {
	return pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340
}

func (a *fakeChainAdapter) BuildWithdrawTemplate(params chain.WithdrawTemplateParams) (*chain.TemplateResult, error) {
	// 返回确定性的 template_hash
	data := params.Chain + "|" + params.Asset + "|" + params.Outputs[0].To
	hash := sha256.Sum256([]byte(data))
	return &chain.TemplateResult{
		TemplateHash: hash[:],
		TemplateData: []byte(data),
	}, nil
}

func (a *fakeChainAdapter) TemplateHash(templateData []byte) ([]byte, error) {
	hash := sha256.Sum256(templateData)
	return hash[:], nil
}

func (a *fakeChainAdapter) PackageSigned(templateData []byte, signatures [][]byte) (*chain.SignedPackage, error) {
	return &chain.SignedPackage{
		TemplateData: templateData,
		Signatures:   signatures,
	}, nil
}

func (a *fakeChainAdapter) VerifySignature(groupPubkey []byte, msg []byte, signature []byte) (bool, error) {
	return true, nil
}

// fakeAdapterFactory 用于测试的 fake AdapterFactory
type fakeAdapterFactory struct {
	adapters map[string]*fakeChainAdapter
}

func newFakeAdapterFactory() *fakeAdapterFactory {
	return &fakeAdapterFactory{
		adapters: make(map[string]*fakeChainAdapter),
	}
}

func (f *fakeAdapterFactory) Register(chainName string) {
	f.adapters[chainName] = &fakeChainAdapter{chainName: chainName}
}

func (f *fakeAdapterFactory) Adapter(chainName string) (chain.ChainAdapter, error) {
	adapter, ok := f.adapters[chainName]
	if !ok {
		return nil, chain.ErrUnsupportedChain
	}
	return adapter, nil
}

func (f *fakeAdapterFactory) RegisterAdapter(adapter chain.ChainAdapter) {
	// Not used in tests
}

// TestJobPlanner 测试 JobPlanner
func TestJobPlanner(t *testing.T) {
	reader := newFakeStateReader()
	factory := newFakeAdapterFactory()
	factory.Register("BTC")

	planner := planning.NewJobPlanner(reader, factory)

	// 设置 withdraw 状态
	withdrawState := &pb.FrostWithdrawState{
		WithdrawId: "test_withdraw_1",
		Chain:      "BTC",
		Asset:      "native",
		To:         "bc1qtest...",
		Amount:     "1000000",
		Status:     "QUEUED",
		Seq:        1,
	}
	data, _ := proto.Marshal(withdrawState)
	reader.Set(keys.KeyFrostWithdraw("test_withdraw_1"), data)

	// 创建扫描结果
	scanResult := &planning.ScanResult{
		Chain:      "BTC",
		Asset:      "native",
		WithdrawID: "test_withdraw_1",
		Seq:        1,
	}

	// 规划 job
	job, err := planner.PlanJob(scanResult)
	if err != nil {
		t.Fatalf("PlanJob failed: %v", err)
	}
	if job == nil {
		t.Fatal("Expected non-nil job")
	}

	// 验证 job 属性
	if job.Chain != "BTC" {
		t.Errorf("Expected chain 'BTC', got '%s'", job.Chain)
	}
	if job.Asset != "native" {
		t.Errorf("Expected asset 'native', got '%s'", job.Asset)
	}
	if len(job.WithdrawIDs) != 1 || job.WithdrawIDs[0] != "test_withdraw_1" {
		t.Errorf("Unexpected withdraw_ids: %v", job.WithdrawIDs)
	}
	if len(job.TemplateHash) == 0 {
		t.Error("Expected non-empty template_hash")
	}
	if job.JobID == "" {
		t.Error("Expected non-empty job_id")
	}

	// 测试确定性：相同输入多次规划结果一致
	t.Run("Deterministic", func(t *testing.T) {
		job2, err := planner.PlanJob(scanResult)
		if err != nil {
			t.Fatalf("PlanJob failed: %v", err)
		}
		if job2.JobID != job.JobID {
			t.Errorf("Expected same job_id, got '%s' vs '%s'", job.JobID, job2.JobID)
		}
		if string(job2.TemplateHash) != string(job.TemplateHash) {
			t.Errorf("Expected same template_hash")
		}
	})
}

// ======================== WithdrawWorker Tests ========================

// fakeTxSubmitter 用于测试的 fake TxSubmitter
type fakeTxSubmitter struct {
	submitted []any
}

func newFakeTxSubmitter() *fakeTxSubmitter {
	return &fakeTxSubmitter{submitted: make([]any, 0)}
}

func (s *fakeTxSubmitter) Submit(tx any) (txID string, err error) {
	s.submitted = append(s.submitted, tx)
	return "fake_tx_id", nil
}

func (s *fakeTxSubmitter) SubmitDkgCommitTx(ctx context.Context, tx *pb.FrostVaultDkgCommitTx) error {
	s.submitted = append(s.submitted, tx)
	return nil
}

func (s *fakeTxSubmitter) SubmitDkgShareTx(ctx context.Context, tx *pb.FrostVaultDkgShareTx) error {
	s.submitted = append(s.submitted, tx)
	return nil
}

func (s *fakeTxSubmitter) SubmitDkgValidationSignedTx(ctx context.Context, tx *pb.FrostVaultDkgValidationSignedTx) error {
	s.submitted = append(s.submitted, tx)
	return nil
}

func (s *fakeTxSubmitter) SubmitWithdrawSignedTx(ctx context.Context, tx *pb.FrostWithdrawSignedTx) error {
	s.submitted = append(s.submitted, tx)
	return nil
}

func (s *fakeTxSubmitter) SubmitWithdrawPlanningLogTx(ctx context.Context, tx *pb.FrostWithdrawPlanningLogTx) error {
	s.submitted = append(s.submitted, tx)
	return nil
}

func (s *fakeTxSubmitter) GetSubmitted() []any {
	return s.submitted
}

// fakeSigningService 用于测试
type fakeSigningService struct{}

func (s *fakeSigningService) StartSigningSession(ctx context.Context, params *workers.SigningSessionParams) (string, error) {
	return "session_1", nil
}

func (s *fakeSigningService) GetSessionStatus(sessionID string) (*workers.SessionStatus, error) {
	return &workers.SessionStatus{State: "COMPLETED"}, nil
}

func (s *fakeSigningService) CancelSession(sessionID string) error {
	return nil
}

func (s *fakeSigningService) WaitForCompletion(ctx context.Context, sessionID string, timeout time.Duration) (*workers.SignedPackage, error) {
	// 模拟异步完成
	return &workers.SignedPackage{
		SessionID:    sessionID,
		JobID:        "job_1",
		Signature:    []byte("signature"),
		RawTx:        []byte("rawtx"),
		TemplateHash: []byte("hash"),
	}, nil
}

// TestWithdrawWorker 测试 WithdrawWorker
func TestWithdrawWorker(t *testing.T) {
	reader := newFakeStateReader()
	factory := newFakeAdapterFactory()
	factory.Register("eth")
	submitter := newFakeTxSubmitter()

	// 创建fake signingService和vaultProvider
	var signingService workers.SigningService = &fakeSigningService{}
	var vaultProvider VaultCommitteeProvider = newFakeVaultProvider()

	// 注入 SignerProvider，避免空指针 panic
	worker := workers.NewWithdrawWorker(reader, factory, submitter, signingService, vaultProvider, 1, "test-node-address", logs.NewNodeLogger("test", 0))

	// 1. 设置 VaultConfig
	vaultCfg := &pb.FrostVaultConfig{
		Chain:         "eth",
		VaultCount:    1,
		CommitteeSize: 100,
	}
	vaultCfgBytes, _ := proto.Marshal(vaultCfg)
	reader.Set(keys.KeyFrostVaultConfig("eth", 0), vaultCfgBytes)

	// 2. 设置 VaultState (ACTIVE)
	vaultState := &pb.FrostVaultState{
		Chain:    "eth",
		VaultId:  0,
		Status:   "ACTIVE",
		KeyEpoch: 1,
		SignAlgo: 1, // 必须设置，否则 getSignAlgo 会失败
	}
	vaultStateBytes, _ := proto.Marshal(vaultState)
	reader.Set(keys.KeyFrostVaultState("eth", 0), vaultStateBytes)

	// 3. 设置充值 (RechargeRequest) 以提供余额
	recharge := &pb.RechargeRequest{
		RequestId: "recharge_tx_1",
		Amount:    "2000000",
	}
	rechargeBytes, _ := proto.Marshal(recharge)
	reader.Set(keys.KeyRechargeRequest("recharge_tx_1"), rechargeBytes)

	// 4. 设置 FundsLot 指向充值
	lotKey := fmt.Sprintf("v1_frost_funds_lot_%s_%s_%d_%d", "eth", "native", 0, 1)
	reader.Set(lotKey, []byte("recharge_tx_1"))

	// 设置 withdraw 队列
	reader.Set(keys.KeyFrostWithdrawFIFOHead("eth", "native"), []byte("1"))
	reader.Set(keys.KeyFrostWithdrawFIFOSeq("eth", "native"), []byte("1"))
	reader.Set(keys.KeyFrostWithdrawFIFOIndex("eth", "native", 1), []byte("test_withdraw_1"))

	withdrawState := &pb.FrostWithdrawState{
		WithdrawId: "test_withdraw_1",
		Chain:      "eth",
		Asset:      "native",
		To:         "0x123...",
		Amount:     "1000000",
		Status:     "QUEUED",
		Seq:        1,
	}
	data, _ := proto.Marshal(withdrawState)
	reader.Set(keys.KeyFrostWithdraw("test_withdraw_1"), data)

	// 处理一次
	ctx := context.Background()
	_, err := worker.ProcessOnce(ctx, "eth", "native")
	if err != nil {
		t.Fatalf("ProcessOnce failed: %v", err)
	}

	// 验证 submitter 收到了正确的 tx (异步等待)
	var submitted []any
	for i := 0; i < 20; i++ { // 等待最多 2 秒
		submitted = submitter.GetSubmitted()
		if len(submitted) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if len(submitted) != 1 {
		t.Fatalf("Expected 1 submitted tx, got %d", len(submitted))
	}

	tx, ok := submitted[0].(*pb.FrostWithdrawSignedTx)
	if !ok {
		t.Fatalf("Expected *pb.FrostWithdrawSignedTx, got %T", submitted[0])
	}

	if len(tx.WithdrawIds) != 1 || tx.WithdrawIds[0] != "test_withdraw_1" {
		t.Errorf("Unexpected withdraw_ids: %v", tx.WithdrawIds)
	}
}

// TestWithdrawWorker_EmptyQueue 测试空队列
func TestWithdrawWorker_EmptyQueue(t *testing.T) {
	reader := newFakeStateReader()
	factory := newFakeAdapterFactory()
	factory.Register("BTC")
	submitter := newFakeTxSubmitter()

	// 创建fake signingService和vaultProvider
	var signingService workers.SigningService = nil // TODO: 创建fake实现
	var vaultProvider VaultCommitteeProvider = nil  // TODO: 创建fake实现
	worker := workers.NewWithdrawWorker(reader, factory, submitter, signingService, vaultProvider, 1, "test-node-address", logs.NewNodeLogger("test", 0))

	// 处理空队列
	ctx := context.Background()
	job, err := worker.ProcessOnce(ctx, "BTC", "native")
	if err != nil {
		t.Fatalf("ProcessOnce failed: %v", err)
	}
	if job != nil {
		t.Errorf("Expected nil job for empty queue, got %+v", job)
	}

	// 验证没有提交任何 tx

}

// fakeVaultProvider 用于测试的 fake VaultCommitteeProvider
type fakeVaultProvider struct{}

func newFakeVaultProvider() *fakeVaultProvider {
	return &fakeVaultProvider{}
}

func (p *fakeVaultProvider) VaultCommittee(chain string, vaultID uint32, epoch uint64) ([]SignerInfo, error) {
	return []SignerInfo{{ID: "member1", Index: 1, Weight: 1}}, nil
}

func (p *fakeVaultProvider) VaultCurrentEpoch(chain string, vaultID uint32) uint64 {
	return 1
}

func (p *fakeVaultProvider) VaultGroupPubkey(chain string, vaultID uint32, epoch uint64) ([]byte, error) {
	return []byte("group_pubkey"), nil
}

func (p *fakeVaultProvider) CalculateThreshold(chain string, vaultID uint32) (int, error) {
	return 2, nil
}

func (p *fakeVaultProvider) CurrentEpoch(height uint64) uint64 {
	return 1
}

func (p *fakeVaultProvider) Top10000(height uint64) ([]SignerInfo, error) {
	return []SignerInfo{{ID: "member1", Index: 1, Weight: 1}}, nil
}
