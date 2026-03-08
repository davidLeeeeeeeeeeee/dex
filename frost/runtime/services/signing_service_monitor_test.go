package services

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"dex/frost/runtime/roast"
	"dex/frost/runtime/session"
	"dex/frost/runtime/types"
	"dex/logs"
	"dex/pb"
)

type testVaultProvider struct{}

func (p *testVaultProvider) VaultCommittee(chain string, vaultID uint32, epoch uint64) ([]types.SignerInfo, error) {
	return []types.SignerInfo{
		{ID: types.NodeID("node-a"), Index: 0, Weight: 1},
	}, nil
}

func (p *testVaultProvider) VaultCurrentEpoch(chain string, vaultID uint32) uint64 { return 1 }

func (p *testVaultProvider) VaultGroupPubkey(chain string, vaultID uint32, epoch uint64) ([]byte, error) {
	return nil, nil
}

func (p *testVaultProvider) CalculateThreshold(chain string, vaultID uint32) (int, error) {
	return 1, nil
}

func TestMonitorSessionCopiesAllSignatures(t *testing.T) {
	coord := roast.NewCoordinator(
		roast.NodeID("node-a"),
		nil,
		&testVaultProvider{},
		nil,
		logs.NewNodeLogger("svc-monitor-test", 0),
	)

	ctx := context.Background()
	err := coord.StartSession(ctx, &roast.StartSessionParams{
		JobID:     "job-monitor-1",
		VaultID:   1,
		Chain:     "btc",
		KeyEpoch:  1,
		SignAlgo:  pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
		Messages:  [][]byte{{0x01}, {0x02}},
		Threshold: 1,
	})
	if err != nil {
		t.Fatalf("StartSession failed: %v", err)
	}

	sess := coord.GetSession("job-monitor-1")
	if sess == nil {
		t.Fatal("session not found")
	}

	sig1 := bytes.Repeat([]byte{0x11}, 64)
	sig2 := bytes.Repeat([]byte{0x22}, 64)
	sess.MarkCompleted([][]byte{sig1, sig2})

	svc := &RoastSigningService{
		coordinator: coord,
		sessions:    map[string]*signingSessionWrapper{},
		callbacks:   map[string]func(*SignedPackage, error){},
	}

	wrapper := &signingSessionWrapper{
		sessionID: "job-monitor-1",
		jobID:     "job-monitor-1",
		status: &SessionStatus{
			SessionID: "job-monitor-1",
			JobID:     "job-monitor-1",
			State:     "INIT",
		},
		doneCh: make(chan struct{}),
	}

	done := make(chan struct{})
	go func() {
		svc.monitorSession(context.Background(), wrapper)
		close(done)
	}()

	select {
	case <-wrapper.doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("monitorSession did not complete in time")
	}

	wrapper.mu.RLock()
	defer wrapper.mu.RUnlock()
	if wrapper.err != nil {
		t.Fatalf("monitorSession returned error: %v", wrapper.err)
	}
	if wrapper.result == nil {
		t.Fatal("monitorSession returned nil result")
	}
	if len(wrapper.result.Signatures) != 2 {
		t.Fatalf("signatures count mismatch: got=%d want=2", len(wrapper.result.Signatures))
	}
	if !bytes.Equal(wrapper.result.Signatures[0], sig1) || !bytes.Equal(wrapper.result.Signatures[1], sig2) {
		t.Fatal("signature list mismatch")
	}
	if len(wrapper.result.Signature) != 128 {
		t.Fatalf("combined signature length mismatch: got=%d want=128", len(wrapper.result.Signature))
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("monitorSession goroutine did not exit")
	}
}

func TestStartSigningSessionRestartsFailedSession(t *testing.T) {
	coord := roast.NewCoordinator(
		roast.NodeID("node-a"),
		nil,
		&testVaultProvider{},
		nil,
		logs.NewNodeLogger("svc-restart-test", 0),
	)

	svc := NewRoastSigningService(coord, nil, &testVaultProvider{})
	params := &SigningSessionParams{
		JobID:     "job-restart-1",
		Chain:     "btc",
		VaultID:   1,
		KeyEpoch:  1,
		SignAlgo:  pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
		Messages:  [][]byte{{0x01}},
		Threshold: 0,
	}

	sessionID, err := svc.StartSigningSession(context.Background(), params)
	if err != nil {
		t.Fatalf("first StartSigningSession failed: %v", err)
	}

	oldSess := coord.GetSession(params.JobID)
	if oldSess == nil {
		t.Fatal("old session not found")
	}
	oldSess.SetState(session.SignSessionStateFailed)

	oldWrapper := svc.sessions[sessionID]
	if oldWrapper == nil {
		t.Fatal("old wrapper not found")
	}
	oldWrapper.setError(errors.New("session failed"))

	restartedID, err := svc.StartSigningSession(context.Background(), params)
	if err != nil {
		t.Fatalf("restart StartSigningSession failed: %v", err)
	}
	if restartedID != sessionID {
		t.Fatalf("session id mismatch: got=%s want=%s", restartedID, sessionID)
	}

	newSess := coord.GetSession(params.JobID)
	if newSess == nil {
		t.Fatal("new session not found")
	}
	if newSess == oldSess {
		t.Fatal("expected coordinator session to be replaced after failure")
	}
	if state := newSess.GetState(); state != session.SignSessionStateCollectingNonces {
		t.Fatalf("unexpected new session state: got=%v want=%v", state, session.SignSessionStateCollectingNonces)
	}

	newWrapper := svc.sessions[sessionID]
	if newWrapper == nil {
		t.Fatal("new wrapper not found")
	}
	if newWrapper == oldWrapper {
		t.Fatal("expected failed wrapper to be replaced")
	}

	newWrapper.mu.RLock()
	defer newWrapper.mu.RUnlock()
	if newWrapper.err != nil {
		t.Fatalf("new wrapper unexpectedly has error: %v", newWrapper.err)
	}
	if newWrapper.result != nil {
		t.Fatal("new wrapper unexpectedly has result")
	}
	select {
	case <-newWrapper.doneCh:
		t.Fatal("new wrapper should still be pending")
	default:
	}
}
