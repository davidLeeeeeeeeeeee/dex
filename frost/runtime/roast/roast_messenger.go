// frost/runtime/roast/roast_messenger.go
// RoastMessenger abstracts ROAST message transport from Coordinator/Participant.

package roast

import (
	"dex/frost/runtime/types"
	"dex/pb"
)

// Envelope is the transport-agnostic ROAST message.
type Envelope struct {
	SessionID string
	Kind      string
	From      NodeID
	Chain     string
	VaultID   uint32
	SignAlgo  pb.SignAlgo
	Epoch     uint64
	Round     uint32
	Payload   []byte
}

// P2P 网络接口（用于适配）
type P2P interface {
	Send(to NodeID, msg *FrostEnvelope) error
	Broadcast(peers []NodeID, msg *FrostEnvelope) error
	SamplePeers(n int, role string) []NodeID
}

// FrostEnvelope P2P 消息封装（用于适配runtime.FrostEnvelope）
type FrostEnvelope struct {
	SessionID string
	Kind      string
	From      NodeID
	Chain     string
	VaultID   uint32
	SignAlgo  int32
	Epoch     uint64
	Round     uint32
	Payload   []byte
	Sig       []byte
}

// P2PMessenger adapts RoastMessenger to the existing P2P transport.
type P2PMessenger struct {
	p2p P2P
}

// NewP2PMessenger creates a messenger that uses the P2P transport.
func NewP2PMessenger(p2p P2P) *P2PMessenger {
	return &P2PMessenger{p2p: p2p}
}

// Send sends a ROAST message to a single peer.
func (m *P2PMessenger) Send(to NodeID, msg *Envelope) error {
	if m == nil || m.p2p == nil || msg == nil {
		return nil
	}
	return m.p2p.Send(to, toFrostEnvelope(msg))
}

// Broadcast sends a ROAST message to multiple peers.
func (m *P2PMessenger) Broadcast(peers []NodeID, msg *Envelope) error {
	if m == nil || m.p2p == nil || msg == nil {
		return nil
	}
	return m.p2p.Broadcast(peers, toFrostEnvelope(msg))
}

// FromFrostEnvelope converts a transport envelope to Envelope.
func FromFrostEnvelope(env *FrostEnvelope) *Envelope {
	if env == nil {
		return nil
	}
	return &Envelope{
		SessionID: env.SessionID,
		Kind:      env.Kind,
		From:      env.From,
		Chain:     env.Chain,
		VaultID:   env.VaultID,
		SignAlgo:  pb.SignAlgo(env.SignAlgo),
		Epoch:     env.Epoch,
		Round:     env.Round,
		Payload:   env.Payload,
	}
}

func toFrostEnvelope(msg *Envelope) *FrostEnvelope {
	if msg == nil {
		return nil
	}
	return &FrostEnvelope{
		SessionID: msg.SessionID,
		Kind:      msg.Kind,
		From:      msg.From,
		Chain:     msg.Chain,
		VaultID:   msg.VaultID,
		SignAlgo:  int32(msg.SignAlgo),
		Epoch:     msg.Epoch,
		Round:     msg.Round,
		Payload:   msg.Payload,
	}
}

// toTypesRoastEnvelope 将 roast.Envelope 转换为 types.RoastEnvelope
func toTypesRoastEnvelope(msg *Envelope) *types.RoastEnvelope {
	if msg == nil {
		return nil
	}
	return &types.RoastEnvelope{
		SessionID: msg.SessionID,
		Kind:      msg.Kind,
		From:      msg.From,
		Chain:     msg.Chain,
		VaultID:   msg.VaultID,
		SignAlgo:  int32(msg.SignAlgo),
		Epoch:     msg.Epoch,
		Round:     msg.Round,
		Payload:   msg.Payload,
	}
}
