package runtime

import (
	"context"
	"testing"
	"time"

	"dex/frost/runtime/services"
)

type fakeServicesSigningService struct {
	result *services.SignedPackage
	err    error
}

func (s *fakeServicesSigningService) StartSigningSession(ctx context.Context, params *services.SigningSessionParams) (sessionID string, err error) {
	return "session-test", nil
}

func (s *fakeServicesSigningService) GetSessionStatus(sessionID string) (*services.SessionStatus, error) {
	return &services.SessionStatus{SessionID: sessionID, State: "COMPLETE", Progress: 1}, nil
}

func (s *fakeServicesSigningService) CancelSession(sessionID string) error { return nil }

func (s *fakeServicesSigningService) WaitForCompletion(ctx context.Context, sessionID string, timeout time.Duration) (*services.SignedPackage, error) {
	return s.result, s.err
}

func TestSigningServiceAdapterWaitForCompletionPropagatesSignatures(t *testing.T) {
	sig1 := []byte{0x01, 0x02}
	sig2 := []byte{0x03, 0x04}
	fakeSvc := &fakeServicesSigningService{
		result: &services.SignedPackage{
			SessionID:    "session-test",
			JobID:        "job-test",
			Signature:    []byte{0xaa},
			Signatures:   [][]byte{sig1, sig2},
			RawTx:        []byte{0xbb},
			TemplateHash: []byte{0xcc},
		},
	}

	adapter := &signingServiceAdapter{service: fakeSvc}
	got, err := adapter.WaitForCompletion(context.Background(), "session-test", time.Second)
	if err != nil {
		t.Fatalf("WaitForCompletion returned error: %v", err)
	}
	if got == nil {
		t.Fatal("WaitForCompletion returned nil package")
	}
	if len(got.Signatures) != 2 {
		t.Fatalf("signatures count mismatch: got=%d want=2", len(got.Signatures))
	}
	if got.Signatures[0][0] != sig1[0] || got.Signatures[1][1] != sig2[1] {
		t.Fatal("signatures content mismatch")
	}
}
