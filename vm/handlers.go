package vm

import (
	"errors"
	"fmt"
	"sync"
)

// HandlerRegistry Handler注册表
type HandlerRegistry struct {
	mu sync.RWMutex
	m  map[string]TxHandler
}

// NewHandlerRegistry 创建新的注册表
func NewHandlerRegistry() *HandlerRegistry {
	return &HandlerRegistry{m: make(map[string]TxHandler)}
}

// 注册Handler
func (r *HandlerRegistry) Register(h TxHandler) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if h == nil {
		return errors.New("nil handler")
	}

	kind := h.Kind()
	if kind == "" {
		return errors.New("empty handler kind")
	}

	if _, ok := r.m[kind]; ok {
		return fmt.Errorf("duplicate handler kind: %s", kind)
	}
	r.m[kind] = h
	return nil
}

// Get 获取Handler
func (r *HandlerRegistry) Get(kind string) (TxHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.m[kind]
	return h, ok
}

// List 列出所有已注册的Handler类型
func (r *HandlerRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	kinds := make([]string, 0, len(r.m))
	for k := range r.m {
		kinds = append(kinds, k)
	}
	return kinds
}

// DefaultKindFn 默认的KindFn实现
func DefaultKindFn(tx *AnyTx) (string, error) {
	if tx == nil {
		return "", ErrNilTx
	}
	if tx.Type != "" {
		return tx.Type, nil
	}
	if tx.Kind != "" {
		return tx.Kind, nil
	}
	return "", fmt.Errorf("cannot infer tx kind: %v", tx.TxID)
}
