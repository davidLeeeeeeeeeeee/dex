package roast

import (
	"bytes"
	"testing"
)

func TestRoastRequestPayloadRoundTrip(t *testing.T) {
	messages := [][]byte{
		[]byte("msg-1"),
		{0x01, 0x02, 0x03},
	}
	nonces := []byte{0xAA, 0xBB, 0xCC}

	encoded := encodeRoastRequestPayload(messages, nonces)
	gotMsgs, gotNonces, ok := decodeRoastRequestPayload(encoded)
	if !ok {
		t.Fatalf("expected decode success")
	}
	if len(gotMsgs) != len(messages) {
		t.Fatalf("message count mismatch: got=%d want=%d", len(gotMsgs), len(messages))
	}
	for i := range messages {
		if !bytes.Equal(gotMsgs[i], messages[i]) {
			t.Fatalf("message[%d] mismatch: got=%x want=%x", i, gotMsgs[i], messages[i])
		}
	}
	if !bytes.Equal(gotNonces, nonces) {
		t.Fatalf("nonce blob mismatch: got=%x want=%x", gotNonces, nonces)
	}
}

func TestRoastRequestPayloadDecodeLegacy(t *testing.T) {
	legacy := []byte{0x02, 0x11, 0x22}
	_, _, ok := decodeRoastRequestPayload(legacy)
	if ok {
		t.Fatalf("legacy payload should not decode as request payload")
	}
}
