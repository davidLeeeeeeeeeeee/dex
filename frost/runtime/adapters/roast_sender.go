package adapters

import (
	"dex/frost/runtime"
	"dex/sender"
)

// SenderRoastMessenger adapts SenderManager to RoastMessenger.
type SenderRoastMessenger struct {
	sender *sender.SenderManager
}

// NewSenderRoastMessenger creates a RoastMessenger backed by SenderManager.
func NewSenderRoastMessenger(senderMgr *sender.SenderManager) *SenderRoastMessenger {
	return &SenderRoastMessenger{sender: senderMgr}
}

// Send sends a ROAST message to a single peer.
func (m *SenderRoastMessenger) Send(to runtime.NodeID, msg *runtime.RoastEnvelope) error {
	if m == nil || m.sender == nil || msg == nil {
		return nil
	}
	env, err := runtime.PBEnvelopeFromRoast(msg)
	if err != nil {
		return err
	}
	return m.sender.SendFrostToAddress(string(to), env)
}

// Broadcast sends a ROAST message to multiple peers.
func (m *SenderRoastMessenger) Broadcast(peers []runtime.NodeID, msg *runtime.RoastEnvelope) error {
	if m == nil || m.sender == nil || msg == nil {
		return nil
	}
	env, err := runtime.PBEnvelopeFromRoast(msg)
	if err != nil {
		return err
	}
	for _, peer := range peers {
		if err := m.sender.SendFrostToAddress(string(peer), env); err != nil {
			return err
		}
	}
	return nil
}
