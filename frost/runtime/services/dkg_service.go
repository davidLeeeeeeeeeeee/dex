// frost/runtime/services/dkg_service.go
// DKGService: DKG 服务接口实现，解耦 DKG 与执行流程

package services

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"dex/frost/runtime/session"
	"dex/frost/runtime/workers"
	"dex/pb"
)

// ========== 接口定义 ==========

// DKGService DKG 服务接口（解耦 DKG 执行）
type DKGService interface {
	// StartDKGSession 启动 DKG 会话
	StartDKGSession(ctx context.Context, params *DKGSessionParams) (sessionID string, err error)

	// SubmitCommitment 提交承诺（参与者调用）
	SubmitCommitment(ctx context.Context, sessionID string, commitment *DKGCommitment) error

	// SubmitShare 提交加密 share（参与者调用）
	SubmitShare(ctx context.Context, sessionID string, share *DKGShare) error

	// GetDKGStatus 查询 DKG 状态
	GetDKGStatus(sessionID string) (*DKGStatus, error)

	// WaitForCompletion 等待 DKG 完成
	WaitForCompletion(ctx context.Context, sessionID string, timeout time.Duration) (*DKGResult, error)
}

// DKGSessionParams DKG 会话参数
type DKGSessionParams struct {
	SessionID string
	Chain     string
	VaultID   uint32
	EpochID   uint64
	SignAlgo  pb.SignAlgo
	Committee []string // 委员会成员地址列表
	Threshold int      // 门限 t
	MyIndex   int      // 本节点在委员会中的索引 (1-based)
	MyAddress string   // 本节点地址
}

// DKGCommitment DKG 承诺数据
type DKGCommitment struct {
	DealerIndex      int
	DealerAddress    string
	CommitmentPoints [][]byte // Feldman VSS 承诺点
	AI0              []byte   // 第一个承诺点 A_i0
}

// DKGShare DKG 加密 share 数据
type DKGShare struct {
	DealerIndex   int
	DealerAddress string
	ReceiverIndex int
	ReceiverID    string
	Ciphertext    []byte
}

// DKGStatus DKG 状态
type DKGStatus struct {
	SessionID   string
	Chain       string
	VaultID     uint32
	EpochID     uint64
	Phase       string  // "INIT" | "COMMITTING" | "SHARING" | "RESOLVING" | "KEY_READY" | "FAILED"
	Progress    float64 // 0.0 - 1.0
	StartedAt   time.Time
	CompletedAt *time.Time
	Error       error

	// 收集统计
	CommitmentCount int // 已收集的承诺数
	ShareCount      int // 已收集的 share 数
	QualifiedCount  int // 合格的参与者数
}

// DKGResult DKG 结果
type DKGResult struct {
	SessionID   string
	Chain       string
	VaultID     uint32
	EpochID     uint64
	GroupPubkey []byte
	LocalShare  []byte
	CompletedAt time.Time
}

// ========== 实现 ==========

// TransitionDKGService TransitionWorker 的 DKG 服务实现
type TransitionDKGService struct {
	mu sync.RWMutex

	transitionWorker *workers.TransitionWorker
	sessions         map[string]*dkgSessionWrapper // sessionID -> wrapper
}

// dkgSessionWrapper 包装 DKG 会话，提供统一接口
type dkgSessionWrapper struct {
	mu sync.RWMutex

	sessionID  string
	params     *DKGSessionParams
	status     *DKGStatus
	dkgSession *session.DKGSession // 使用 session 包的 DKG 会话

	// 完成通知
	doneCh chan struct{}
	result *DKGResult
	err    error
}

// NewTransitionDKGService 创建 TransitionWorker 的 DKG 服务
func NewTransitionDKGService(transitionWorker *workers.TransitionWorker) *TransitionDKGService {
	return &TransitionDKGService{
		transitionWorker: transitionWorker,
		sessions:         make(map[string]*dkgSessionWrapper),
	}
}

