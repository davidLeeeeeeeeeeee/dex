// frost/runtime/adapters/tx_submitter.go
// TxSubmitter 适配器实现（基于 txpool）
package adapters

import (
	"context"
	"crypto/sha256"
	"dex/db"
	"dex/frost/runtime"
	"dex/pb"
	"dex/sender"
	"dex/txpool"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"google.golang.org/protobuf/proto"
)

// ErrTxPoolFull txpool 已满
var ErrTxPoolFull = errors.New("txpool is full")

// TxPool 交易池接口（由外部实现）
type TxPool interface {
	// AddTx 添加交易到池中
	AddTx(tx proto.Message) error
	// Broadcast 广播交易
	Broadcast(tx proto.Message) error
}

// TxPoolAdapter 适配 txpool.TxPool 到 adapters.TxPool 接口
type TxPoolAdapter struct {
	txPool *txpool.TxPool
	sender *sender.SenderManager
}

// NewTxPoolAdapter 创建新的 TxPoolAdapter
func NewTxPoolAdapter(txPool *txpool.TxPool, sender *sender.SenderManager) *TxPoolAdapter {
	return &TxPoolAdapter{
		txPool: txPool,
		sender: sender,
	}
}

// AddTx 添加交易到池中
func (a *TxPoolAdapter) AddTx(tx proto.Message) error {
	// 将 proto.Message 转换为 pb.AnyTx
	anyTx, ok := tx.(*pb.AnyTx)
	if !ok {
		return fmt.Errorf("AddTx: expected *pb.AnyTx, got %T", tx)
	}

	// 使用 txpool 的 SubmitTx 方法
	err := a.txPool.SubmitTx(anyTx, "", func(txID string) {
		// 广播回调
		if a.sender != nil {
			a.sender.BroadcastTx(anyTx)
		}
	})
	if err != nil {
		return fmt.Errorf("AddTx: SubmitTx failed: %w", err)
	}
	return nil
}

// Broadcast 广播交易
func (a *TxPoolAdapter) Broadcast(tx proto.Message) error {
	if a.sender == nil {
		return nil
	}
	anyTx, ok := tx.(*pb.AnyTx)
	if !ok {
		return nil
	}
	a.sender.BroadcastTx(anyTx)
	return nil
}

// TxPoolSubmitter 基于 TxPool 的 TxSubmitter 实现
type TxPoolSubmitter struct {
	pool TxPool
}

// NewTxPoolSubmitter 创建新的 TxPoolSubmitter
func NewTxPoolSubmitter(pool TxPool) *TxPoolSubmitter {
	return &TxPoolSubmitter{pool: pool}
}

// Submit 提交交易
func (s *TxPoolSubmitter) Submit(tx any) (txID string, err error) {
	msg, ok := tx.(proto.Message)
	if !ok {
		return "", errors.New("tx must be proto.Message")
	}

	// 计算 txID
	data, err := proto.Marshal(msg)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	txID = hex.EncodeToString(hash[:])

	// 添加到 pool 并广播
	if err := s.pool.AddTx(msg); err != nil {
		return "", err
	}
	if err := s.pool.Broadcast(msg); err != nil {
		// 广播失败不影响 txID 返回，交易已在本地 pool
		// 可以后续重试广播
	}

	return txID, nil
}

// SubmitDkgCommitTx 提交 DKG 承诺交易
func (s *TxPoolSubmitter) SubmitDkgCommitTx(ctx context.Context, tx *pb.FrostVaultDkgCommitTx) error {
	anyTx := &pb.AnyTx{Content: &pb.AnyTx_FrostVaultDkgCommitTx{FrostVaultDkgCommitTx: tx}}
	_, err := s.Submit(anyTx)
	return err
}

// SubmitDkgShareTx 提交 DKG share 交易
func (s *TxPoolSubmitter) SubmitDkgShareTx(ctx context.Context, tx *pb.FrostVaultDkgShareTx) error {
	anyTx := &pb.AnyTx{Content: &pb.AnyTx_FrostVaultDkgShareTx{FrostVaultDkgShareTx: tx}}
	_, err := s.Submit(anyTx)
	return err
}

// SubmitDkgValidationSignedTx 提交 DKG 验证签名交易
func (s *TxPoolSubmitter) SubmitDkgValidationSignedTx(ctx context.Context, tx *pb.FrostVaultDkgValidationSignedTx) error {
	anyTx := &pb.AnyTx{Content: &pb.AnyTx_FrostVaultDkgValidationSignedTx{FrostVaultDkgValidationSignedTx: tx}}
	_, err := s.Submit(anyTx)
	return err
}

