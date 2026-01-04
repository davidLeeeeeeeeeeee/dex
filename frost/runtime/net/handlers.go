// frost/runtime/net/handlers.go
// Frost P2P 消息处理器

package net

import (
	"dex/logs"
	"dex/pb"
)

// DefaultRouter 默认全局路由器
var DefaultRouter = NewRouter()

// RegisterDefaultHandlers 注册默认的消息处理器（占位）
func RegisterDefaultHandlers(r *Router) {
	// DKG 第 1 轮
	r.Register(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_DKG_ROUND1, func(env *pb.FrostEnvelope) error {
		logs.Debug("[FrostRouter] received DKG_ROUND1 from=%s, job=%s", env.From, env.JobId)
		// TODO: 转发给 DKG Coordinator
		return nil
	})

	// DKG 第 2 轮
	r.Register(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_DKG_ROUND2, func(env *pb.FrostEnvelope) error {
		logs.Debug("[FrostRouter] received DKG_ROUND2 from=%s, job=%s", env.From, env.JobId)
		// TODO: 转发给 DKG Coordinator
		return nil
	})

	// 签名 nonce 承诺
	r.Register(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_SIGN_NONCE, func(env *pb.FrostEnvelope) error {
		logs.Debug("[FrostRouter] received SIGN_NONCE from=%s, job=%s", env.From, env.JobId)
		// TODO: 转发给 SignSession
		return nil
	})

	// 签名份额
	r.Register(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_SIGN_SHARE, func(env *pb.FrostEnvelope) error {
		logs.Debug("[FrostRouter] received SIGN_SHARE from=%s, job=%s", env.From, env.JobId)
		// TODO: 转发给 SignSession
		return nil
	})

	// ROAST 签名请求
	r.Register(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_ROAST_REQUEST, func(env *pb.FrostEnvelope) error {
		logs.Debug("[FrostRouter] received ROAST_REQUEST from=%s, job=%s", env.From, env.JobId)
		// TODO: 转发给 ROAST Coordinator
		return nil
	})

	// ROAST 响应
	r.Register(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_ROAST_RESPONSE, func(env *pb.FrostEnvelope) error {
		logs.Debug("[FrostRouter] received ROAST_RESPONSE from=%s, job=%s", env.From, env.JobId)
		// TODO: 转发给 ROAST Coordinator
		return nil
	})
}

// SetDKGHandler 设置 DKG 处理器
func SetDKGHandler(r *Router, round1Handler, round2Handler FrostMsgHandler) {
	if round1Handler != nil {
		r.Register(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_DKG_ROUND1, round1Handler)
	}
	if round2Handler != nil {
		r.Register(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_DKG_ROUND2, round2Handler)
	}
}

// SetSignHandler 设置签名处理器
func SetSignHandler(r *Router, nonceHandler, shareHandler FrostMsgHandler) {
	if nonceHandler != nil {
		r.Register(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_SIGN_NONCE, nonceHandler)
	}
	if shareHandler != nil {
		r.Register(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_SIGN_SHARE, shareHandler)
	}
}

// SetROASTHandler 设置 ROAST 处理器
func SetROASTHandler(r *Router, requestHandler, responseHandler FrostMsgHandler) {
	if requestHandler != nil {
		r.Register(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_ROAST_REQUEST, requestHandler)
	}
	if responseHandler != nil {
		r.Register(pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_ROAST_RESPONSE, responseHandler)
	}
}
