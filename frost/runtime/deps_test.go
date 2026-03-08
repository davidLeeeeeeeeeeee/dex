package runtime

import (
	"bytes"
	"testing"

	"dex/frost/runtime/roast"
)

func TestPBEnvelopeFromRoast_EncodesRoastEnvelope(t *testing.T) {
	tweaks := [][]byte{
		bytes.Repeat([]byte{0x11}, 32),
		bytes.Repeat([]byte{0x22}, 32),
	}
	in := &RoastEnvelope{
		SessionID: "sess-1",
		Kind:      "NonceRequest",
		From:      "node-a",
		Chain:     "btc",
		VaultID:   4,
		SignAlgo:  int32(1),
		Epoch:     9,
		Round:     2,
		Payload:   []byte("payload-bytes"),
		Tweaks:    tweaks,
	}

	pbEnv, err := PBEnvelopeFromRoast(in)
	if err != nil {
		t.Fatalf("PBEnvelopeFromRoast returned error: %v", err)
	}
	if pbEnv == nil {
		t.Fatal("PBEnvelopeFromRoast returned nil envelope")
	}
	if len(pbEnv.Payload) == 0 {
		t.Fatal("PB envelope payload is empty")
	}

	out, err := roast.EnvelopeFromPB(pbEnv)
	if err != nil {
		t.Fatalf("EnvelopeFromPB returned error: %v", err)
	}
	if out == nil {
		t.Fatal("EnvelopeFromPB returned nil")
	}
	if out.SessionID != in.SessionID || out.Kind != in.Kind || string(out.From) != string(in.From) {
		t.Fatalf("roundtrip mismatch: got session=%q kind=%q from=%q", out.SessionID, out.Kind, out.From)
	}
	if out.Chain != in.Chain || out.VaultID != in.VaultID || out.Epoch != in.Epoch || out.Round != in.Round {
		t.Fatalf("roundtrip numeric mismatch: got chain=%q vault=%d epoch=%d round=%d", out.Chain, out.VaultID, out.Epoch, out.Round)
	}
	if !bytes.Equal(out.Payload, in.Payload) {
		t.Fatalf("payload mismatch: got=%x want=%x", out.Payload, in.Payload)
	}
	if len(out.Tweaks) != len(in.Tweaks) {
		t.Fatalf("tweaks length mismatch: got=%d want=%d", len(out.Tweaks), len(in.Tweaks))
	}
	for i := range in.Tweaks {
		if !bytes.Equal(out.Tweaks[i], in.Tweaks[i]) {
			t.Fatalf("tweak[%d] mismatch: got=%x want=%x", i, out.Tweaks[i], in.Tweaks[i])
		}
	}
}
