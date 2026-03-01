package roast

import (
	"bytes"
	"testing"
)

func TestAggregatedNoncePayloadRoundTrip(t *testing.T) {
	signerIDs := []int{2, 5, 9}
	blob := []byte{0xAA, 0xBB, 0xCC, 0xDD}

	encoded := encodeAggregatedNoncePayload(signerIDs, blob)
	gotIDs, gotBlob, ok := decodeAggregatedNoncePayload(encoded)
	if !ok {
		t.Fatalf("expected decode success")
	}
	if len(gotIDs) != len(signerIDs) {
		t.Fatalf("signer id count mismatch: got %d want %d", len(gotIDs), len(signerIDs))
	}
	for i := range signerIDs {
		if gotIDs[i] != signerIDs[i] {
			t.Fatalf("signer id mismatch at %d: got %d want %d", i, gotIDs[i], signerIDs[i])
		}
	}
	if !bytes.Equal(gotBlob, blob) {
		t.Fatalf("blob mismatch: got %x want %x", gotBlob, blob)
	}
}

func TestAggregatedNoncePayloadDecodeLegacy(t *testing.T) {
	legacy := []byte{0x02, 0x11, 0x22}
	_, _, ok := decodeAggregatedNoncePayload(legacy)
	if ok {
		t.Fatalf("legacy payload should not decode as header payload")
	}
}