// SubmitWithdrawSignedTx 提交提现签名交易
func (s *TxPoolSubmitter) SubmitWithdrawSignedTx(ctx context.Context, tx *pb.FrostWithdrawSignedTx) error {
	anyTx := &pb.AnyTx{Content: &pb.AnyTx_FrostWithdrawSignedTx{FrostWithdrawSignedTx: tx}}
	_, err := s.Submit(anyTx)
	return err
}

// LocalLogReporter 只有本地记录功能的汇报器
type LocalLogReporter struct {
	dbManager *db.Manager
}

func NewLocalLogReporter(dbManager *db.Manager) *LocalLogReporter {
	return &LocalLogReporter{dbManager: dbManager}
}

func (r *LocalLogReporter) ReportWithdrawPlanningLog(ctx context.Context, log *pb.FrostWithdrawPlanningLogTx) error {
	if r.dbManager == nil || log == nil {
		return nil
	}
	// 将日志存入本地数据库，Key：v1_frost_planning_log_<withdraw_id>_<reporter>
	key := fmt.Sprintf("v1_frost_planning_log_%s_%s", log.WithdrawId, log.Reporter)
	data, err := proto.Marshal(log)
	if err != nil {
		return err
	}
	r.dbManager.EnqueueSet(key, string(data))
	return nil
}

// Ensure LocalLogReporter implements runtime.LogReporter
var _ runtime.LogReporter = (*LocalLogReporter)(nil)

// Ensure TxPoolSubmitter implements runtime.TxSubmitter
var _ runtime.TxSubmitter = (*TxPoolSubmitter)(nil)

// FakeTxSubmitter 用于测试的 fake TxSubmitter
type FakeTxSubmitter struct {
	mu          sync.Mutex
	submitted   []any
	submitCount int
	shouldFail  bool
	failErr     error
}

// NewFakeTxSubmitter 创建新的 FakeTxSubmitter
func NewFakeTxSubmitter() *FakeTxSubmitter {
	return &FakeTxSubmitter{
		submitted: make([]any, 0),
	}
}

// Submit 提交交易（测试用）
func (s *FakeTxSubmitter) Submit(tx any) (txID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.shouldFail {
		return "", s.failErr
	}

	s.submitted = append(s.submitted, tx)
	s.submitCount++

	// 生成 fake txID
	txID = hex.EncodeToString(sha256.New().Sum([]byte{byte(s.submitCount)}))
	return txID, nil
}

// GetSubmitted 获取所有已提交的交易
func (s *FakeTxSubmitter) GetSubmitted() []any {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]any, len(s.submitted))
	copy(result, s.submitted)
	return result
}

// SubmitCount 获取提交次数
func (s *FakeTxSubmitter) SubmitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.submitCount
}

// SetShouldFail 设置是否应该失败（测试用）
func (s *FakeTxSubmitter) SetShouldFail(fail bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shouldFail = fail
	s.failErr = err
}

// Reset 重置状态
func (s *FakeTxSubmitter) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.submitted = make([]any, 0)
	s.submitCount = 0
	s.shouldFail = false
	s.failErr = nil
}

// SubmitDkgCommitTx 提交 DKG 承诺交易（测试用）
func (s *FakeTxSubmitter) SubmitDkgCommitTx(ctx context.Context, tx *pb.FrostVaultDkgCommitTx) error {
	_, err := s.Submit(tx)
	return err
}

// SubmitDkgShareTx 提交 DKG share 交易（测试用）
func (s *FakeTxSubmitter) SubmitDkgShareTx(ctx context.Context, tx *pb.FrostVaultDkgShareTx) error {
	_, err := s.Submit(tx)
	return err
}

// SubmitDkgValidationSignedTx 提交 DKG 验证签名交易（测试用）
func (s *FakeTxSubmitter) SubmitDkgValidationSignedTx(ctx context.Context, tx *pb.FrostVaultDkgValidationSignedTx) error {
	_, err := s.Submit(tx)
	return err
}

// SubmitWithdrawSignedTx 提交提现签名交易（测试用）
func (s *FakeTxSubmitter) SubmitWithdrawSignedTx(ctx context.Context, tx *pb.FrostWithdrawSignedTx) error {
	_, err := s.Submit(tx)
	return err
}

// FakeLogReporter 用于测试的 fake LogReporter
type FakeLogReporter struct {
	Logs []*pb.FrostWithdrawPlanningLogTx
}

func (s *FakeLogReporter) ReportWithdrawPlanningLog(ctx context.Context, log *pb.FrostWithdrawPlanningLogTx) error {
	s.Logs = append(s.Logs, log)
	return nil
}

// Ensure FakeTxSubmitter implements runtime.TxSubmitter
var _ runtime.TxSubmitter = (*FakeTxSubmitter)(nil)
var _ runtime.LogReporter = (*FakeLogReporter)(nil)
