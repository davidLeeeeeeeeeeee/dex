// frost/runtime/net/msg_test.go
// Router 单元测试

package net

import (
	"dex/pb"
	"testing"
)

func TestRouter_RegisterAndRoute(t *testing.T) {
	r := NewRouter()

	// 记录调用
	called := false
	receivedEnv := (*pb.FrostEnvelope)(nil)

	// 注册处理器
	r.Register(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_SIGN_NONCE, func(env *pb.FrostEnvelope) error {
		called = true
		receivedEnv = env
		return nil
	})

	// 验证注册成功
	if !r.HasHandler(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_SIGN_NONCE) {
		t.Error("Handler should be registered")
	}

	// 路由消息
	testEnv := &pb.FrostEnvelope{
		From:  "sender_1",
		To:    "receiver_1",
		Kind:  pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_SIGN_NONCE,
		JobId: "job_123",
	}

	err := r.Route(testEnv)
	if err != nil {
		t.Errorf("Route should not fail: %v", err)
	}

	// 验证处理器被调用
	if !called {
		t.Error("Handler should be called")
	}
	if receivedEnv != testEnv {
		t.Error("Handler should receive the same envelope")
	}
}

func TestRouter_NoHandler(t *testing.T) {
	r := NewRouter()

	testEnv := &pb.FrostEnvelope{
		Kind: pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_DKG_ROUND1,
	}

	err := r.Route(testEnv)
	if err != ErrNoHandlerRegistered {
		t.Errorf("Expected ErrNoHandlerRegistered, got %v", err)
	}
}

func TestRouter_Unregister(t *testing.T) {
	r := NewRouter()

	// 注册
	r.Register(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_SIGN_SHARE, func(env *pb.FrostEnvelope) error {
		return nil
	})

	if !r.HasHandler(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_SIGN_SHARE) {
		t.Error("Handler should be registered")
	}

	// 注销
	r.Unregister(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_SIGN_SHARE)

	if r.HasHandler(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_SIGN_SHARE) {
		t.Error("Handler should be unregistered")
	}
}

func TestRouter_RegisteredKinds(t *testing.T) {
	r := NewRouter()

	r.Register(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_DKG_ROUND1, func(env *pb.FrostEnvelope) error { return nil })
	r.Register(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_DKG_ROUND2, func(env *pb.FrostEnvelope) error { return nil })
	r.Register(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_SIGN_NONCE, func(env *pb.FrostEnvelope) error { return nil })

	kinds := r.RegisteredKinds()
	if len(kinds) != 3 {
		t.Errorf("Expected 3 kinds, got %d", len(kinds))
	}
}

func TestRouter_DefaultHandlers(t *testing.T) {
	r := NewRouter()
	RegisterDefaultHandlers(r)

	// 验证所有默认处理器都已注册
	expectedKinds := []pb.FrostEnvelopeKind{
		pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_DKG_ROUND1,
		pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_DKG_ROUND2,
		pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_SIGN_NONCE,
		pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_SIGN_SHARE,
		pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_ROAST_REQUEST,
		pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_ROAST_RESPONSE,
	}

	for _, kind := range expectedKinds {
		if !r.HasHandler(kind) {
			t.Errorf("Missing default handler for %v", kind)
		}
	}
}
