package roast

import (
	"bytes"
	"errors"
	"testing"

	"dex/pb"
)

func TestRoastEnvelopeWireRoundTrip(t *testing.T) {
	msg := &Envelope{
		SessionID: "job-1",
		Kind:      "NonceCommit",
		From:      NodeID("node-1"),
		Chain:     "btc",
		VaultID:   10,
		SignAlgo:  pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
		Epoch:     7,
		Round:     1,
		Payload:   []byte{1, 2, 3},
	}

	env, err := PBEnvelopeFromRoast(msg)
	if err != nil {
		t.Fatalf("PBEnvelopeFromRoast failed: %v", err)
	}
	if env.Kind != pb.FrostEnvelopeKind_FROST_ENVELOPE_KIND_ROAST_RESPONSE {
		t.Fatalf("unexpected envelope kind: %v", env.Kind)
	}

	decoded, err := EnvelopeFromPB(env)
	if err != nil {
		t.Fatalf("EnvelopeFromPB failed: %v", err)
	}
	if decoded.SessionID != msg.SessionID || decoded.Kind != msg.Kind || decoded.From != msg.From {
		t.Fatalf("decoded header mismatch: got %+v want %+v", decoded, msg)
	}
	if decoded.Chain != msg.Chain || decoded.VaultID != msg.VaultID || decoded.SignAlgo != msg.SignAlgo {
		t.Fatalf("decoded routing mismatch: got %+v want %+v", decoded, msg)
	}
	if decoded.Epoch != msg.Epoch || decoded.Round != msg.Round {
		t.Fatalf("decoded epoch/round mismatch: got %+v want %+v", decoded, msg)
	}
	if !bytes.Equal(decoded.Payload, msg.Payload) {
		t.Fatalf("decoded payload mismatch: got %x want %x", decoded.Payload, msg.Payload)
	}
}

func TestEnvelopeFromPBEmptyPayload(t *testing.T) {
	_, err := EnvelopeFromPB(&pb.FrostEnvelope{})
	if !errors.Is(err, ErrEmptyRoastPayload) {
		t.Fatalf("expected ErrEmptyRoastPayload, got %v", err)
	}
}

func TestPBEnvelopeFromRoastInvalidKind(t *testing.T) {
	_, err := PBEnvelopeFromRoast(&Envelope{
		SessionID: "job-1",
		Kind:      "UnknownKind",
	})
	if !errors.Is(err, ErrInvalidRoastKind) {
		t.Fatalf("expected ErrInvalidRoastKind, got %v", err)
	}
}