// StartDKGSession 启动 DKG 会话
func (s *TransitionDKGService) StartDKGSession(ctx context.Context, params *DKGSessionParams) (string, error) {
	if params == nil {
		return "", errors.New("params is nil")
	}

	sessionID := params.SessionID
	if sessionID == "" {
		sessionID = generateDKGSessionID(params.Chain, params.VaultID, params.EpochID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 检查是否已存在
	if _, exists := s.sessions[sessionID]; exists {
		return sessionID, nil // 幂等
	}

	// 创建包装器
	wrapper := &dkgSessionWrapper{
		sessionID: sessionID,
		params:    params,
		status: &DKGStatus{
			SessionID:       sessionID,
			Chain:           params.Chain,
			VaultID:         params.VaultID,
			EpochID:         params.EpochID,
			Phase:           "INIT",
			Progress:        0.0,
			StartedAt:       time.Now(),
			CommitmentCount: 0,
			ShareCount:      0,
			QualifiedCount:  0,
		},
		doneCh: make(chan struct{}),
	}

	// 创建 session 包的 DKG 会话
	dkgSessionParams := session.DKGSessionParams{
		SessionID: sessionID,
		Chain:     params.Chain,
		VaultID:   params.VaultID,
		EpochID:   params.EpochID,
		SignAlgo:  int32(params.SignAlgo),
		Committee: params.Committee,
		Threshold: params.Threshold,
		MyIndex:   params.MyIndex,
		MyAddress: params.MyAddress,
	}
	wrapper.dkgSession = session.NewDKGSession(dkgSessionParams)

	s.sessions[sessionID] = wrapper

	// 启动 DKG 会话（通过 TransitionWorker）
	if err := s.transitionWorker.StartSession(ctx, params.Chain, params.VaultID, params.EpochID, params.SignAlgo); err != nil {
		delete(s.sessions, sessionID)
		return "", err
	}

	// 异步监听会话状态
	go s.monitorSession(ctx, wrapper)

	return sessionID, nil
}

// generateDKGSessionID 生成 DKG 会话 ID
func generateDKGSessionID(chain string, vaultID uint32, epochID uint64) string {
	return fmt.Sprintf("%s_%d_%d", chain, vaultID, epochID)
}

// monitorSession 监听会话状态变化
func (s *TransitionDKGService) monitorSession(ctx context.Context, wrapper *dkgSessionWrapper) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			wrapper.setError(ctx.Err())
			return
		case <-ticker.C:
			// 检查会话状态
			wrapper.mu.RLock()
			dkgSession := wrapper.dkgSession
			wrapper.mu.RUnlock()

			if dkgSession == nil {
				continue
			}

			phase := dkgSession.GetPhase()
			wrapper.updateStatus(phase)

			// 检查是否完成
			if phase == session.DKGPhaseKeyReady {
				// 获取结果（使用 getter 方法）
				localShare := dkgSession.GetLocalShare()
				groupPubkey := dkgSession.GetGroupPubkey()

				if localShare != nil && len(localShare) > 0 && groupPubkey != nil && len(groupPubkey) > 0 {
					result := &DKGResult{
						SessionID:   wrapper.sessionID,
						Chain:       wrapper.params.Chain,
						VaultID:     wrapper.params.VaultID,
						EpochID:     wrapper.params.EpochID,
						GroupPubkey: groupPubkey,
						LocalShare:  localShare,
						CompletedAt: time.Now(),
					}
					wrapper.setResult(result)
				} else {
					wrapper.setError(errors.New("DKG completed but no result"))
				}
				return
			}

			if phase == session.DKGPhaseFailed {
				wrapper.setError(errors.New("DKG failed"))
				return
			}
		}
	}
}

