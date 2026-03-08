package main

import (
	"bytes"
	"dex/utils"
	"encoding/hex"
	"testing"
)

func mustDecodeHex(t *testing.T, raw string) []byte {
	t.Helper()
	out, err := hex.DecodeString(raw)
	if err != nil {
		t.Fatalf("decode hex failed: %v", err)
	}
	return out
}

func TestBuildTweakedTaprootScriptPubKey(t *testing.T) {
	groupPubKey := mustDecodeHex(t, "03d194cd76d1ba9f139cca4af1277993c90e20ac5c817dcfe93a92810c5f9b03cb")
	receiverAddr := "bc1q8ra2f338djg0pzgms35tpkdxjx9lr39mkyw84y"

	got, err := buildTweakedTaprootScriptPubKey(groupPubKey, receiverAddr)
	if err != nil {
		t.Fatalf("buildTweakedTaprootScriptPubKey failed: %v", err)
	}

	rawXOnly := groupPubKey[1:]
	tweak := utils.ComputeUserTweak(rawXOnly, receiverAddr)
	tweakedPub, err := utils.ComputeTweakedPubkey(groupPubKey, tweak)
	if err != nil {
		t.Fatalf("ComputeTweakedPubkey failed: %v", err)
	}
	want, err := utils.BuildTaprootScriptPubKeyFromXOnly(tweakedPub[1:33])
	if err != nil {
		t.Fatalf("BuildTaprootScriptPubKeyFromXOnly failed: %v", err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("scriptPubKey mismatch: got=%x want=%x", got, want)
	}
	if bytes.Equal(got[2:], rawXOnly) {
		t.Fatalf("expected tweaked scriptPubKey to differ from raw x-only pubkey")
	}
}
