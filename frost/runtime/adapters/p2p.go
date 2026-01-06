// frost/runtime/adapters/p2p.go
// P2P 适配器实现（基于 Transport）
package adapters

import (
	"dex/frost/runtime"
	"dex/pb"
	"dex/types"
	"fmt"

	"google.golang.org/protobuf/proto"
)

// Transport 网络传输接口（由外部实现）
type Transport interface {
	Send(to types.NodeID, msg types.Message) error
	Broadcast(msg types.Message, peers []types.NodeID)
	SamplePeers(exclude types.NodeID, count int) []types.NodeID
}

// TransportP2P 基于 Transport 的 P2P 实现
type TransportP2P struct {
	transport Transport
	localID   runtime.NodeID
}

// NewTransportP2P 创建新的 TransportP2P
func NewTransportP2P(transport Transport, localID runtime.NodeID) *TransportP2P {
	return &TransportP2P{
		transport: transport,
		localID:   localID,
	}
}

// Send 发送消息到指定节点
func (p *TransportP2P) Send(to runtime.NodeID, msg *runtime.FrostEnvelope) error {
	if p.transport == nil {
		return fmt.Errorf("transport not available")
	}

	// 转换 runtime.FrostEnvelope 到 types.Message
	transportMsg, err := p.toTransportMessage(msg)
	if err != nil {
		return fmt.Errorf("convert to transport message: %w", err)
	}

	return p.transport.Send(types.NodeID(to), transportMsg)
}

// Broadcast 广播消息到多个节点
func (p *TransportP2P) Broadcast(peers []runtime.NodeID, msg *runtime.FrostEnvelope) error {
	if p.transport == nil {
		return fmt.Errorf("transport not available")
	}

	// 转换 runtime.FrostEnvelope 到 types.Message
	transportMsg, err := p.toTransportMessage(msg)
	if err != nil {
		return fmt.Errorf("convert to transport message: %w", err)
	}

	// 转换 peers
	transportPeers := make([]types.NodeID, len(peers))
	for i, peer := range peers {
		transportPeers[i] = types.NodeID(peer)
	}

	p.transport.Broadcast(transportMsg, transportPeers)
	return nil
}

// SamplePeers 采样节点
func (p *TransportP2P) SamplePeers(n int, role string) []runtime.NodeID {
	if p.transport == nil {
		return nil
	}

	transportPeers := p.transport.SamplePeers(types.NodeID(p.localID), n)
	result := make([]runtime.NodeID, len(transportPeers))
	for i, peer := range transportPeers {
		result[i] = runtime.NodeID(peer)
	}
	return result
}

// toTransportMessage 转换 runtime.FrostEnvelope 到 types.Message
func (p *TransportP2P) toTransportMessage(env *runtime.FrostEnvelope) (types.Message, error) {
	// 构建 pb.FrostEnvelope
	pbEnv := &pb.FrostEnvelope{
		From: string(env.From),
		Kind: p.toPBEnvelopeKind(env.Kind),
		Payload: env.Payload,
		Sig: env.Sig,
		JobId: env.SessionID,
	}

	// 序列化
	data, err := proto.Marshal(pbEnv)
	if err != nil {
		return types.Message{}, fmt.Errorf("marshal envelope: %w", err)
	}

	// 创建 types.Message
	msg := types.Message{
		Type:        types.MsgFrost,
		From:        types.NodeID(env.From),
		FrostPayload: data,
	}
	return msg, nil
}

// toPBEnvelopeKind 转换消息类型
func (p *TransportP2P) toPBEnvelopeKind(kind string) pb.FrostEnvelopeKind {
	switch kind {
	case "NonceCommit", "NonceRequest":
		return pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_ROAST_REQUEST
	case "SigShare", "SignRequest":
		return pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_ROAST_RESPONSE
	default:
		return pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_UNSPECIFIED
	}
}

// Ensure TransportP2P implements runtime.P2P
var _ runtime.P2P = (*TransportP2P)(nil)