// updateStatus 更新状态
func (w *dkgSessionWrapper) updateStatus(phase session.DKGPhase) {
	w.mu.Lock()
	defer w.mu.Unlock()

	var phaseStr string
	var progress float64

	switch phase {
	case session.DKGPhaseInit:
		phaseStr = "INIT"
		progress = 0.0
	case session.DKGPhaseCommitting:
		phaseStr = "COMMITTING"
		progress = 0.2
	case session.DKGPhaseSharing:
		phaseStr = "SHARING"
		progress = 0.5
	case session.DKGPhaseResolving:
		phaseStr = "RESOLVING"
		progress = 0.8
	case session.DKGPhaseKeyReady:
		phaseStr = "KEY_READY"
		progress = 1.0
		now := time.Now()
		w.status.CompletedAt = &now
	case session.DKGPhaseFailed:
		phaseStr = "FAILED"
		progress = 0.0
	}

	w.status.Phase = phaseStr
	w.status.Progress = progress

	// 更新收集统计
	if w.dkgSession != nil {
		commitments := w.dkgSession.GetAllCommitments()
		shares := w.dkgSession.GetAllShares()
		w.status.CommitmentCount = len(commitments)
		w.status.ShareCount = len(shares)
	}
}

// setResult 设置结果
func (w *dkgSessionWrapper) setResult(result *DKGResult) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.result = result
	w.status.Phase = "KEY_READY"
	w.status.Progress = 1.0
	now := time.Now()
	w.status.CompletedAt = &now
	close(w.doneCh)
}

// setError 设置错误
func (w *dkgSessionWrapper) setError(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.err = err
	w.status.Phase = "FAILED"
	w.status.Error = err
	close(w.doneCh)
}

// SubmitCommitment 提交承诺
func (s *TransitionDKGService) SubmitCommitment(ctx context.Context, sessionID string, commitment *DKGCommitment) error {
	s.mu.RLock()
	wrapper, exists := s.sessions[sessionID]
	s.mu.RUnlock()

	if !exists {
		return errors.New("session not found")
	}

	// 通过 TransitionWorker 提交（实际应该通过链上 tx）
	// 这里简化处理，直接更新本地会话状态
	wrapper.mu.Lock()
	if wrapper.dkgSession != nil {
		// 添加承诺到会话
		wrapper.dkgSession.AddCommitment(commitment.DealerIndex, commitment.DealerAddress, commitment.CommitmentPoints, commitment.AI0)
	}
	wrapper.mu.Unlock()

	return nil
}

// SubmitShare 提交加密 share
func (s *TransitionDKGService) SubmitShare(ctx context.Context, sessionID string, share *DKGShare) error {
	s.mu.RLock()
	wrapper, exists := s.sessions[sessionID]
	s.mu.RUnlock()

	if !exists {
		return errors.New("session not found")
	}

	// 通过 TransitionWorker 提交（实际应该通过链上 tx）
	// 这里简化处理，直接更新本地会话状态
	wrapper.mu.Lock()
	if wrapper.dkgSession != nil {
		// 添加 share 到会话
		wrapper.dkgSession.AddShare(share.DealerIndex, share.DealerAddress, share.Ciphertext)
	}
	wrapper.mu.Unlock()

	return nil
}

// GetDKGStatus 查询 DKG 状态
func (s *TransitionDKGService) GetDKGStatus(sessionID string) (*DKGStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	wrapper, exists := s.sessions[sessionID]
	if !exists {
		return nil, errors.New("session not found")
	}

	wrapper.mu.RLock()
	defer wrapper.mu.RUnlock()

	// 返回状态副本
	status := *wrapper.status
	return &status, nil
}

// WaitForCompletion 等待 DKG 完成
func (s *TransitionDKGService) WaitForCompletion(ctx context.Context, sessionID string, timeout time.Duration) (*DKGResult, error) {
	s.mu.RLock()
	wrapper, exists := s.sessions[sessionID]
	s.mu.RUnlock()

	if !exists {
		return nil, errors.New("session not found")
	}

	// 设置超时
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-wrapper.doneCh:
		wrapper.mu.RLock()
		defer wrapper.mu.RUnlock()

		if wrapper.err != nil {
			return nil, wrapper.err
		}
		return wrapper.result, nil
	}
}
