package adapters

import (
	"bytes"
	"testing"

	"dex/frost/runtime"
	"dex/frost/runtime/roast"
	"dex/pb"

	"google.golang.org/protobuf/proto"
)

func TestTransportP2PToTransportMessagePreservesRoastTweaks(t *testing.T) {
	p2p := &TransportP2P{}
	env := &runtime.FrostEnvelope{
		SessionID: "sess-1",
		Kind:      "SignRequest",
		From:      "node-a",
		Chain:     "btc",
		VaultID:   7,
		SignAlgo:  int32(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340),
		Epoch:     11,
		Round:     2,
		Payload:   []byte("payload"),
		Tweaks: [][]byte{
			bytes.Repeat([]byte{0x7a}, 32),
		},
	}

	msg, err := p2p.toTransportMessage(env)
	if err != nil {
		t.Fatalf("toTransportMessage returned error: %v", err)
	}

	var pbEnv pb.FrostEnvelope
	if err := proto.Unmarshal(msg.FrostPayload, &pbEnv); err != nil {
		t.Fatalf("proto.Unmarshal returned error: %v", err)
	}

	decoded, err := roast.EnvelopeFromPB(&pbEnv)
	if err != nil {
		t.Fatalf("EnvelopeFromPB returned error: %v", err)
	}
	if len(decoded.Tweaks) != 1 {
		t.Fatalf("decoded tweaks length = %d, want 1", len(decoded.Tweaks))
	}
	if !bytes.Equal(decoded.Tweaks[0], env.Tweaks[0]) {
		t.Fatalf("decoded tweak mismatch: got=%x want=%x", decoded.Tweaks[0], env.Tweaks[0])
	}
}
