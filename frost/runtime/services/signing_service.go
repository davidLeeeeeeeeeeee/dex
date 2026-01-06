// frost/runtime/services/signing_service.go
// SigningService: 签名服务接口实现，解耦 ROAST 与执行流程

package services

import (
	"context"
	"errors"
	"sync"
	"time"

	"dex/frost/runtime/roast"
	"dex/frost/runtime/session"
	"dex/frost/runtime/types"
	"dex/pb"
)

// ========== 接口定义 ==========

// SigningService 签名服务接口（解耦 ROAST）
type SigningService interface {
	// StartSigningSession 启动签名会话（异步）
	StartSigningSession(ctx context.Context, params *SigningSessionParams) (sessionID string, err error)

	// GetSessionStatus 查询会话状态
	GetSessionStatus(sessionID string) (*SessionStatus, error)

	// CancelSession 取消会话
	CancelSession(sessionID string) error

	// WaitForCompletion 等待会话完成（阻塞直到完成或超时）
	WaitForCompletion(ctx context.Context, sessionID string, timeout time.Duration) (*SignedPackage, error)
}

// SigningSessionParams 签名会话参数
type SigningSessionParams struct {
	JobID     string
	Chain     string
	VaultID   uint32
	KeyEpoch  uint64
	SignAlgo  pb.SignAlgo
	Messages  [][]byte // 待签名消息列表（BTC 可能是多个 input 的 sighash）
	Threshold int      // 门限 t
}

// SessionStatus 会话状态
type SessionStatus struct {
	SessionID   string
	JobID       string
	State       string  // "INIT" | "COLLECTING_NONCES" | "COLLECTING_SHARES" | "AGGREGATING" | "COMPLETE" | "FAILED"
	Progress    float64 // 0.0 - 1.0
	StartedAt   time.Time
	CompletedAt *time.Time
	Error       error
}

// SignedPackage 签名产物
type SignedPackage struct {
	SessionID    string
	JobID        string
	Signature    []byte // 聚合签名
	RawTx        []byte // 可广播的完整交易（BTC/EVM/SOL/TRX）
	TemplateHash []byte
}

// ========== 实现 ==========

// RoastSigningService ROAST 签名服务实现
type RoastSigningService struct {
	mu sync.RWMutex

	coordinator   *roast.Coordinator
	participant   *roast.Participant
	vaultProvider types.VaultCommitteeProvider

	// 会话管理
	sessions  map[string]*signingSessionWrapper      // sessionID -> wrapper
	callbacks map[string]func(*SignedPackage, error) // sessionID -> callback
}

// signingSessionWrapper 包装 ROAST 会话，提供统一接口
type signingSessionWrapper struct {
	mu sync.RWMutex

	sessionID string
	jobID     string
	params    *SigningSessionParams
	status    *SessionStatus
	callback  func(*SignedPackage, error)

	// 完成通知
	doneCh chan struct{}
	result *SignedPackage
	err    error
}

// NewRoastSigningService 创建 ROAST 签名服务
func NewRoastSigningService(
	coordinator *roast.Coordinator,
	participant *roast.Participant,
	vaultProvider types.VaultCommitteeProvider,
) *RoastSigningService {
	return &RoastSigningService{
		coordinator:   coordinator,
		participant:   participant,
		vaultProvider: vaultProvider,
		sessions:      make(map[string]*signingSessionWrapper),
		callbacks:     make(map[string]func(*SignedPackage, error)),
	}
}

// StartSigningSession 启动签名会话
func (s *RoastSigningService) StartSigningSession(ctx context.Context, params *SigningSessionParams) (string, error) {
	if params == nil {
		return "", errors.New("params is nil")
	}

	sessionID := params.JobID // 使用 jobID 作为 sessionID

	s.mu.Lock()
	defer s.mu.Unlock()

	// 检查是否已存在
	if _, exists := s.sessions[sessionID]; exists {
		return sessionID, nil // 幂等
	}

	// 创建包装器
	wrapper := &signingSessionWrapper{
		sessionID: sessionID,
		jobID:     params.JobID,
		params:    params,
		status: &SessionStatus{
			SessionID: sessionID,
			JobID:     params.JobID,
			State:     "INIT",
			Progress:  0.0,
			StartedAt: time.Now(),
		},
		doneCh: make(chan struct{}),
	}

	s.sessions[sessionID] = wrapper

	// 启动 ROAST 会话
	startParams := &roast.StartSessionParams{
		JobID:     params.JobID,
		VaultID:   params.VaultID,
		Chain:     params.Chain,
		KeyEpoch:  params.KeyEpoch,
		SignAlgo:  params.SignAlgo,
		Messages:  params.Messages,
		Threshold: params.Threshold,
	}

	if err := s.coordinator.StartSession(ctx, startParams); err != nil {
		delete(s.sessions, sessionID)
		return "", err
	}

	// 异步监听会话完成
	go s.monitorSession(ctx, wrapper)

	return sessionID, nil
}

