package btc

import (
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

func TestBTCAdapterVerifySignatureAcceptsCompressedPubkey(t *testing.T) {
	adapter := NewBTCAdapter("regtest")

	privKey, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatalf("new private key failed: %v", err)
	}

	msg := sha256.Sum256([]byte("adapter verify compressed key"))
	sig, err := schnorr.Sign(privKey, msg[:])
	if err != nil {
		t.Fatalf("schnorr sign failed: %v", err)
	}

	compressed := privKey.PubKey().SerializeCompressed() // 33-byte compressed key
	valid, err := adapter.VerifySignature(compressed, msg[:], sig.Serialize())
	if err != nil {
		t.Fatalf("VerifySignature returned error: %v", err)
	}
	if !valid {
		t.Fatal("expected signature to be valid")
	}
}

func TestBTCAdapterVerifySignatureRejectsInvalidCompressedPrefix(t *testing.T) {
	adapter := NewBTCAdapter("regtest")

	msg := make([]byte, 32)
	sig := make([]byte, 64)
	pub := make([]byte, 33)
	pub[0] = 0x04

	_, err := adapter.VerifySignature(pub, msg, sig)
	if err == nil {
		t.Fatal("expected error for invalid compressed pubkey prefix")
	}
	if !strings.Contains(err.Error(), "prefix") {
		t.Fatalf("expected prefix-related error, got: %v", err)
	}
}
