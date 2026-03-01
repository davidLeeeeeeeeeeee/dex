package services

import (
	"bytes"
	"context"
	"testing"
	"time"

	"dex/frost/runtime/roast"
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
