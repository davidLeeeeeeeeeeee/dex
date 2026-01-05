// frost/runtime/roast_messenger.go
// RoastMessenger abstracts ROAST message transport from Coordinator/Participant.

package runtime

import "dex/pb"

// RoastEnvelope is the transport-agnostic ROAST message.
type RoastEnvelope struct {
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

// RoastMessenger sends ROAST messages without exposing the underlying transport.
type RoastMessenger interface {
	Send(to NodeID, msg *RoastEnvelope) error
	Broadcast(peers []NodeID, msg *RoastEnvelope) error
}

// P2PRoastMessenger adapts RoastMessenger to the existing P2P transport.
type P2PRoastMessenger struct {
	p2p P2P
}

// NewP2PRoastMessenger creates a messenger that uses the P2P transport.
func NewP2PRoastMessenger(p2p P2P) *P2PRoastMessenger {
	return &P2PRoastMessenger{p2p: p2p}
}

// Send sends a ROAST message to a single peer.
func (m *P2PRoastMessenger) Send(to NodeID, msg *RoastEnvelope) error {
	if m == nil || m.p2p == nil || msg == nil {
		return nil
	}
	return m.p2p.Send(to, toFrostEnvelope(msg))
}

// Broadcast sends a ROAST message to multiple peers.
func (m *P2PRoastMessenger) Broadcast(peers []NodeID, msg *RoastEnvelope) error {
	if m == nil || m.p2p == nil || msg == nil {
		return nil
	}
	return m.p2p.Broadcast(peers, toFrostEnvelope(msg))
}

// FromFrostEnvelope converts a transport envelope to RoastEnvelope.
func FromFrostEnvelope(env *FrostEnvelope) *RoastEnvelope {
	if env == nil {
		return nil
	}
	return &RoastEnvelope{
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

func toFrostEnvelope(msg *RoastEnvelope) *FrostEnvelope {
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
