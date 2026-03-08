package roast

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	roastsession "dex/frost/runtime/session"
	"dex/frost/runtime/types"
	"dex/logs"
	"dex/pb"
)

type fixedCommitteeVaultProvider struct {
	committee   []SignerInfo
	groupPubkey []byte
}

func (p *fixedCommitteeVaultProvider) VaultCommittee(string, uint32, uint64) ([]SignerInfo, error) {
	return p.committee, nil
}

func (p *fixedCommitteeVaultProvider) VaultCurrentEpoch(string, uint32) uint64 { return 1 }

func (p *fixedCommitteeVaultProvider) VaultGroupPubkey(string, uint32, uint64) ([]byte, error) {
	return append([]byte(nil), p.groupPubkey...), nil
}

func (p *fixedCommitteeVaultProvider) CalculateThreshold(string, uint32) (int, error) { return 0, nil }

type recordingMessenger struct {
	mu             sync.Mutex
	sendKinds      []string
	broadcastKinds []string
}

func (m *recordingMessenger) Send(_ NodeID, msg *types.RoastEnvelope) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sendKinds = append(m.sendKinds, msg.Kind)
	return nil
}

func (m *recordingMessenger) Broadcast(_ []NodeID, msg *types.RoastEnvelope) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.broadcastKinds = append(m.broadcastKinds, msg.Kind)
	return nil
}

func (m *recordingMessenger) sendCount(kind string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, got := range m.sendKinds {
		if got == kind {
			count++
		}
	}
	return count
}

func (m *recordingMessenger) broadcastCount(kind string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, got := range m.broadcastKinds {
		if got == kind {
			count++
		}
	}
	return count
}

func TestParticipantHandleRoastSignRequestResendsCachedShares(t *testing.T) {
	messenger := &recordingMessenger{}
	vaultProvider := &fixedCommitteeVaultProvider{
		committee: []SignerInfo{
			{ID: "node-a", Index: 0, Weight: 1},
		},
	}

	participant := NewParticipant(
		"node-a",
		messenger,
		vaultProvider,
		&mockCryptoFactory{},
		nil,
		nil,
		logs.NewNodeLogger("participant-idempotency-test", 0),
	)

	jobID := "job-dup-sign"
	participant.sessions[jobID] = &ParticipantSession{
		JobID:           jobID,
		VaultID:         1,
		Chain:           "btc",
		KeyEpoch:        1,
		SignAlgo:        pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
		State:           ParticipantStateShareGenerated,
		GeneratedShares: [][]byte{bytes.Repeat([]byte{0x42}, 32)},
	}

	env := &Envelope{
		SessionID: jobID,
		Kind:      "SignRequest",
		From:      "node-a",
		Chain:     "btc",
		VaultID:   1,
		SignAlgo:  pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
		Epoch:     1,
		Payload:   encodeRoastRequestPayload([][]byte{{0x01}}, []byte{0x02}),
	}

	if err := participant.HandleRoastSignRequest(env); err != nil {
		t.Fatalf("first duplicate HandleRoastSignRequest failed: %v", err)
	}
	if err := participant.HandleRoastSignRequest(env); err != nil {
		t.Fatalf("second duplicate HandleRoastSignRequest failed: %v", err)
	}

	if got := messenger.sendCount("SigShare"); got != 2 {
		t.Fatalf("duplicate sign request should resend cached shares twice: got=%d want=2", got)
	}
}

func TestCoordinatorHandleRoastSigShareIgnoresLateDuplicate(t *testing.T) {
	vaultProvider := &fixedCommitteeVaultProvider{
		committee: []SignerInfo{
			{ID: "node-a", Index: 0, Weight: 1},
		},
	}
	coord := NewCoordinator(
		"node-a",
		nil,
		vaultProvider,
		nil,
		logs.NewNodeLogger("coordinator-idempotency-test", 0),
	)

	jobID := "job-late-share"
	sess := roastsession.NewSession(roastsession.SessionParams{
		JobID:     jobID,
		VaultID:   1,
		Chain:     "btc",
		KeyEpoch:  1,
		SignAlgo:  int32(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340),
		Messages:  [][]byte{{0x01}},
		Committee: []roastsession.Participant{{ID: "node-a", Index: 0}},
		Threshold: 0,
		MyIndex:   0,
	})
	sess.SetState(roastsession.SignSessionStateAggregating)
	coord.sessions[jobID] = sess

	env := &Envelope{
		SessionID: jobID,
		Kind:      "SigShare",
		From:      "node-a",
		Chain:     "btc",
		VaultID:   1,
		SignAlgo:  pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
		Epoch:     1,
		Payload:   bytes.Repeat([]byte{0x24}, 32),
	}

	if err := coord.HandleRoastSigShare(env); err != nil {
		t.Fatalf("late HandleRoastSigShare returned error: %v", err)
	}
}

func TestCoordinatorLoopDoesNotRebroadcastImmediateDuplicateSignRequest(t *testing.T) {
	messenger := &recordingMessenger{}
	vaultProvider := &fixedCommitteeVaultProvider{
		committee: []SignerInfo{
			{ID: "node-a", Index: 0, Weight: 1},
		},
	}
	coord := NewCoordinator(
		"node-a",
		messenger,
		vaultProvider,
		&mockCryptoFactory{},
		logs.NewNodeLogger("coordinator-loop-test", 0),
	)
	coord.config.NonceCollectTimeout = 2 * time.Second
	coord.config.ShareCollectTimeout = 2 * time.Second

	jobID := "job-single-sign-request"
	sess := roastsession.NewSession(roastsession.SessionParams{
		JobID:     jobID,
		VaultID:   1,
		Chain:     "btc",
		KeyEpoch:  1,
		SignAlgo:  int32(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340),
		Messages:  [][]byte{{0x01}},
		Committee: []roastsession.Participant{{ID: "node-a", Index: 0}},
		Threshold: 0,
		MyIndex:   0,
	})
	if err := sess.Start(); err != nil {
		t.Fatalf("sess.Start failed: %v", err)
	}
	if err := sess.AddNonce(0, [][]byte{{0x11}}, [][]byte{{0x22}}); err != nil {
		t.Fatalf("sess.AddNonce failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		coord.runCoordinatorLoop(ctx, sess)
		close(done)
	}()

	time.Sleep(450 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runCoordinatorLoop did not exit after cancel")
	}

	if got := messenger.broadcastCount("SignRequest"); got != 1 {
		t.Fatalf("sign request broadcast count mismatch: got=%d want=1", got)
	}
}
