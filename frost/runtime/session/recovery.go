// frost/runtime/session/recovery.go
// 会话恢复逻辑：节点重启后从本地存储或链上状态恢复未完成的签名会话

package session

import (
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// ========== 错误定义 ==========

var (
	// ErrRecoveryFailed 恢复失败
	ErrRecoveryFailed = errors.New("session recovery failed")
	// ErrNoSessionsToRecover 没有需要恢复的会话
	ErrNoSessionsToRecover = errors.New("no sessions to recover")
	// ErrSessionExpired 会话已过期（超过恢复窗口）
	ErrSessionExpired = errors.New("session expired")
)

// ========== 持久化会话数据 ==========

// PersistedSession 可持久化的会话数据
type PersistedSession struct {
	JobID        string            `json:"job_id"`
	KeyEpoch     uint64            `json:"key_epoch"`
	Message      []byte            `json:"message"`
	State        SignSessionState  `json:"state"`
	RetryCount   int               `json:"retry_count"`
	StartedAt    time.Time         `json:"started_at"`
	Participants []Participant     `json:"participants"`
	MyIndex      uint16            `json:"my_index"`
	SelectedSet  []uint16          `json:"selected_set"`
	Config       SignSessionConfig `json:"config"`
	// 收集的 nonce 和 share 不持久化（需要重新收集）
}

// ToJSON 序列化为 JSON
func (p *PersistedSession) ToJSON() ([]byte, error) {
	return json.Marshal(p)
}

// FromJSON 从 JSON 反序列化
func FromJSON(data []byte) (*PersistedSession, error) {
	var p PersistedSession
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ========== 会话存储接口 ==========

// SessionStorage 会话持久化存储接口
type SessionStorage interface {
	// SaveSession 保存会话
	SaveSession(session *PersistedSession) error
	// LoadSession 加载指定会话
	LoadSession(jobID string) (*PersistedSession, error)
	// LoadAllSessions 加载所有未完成的会话
	LoadAllSessions() ([]*PersistedSession, error)
	// DeleteSession 删除会话
	DeleteSession(jobID string) error
}

// ========== 内存存储实现 ==========

// MemorySessionStorage 内存会话存储（用于测试）
type MemorySessionStorage struct {
	mu       sync.RWMutex
	sessions map[string]*PersistedSession
}

// NewMemorySessionStorage 创建内存会话存储
func NewMemorySessionStorage() *MemorySessionStorage {
	return &MemorySessionStorage{
		sessions: make(map[string]*PersistedSession),
	}
}

func (m *MemorySessionStorage) SaveSession(session *PersistedSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[session.JobID] = session
	return nil
}

func (m *MemorySessionStorage) LoadSession(jobID string) (*PersistedSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	session, ok := m.sessions[jobID]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return session, nil
}

func (m *MemorySessionStorage) LoadAllSessions() ([]*PersistedSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*PersistedSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		result = append(result, s)
	}
	return result, nil
}

func (m *MemorySessionStorage) DeleteSession(jobID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, jobID)
	return nil
}

// ========== 恢复管理器 ==========

// RecoveryConfig 恢复配置
type RecoveryConfig struct {
	// MaxRecoveryAge 最大恢复窗口（超过此时间的会话不恢复）
	MaxRecoveryAge time.Duration
	// RetryDelay 恢复后重试的延迟
	RetryDelay time.Duration
}

// DefaultRecoveryConfig 默认恢复配置
func DefaultRecoveryConfig() *RecoveryConfig {
	return &RecoveryConfig{
		MaxRecoveryAge: 5 * time.Minute,
		RetryDelay:     1 * time.Second,
	}
}

// RecoveryManager 会话恢复管理器
type RecoveryManager struct {
	storage SessionStorage
	config  *RecoveryConfig
}

// NewRecoveryManager 创建恢复管理器
func NewRecoveryManager(storage SessionStorage, config *RecoveryConfig) *RecoveryManager {
	if config == nil {
		config = DefaultRecoveryConfig()
	}
	return &RecoveryManager{
		storage: storage,
		config:  config,
	}
}

// RecoverSessions 恢复所有未完成的会话
// 返回可恢复的会话列表和过期的会话 ID 列表
func (r *RecoveryManager) RecoverSessions() ([]*ROASTSession, []string, error) {
	persisted, err := r.storage.LoadAllSessions()
	if err != nil {
		return nil, nil, err
	}

	if len(persisted) == 0 {
		return nil, nil, nil
	}

	now := time.Now()
	recovered := make([]*ROASTSession, 0)
	expired := make([]string, 0)

	for _, p := range persisted {
		// 检查是否过期
		age := now.Sub(p.StartedAt)
		if age > r.config.MaxRecoveryAge {
			expired = append(expired, p.JobID)
			continue
		}

		// 只恢复未完成的会话
		if p.State == SignSessionStateComplete || p.State == SignSessionStateFailed {
			// 已完成的会话可以删除
			_ = r.storage.DeleteSession(p.JobID)
			continue
		}

		// 恢复会话
		session := r.restoreSession(p)
		recovered = append(recovered, session)
	}

	// 删除过期会话
	for _, jobID := range expired {
		_ = r.storage.DeleteSession(jobID)
	}

	return recovered, expired, nil
}

// restoreSession 从持久化数据恢复会话
func (r *RecoveryManager) restoreSession(p *PersistedSession) *ROASTSession {
	session := &ROASTSession{
		JobID:        p.JobID,
		KeyEpoch:     p.KeyEpoch,
		Message:      p.Message,
		State:        SignSessionStateInit, // 重置为初始状态，需要重新开始
		RetryCount:   p.RetryCount,
		StartedAt:    p.StartedAt,
		Participants: p.Participants,
		MyIndex:      p.MyIndex,
		SelectedSet:  p.SelectedSet,
		Config:       &p.Config,
		Nonces:       make(map[uint16]*NonceCommitment),
		Shares:       make(map[uint16]*SignatureShare),
	}

	return session
}

// PersistSession 持久化会话
func (r *RecoveryManager) PersistSession(session *ROASTSession) error {
	session.mu.RLock()
	defer session.mu.RUnlock()

	p := &PersistedSession{
		JobID:        session.JobID,
		KeyEpoch:     session.KeyEpoch,
		Message:      session.Message,
		State:        session.State,
		RetryCount:   session.RetryCount,
		StartedAt:    session.StartedAt,
		Participants: session.Participants,
		MyIndex:      session.MyIndex,
		SelectedSet:  session.SelectedSet,
		Config:       *session.Config,
	}

	return r.storage.SaveSession(p)
}

// DeleteSession 删除会话记录
func (r *RecoveryManager) DeleteSession(jobID string) error {
	return r.storage.DeleteSession(jobID)
}

// GetPendingCount 获取待恢复会话数量
func (r *RecoveryManager) GetPendingCount() (int, error) {
	sessions, err := r.storage.LoadAllSessions()
	if err != nil {
		return 0, err
	}

	count := 0
	now := time.Now()
	for _, p := range sessions {
		if p.State != SignSessionStateComplete && p.State != SignSessionStateFailed {
			age := now.Sub(p.StartedAt)
			if age <= r.config.MaxRecoveryAge {
				count++
			}
		}
	}
	return count, nil
}
