// frost/runtime/net/msg.go
// FrostEnvelope 路由分发

package net

import (
	"dex/pb"
	"errors"
	"sync"

	"google.golang.org/protobuf/proto"
)

// ErrNoHandlerRegistered 没有注册处理器
var ErrNoHandlerRegistered = errors.New("no handler registered for this kind")

// FrostMsgHandler FrostEnvelope 处理器函数
type FrostMsgHandler func(envelope *pb.FrostEnvelope) error

// Router FrostEnvelope 路由器，按 Kind 分发消息
type Router struct {
	mu       sync.RWMutex
	handlers map[pb.FrostEnvelopeKind]FrostMsgHandler
}

// NewRouter 创建新的路由器
func NewRouter() *Router {
	return &Router{
		handlers: make(map[pb.FrostEnvelopeKind]FrostMsgHandler),
	}
}

// Register 注册指定 Kind 的处理器
func (r *Router) Register(kind pb.FrostEnvelopeKind, handler FrostMsgHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[kind] = handler
}

// Unregister 注销指定 Kind 的处理器
func (r *Router) Unregister(kind pb.FrostEnvelopeKind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.handlers, kind)
}

// Route 路由 FrostEnvelope 到对应的处理器
func (r *Router) Route(envelope *pb.FrostEnvelope) error {
	r.mu.RLock()
	handler, ok := r.handlers[envelope.Kind]
	r.mu.RUnlock()

	if !ok {
		return ErrNoHandlerRegistered
	}

	return handler(envelope)
}

// RouteFromBytes 从序列化数据路由
func (r *Router) RouteFromBytes(data []byte) error {
	var envelope pb.FrostEnvelope
	if err := proto.Unmarshal(data, &envelope); err != nil {
		return err
	}
	return r.Route(&envelope)
}

// HasHandler 检查是否有指定 Kind 的处理器
func (r *Router) HasHandler(kind pb.FrostEnvelopeKind) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.handlers[kind]
	return ok
}

// RegisteredKinds 返回所有已注册的 Kind
func (r *Router) RegisteredKinds() []pb.FrostEnvelopeKind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	kinds := make([]pb.FrostEnvelopeKind, 0, len(r.handlers))
	for k := range r.handlers {
		kinds = append(kinds, k)
	}
	return kinds
}
