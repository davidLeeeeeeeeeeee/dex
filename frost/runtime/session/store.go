// frost/runtime/session/store.go
// SessionStore: nonce 状态持久化与管理

package session

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
)

// ========== 错误定义 ==========

var (
	// ErrNonceAlreadyBound nonce 已经绑定到另一个消息
	ErrNonceAlreadyBound = errors.New("nonce already bound to another message")
	// ErrNonceNotFound nonce 不存在
	ErrNonceNotFound = errors.New("nonce not found")
	// ErrNonceExpired nonce 已过期
	ErrNonceExpired = errors.New("nonce expired")
	// ErrSessionNotFound 会话不存在
	ErrSessionNotFound = errors.New("session not found")
)

// ========== 存储接口 ==========

// NonceStore nonce 持久化存储接口
type NonceStore interface {
	// SaveNonce 保存 nonce 绑定
	// nonce: 32 字节 nonce 值
	// msgHash: 绑定的消息哈希
	// keyEpoch: 密钥版本
	SaveNonce(nonce []byte, msgHash []byte, keyEpoch uint64) error

	// GetNonceBind 获取 nonce 绑定信息
	// 返回: (msgHash, keyEpoch, exists)
	GetNonceBind(nonce []byte) (msgHash []byte, keyEpoch uint64, exists bool)

	// DeleteNonce 删除 nonce 绑定
	DeleteNonce(nonce []byte) error
}

// ========== 内存实现 ==========

// nonceBind nonce 绑定信息
type nonceBind struct {
	MsgHash  []byte
	KeyEpoch uint64
}

// MemoryNonceStore 内存 nonce 存储
type MemoryNonceStore struct {
	mu     sync.RWMutex
	nonces map[string]*nonceBind // key = hex(nonce)
}

// NewMemoryNonceStore 创建内存 nonce 存储
func NewMemoryNonceStore() *MemoryNonceStore {
	return &MemoryNonceStore{
		nonces: make(map[string]*nonceBind),
	}
}

func (m *MemoryNonceStore) SaveNonce(nonce []byte, msgHash []byte, keyEpoch uint64) error {
	key := hex.EncodeToString(nonce)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nonces[key] = &nonceBind{MsgHash: msgHash, KeyEpoch: keyEpoch}
	return nil
}

func (m *MemoryNonceStore) GetNonceBind(nonce []byte) ([]byte, uint64, bool) {
	key := hex.EncodeToString(nonce)
	m.mu.RLock()
	defer m.mu.RUnlock()
	bind, ok := m.nonces[key]
	if !ok {
		return nil, 0, false
	}
	return bind.MsgHash, bind.KeyEpoch, true
}

func (m *MemoryNonceStore) DeleteNonce(nonce []byte) error {
	key := hex.EncodeToString(nonce)
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.nonces, key)
	return nil
}

// ========== SessionStore ==========

// SessionStore 会话存储，管理 nonce 和签名会话
type SessionStore struct {
	mu         sync.RWMutex
	nonceStore NonceStore
}

// NewSessionStore 创建会话存储
func NewSessionStore(nonceStore NonceStore) *SessionStore {
	if nonceStore == nil {
		nonceStore = NewMemoryNonceStore()
	}
	return &SessionStore{
		nonceStore: nonceStore,
	}
}

// BindNonce 绑定 nonce 到消息
// 如果 nonce 已绑定到不同消息，返回 ErrNonceAlreadyBound
// 如果 nonce 已绑定到相同消息，返回 nil（幂等）
func (s *SessionStore) BindNonce(nonce []byte, msg []byte, keyEpoch uint64) error {
	msgHash := hashMessage(msg)

	// 检查是否已绑定
	existingHash, _, exists := s.nonceStore.GetNonceBind(nonce)
	if exists {
		// 检查是否绑定到相同消息
		if bytesEqual(existingHash, msgHash) {
			return nil // 幂等
		}
		return ErrNonceAlreadyBound
	}

	// 保存绑定
	return s.nonceStore.SaveNonce(nonce, msgHash, keyEpoch)
}

// GetNonceBind 获取 nonce 绑定的消息哈希
func (s *SessionStore) GetNonceBind(nonce []byte) ([]byte, uint64, bool) {
	return s.nonceStore.GetNonceBind(nonce)
}

// ValidateNonceForMessage 验证 nonce 是否可用于指定消息
// 如果 nonce 未绑定，返回 true
// 如果 nonce 已绑定到相同消息，返回 true
// 如果 nonce 已绑定到不同消息，返回 false
func (s *SessionStore) ValidateNonceForMessage(nonce []byte, msg []byte) bool {
	msgHash := hashMessage(msg)
	existingHash, _, exists := s.nonceStore.GetNonceBind(nonce)
	if !exists {
		return true // 未绑定，可用
	}
	return bytesEqual(existingHash, msgHash)
}

// ReleaseNonce 释放 nonce 绑定
func (s *SessionStore) ReleaseNonce(nonce []byte) error {
	return s.nonceStore.DeleteNonce(nonce)
}

// ========== 辅助函数 ==========

func hashMessage(msg []byte) []byte {
	h := sha256.Sum256(msg)
	return h[:]
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