// monitorSession 监听会话状态变化
func (s *RoastSigningService) monitorSession(ctx context.Context, wrapper *signingSessionWrapper) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			wrapper.setError(ctx.Err())
			return
		case <-ticker.C:
			// 检查会话状态
			sess := s.coordinator.GetSession(wrapper.jobID)
			if sess == nil {
				// 会话不存在，可能已完成或失败
				continue
			}

			state := sess.GetState()
			wrapper.updateStatus(state)

			// 检查是否完成
			if state == session.SignSessionStateComplete {
				// 获取签名结果
				signatures := sess.GetFinalSignatures()
				if len(signatures) > 0 {
					// 构建 SignedPackage
					// 注意：RawTx 和 TemplateHash 需要从 Job 中获取，这里先使用签名
					signedPkg := &SignedPackage{
						SessionID:    wrapper.sessionID,
						JobID:        wrapper.jobID,
						Signature:    signatures[0], // 第一个签名（单任务）或合并所有签名（多任务）
						RawTx:        nil,           // 需要从 Job 或 ChainAdapter 获取
						TemplateHash: nil,           // 需要从 Job 获取
					}
					// 如果有多个签名，合并它们
					if len(signatures) > 1 {
						combined := make([]byte, 0)
						for _, sig := range signatures {
							combined = append(combined, sig...)
						}
						signedPkg.Signature = combined
					}
					wrapper.setResult(signedPkg)
				} else {
					wrapper.setError(errors.New("session completed but no signatures"))
				}
				return
			}

			if state == session.SignSessionStateFailed {
				wrapper.setError(errors.New("session failed"))
				return
			}
		}
	}
}

// 注意：Coordinator 已经有 GetSession 方法，直接使用

// updateStatus 更新状态
func (w *signingSessionWrapper) updateStatus(state session.SignSessionState) {
	w.mu.Lock()
	defer w.mu.Unlock()

	var stateStr string
	var progress float64

	switch state {
	case session.SignSessionStateInit:
		stateStr = "INIT"
		progress = 0.0
	case session.SignSessionStateCollectingNonces:
		stateStr = "COLLECTING_NONCES"
		progress = 0.2
	case session.SignSessionStateCollectingShares:
		stateStr = "COLLECTING_SHARES"
		progress = 0.6
	case session.SignSessionStateAggregating:
		stateStr = "AGGREGATING"
		progress = 0.9
	case session.SignSessionStateComplete:
		stateStr = "COMPLETE"
		progress = 1.0
		now := time.Now()
		w.status.CompletedAt = &now
	case session.SignSessionStateFailed:
		stateStr = "FAILED"
		progress = 0.0
	}

	w.status.State = stateStr
	w.status.Progress = progress
}

// setResult 设置结果
func (w *signingSessionWrapper) setResult(result *SignedPackage) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.result = result
	w.status.State = "COMPLETE"
	w.status.Progress = 1.0
	now := time.Now()
	w.status.CompletedAt = &now
	close(w.doneCh)

	if w.callback != nil {
		w.callback(result, nil)
	}
}

// setError 设置错误
func (w *signingSessionWrapper) setError(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.err = err
	w.status.State = "FAILED"
	w.status.Error = err
	close(w.doneCh)

	if w.callback != nil {
		w.callback(nil, err)
	}
}

// GetSessionStatus 查询会话状态
func (s *RoastSigningService) GetSessionStatus(sessionID string) (*SessionStatus, error) {
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

// CancelSession 取消会话
func (s *RoastSigningService) CancelSession(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	wrapper, exists := s.sessions[sessionID]
	if !exists {
		return errors.New("session not found")
	}

	// 标记为失败
	wrapper.setError(errors.New("session cancelled"))

	// 从 Coordinator 取消会话（如果 Coordinator 支持）
	// TODO: 如果 Coordinator 有 CancelSession 方法，调用它

	delete(s.sessions, sessionID)
	return nil
}

// WaitForCompletion 等待会话完成
func (s *RoastSigningService) WaitForCompletion(ctx context.Context, sessionID string, timeout time.Duration) (*SignedPackage, error) {
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

// SetCompletionCallback 设置完成回调
func (s *RoastSigningService) SetCompletionCallback(sessionID string, callback func(*SignedPackage, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	wrapper, exists := s.sessions[sessionID]
	if exists {
		wrapper.mu.Lock()
		wrapper.callback = callback
		wrapper.mu.Unlock()
	} else {
		s.callbacks[sessionID] = callback
	}
}
