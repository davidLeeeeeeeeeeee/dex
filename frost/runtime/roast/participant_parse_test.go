package roast

import (
	"bytes"
	"math/big"
	"testing"

	"dex/frost/core/curve"
	"dex/pb"
)

func secpPointFromScalar(k int64) CurvePoint {
	g := curve.NewSecp256k1Group()
	p := g.ScalarBaseMult(big.NewInt(k))
	return CurvePoint{X: p.X, Y: p.Y}
}

func secpSerializedPointFromScalar(k int64) []byte {
	g := curve.NewSecp256k1Group()
	p := g.ScalarBaseMult(big.NewInt(k))
	return g.SerializePoint(p)
}

func requirePointEqual(t *testing.T, got, want CurvePoint) {
	t.Helper()
	if got.X == nil || got.Y == nil || want.X == nil || want.Y == nil {
		t.Fatalf("nil point component: got=(%v,%v) want=(%v,%v)", got.X, got.Y, want.X, want.Y)
	}
	if got.X.Cmp(want.X) != 0 || got.Y.Cmp(want.Y) != 0 {
		t.Fatalf("point mismatch: got=(%s,%s) want=(%s,%s)", got.X.String(), got.Y.String(), want.X.String(), want.Y.String())
	}
}

func TestParseAggregatedNoncesSignerMajorOrder(t *testing.T) {
	signAlgo := pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340
	numTasks := 2

	// signer-major payload:
	// signer1: task0(h1,b2), task1(h3,b4)
	// signer2: task0(h5,b6), task1(h7,b8)
	payload := make([]byte, 0, 2*numTasks*2*33)
	appendNonce := func(hidingScalar, bindingScalar int64) {
		payload = append(payload, secpSerializedPointFromScalar(hidingScalar)...)
		payload = append(payload, secpSerializedPointFromScalar(bindingScalar)...)
	}
	appendNonce(1, 2)
	appendNonce(3, 4)
	appendNonce(5, 6)
	appendNonce(7, 8)

	parsed := parseAggregatedNonces(payload, numTasks, signAlgo)
	if len(parsed) != numTasks {
		t.Fatalf("task count mismatch: got %d want %d", len(parsed), numTasks)
	}
	if len(parsed[0]) != 2 || len(parsed[1]) != 2 {
		t.Fatalf("parsed nonce count mismatch: task0=%d task1=%d want=2,2", len(parsed[0]), len(parsed[1]))
	}

	// task0 should contain signer1 then signer2.
	if parsed[0][0].SignerID != 1 || parsed[0][1].SignerID != 2 {
		t.Fatalf("task0 signer ids mismatch: got [%d,%d] want [1,2]", parsed[0][0].SignerID, parsed[0][1].SignerID)
	}
	requirePointEqual(t, parsed[0][0].HidingPoint, secpPointFromScalar(1))
	requirePointEqual(t, parsed[0][0].BindingPoint, secpPointFromScalar(2))
	requirePointEqual(t, parsed[0][1].HidingPoint, secpPointFromScalar(5))
	requirePointEqual(t, parsed[0][1].BindingPoint, secpPointFromScalar(6))

	// task1 should contain signer1 then signer2.
	if parsed[1][0].SignerID != 1 || parsed[1][1].SignerID != 2 {
		t.Fatalf("task1 signer ids mismatch: got [%d,%d] want [1,2]", parsed[1][0].SignerID, parsed[1][1].SignerID)
	}
	requirePointEqual(t, parsed[1][0].HidingPoint, secpPointFromScalar(3))
	requirePointEqual(t, parsed[1][0].BindingPoint, secpPointFromScalar(4))
	requirePointEqual(t, parsed[1][1].HidingPoint, secpPointFromScalar(7))
	requirePointEqual(t, parsed[1][1].BindingPoint, secpPointFromScalar(8))
}

func TestParseAggregatedNoncesSkipsInvalidPoints(t *testing.T) {
	signAlgo := pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340

	invalidPoint := bytes.Repeat([]byte{0x00}, 33) // invalid compressed secp256k1 point
	validPoint := secpSerializedPointFromScalar(9)
	payload := append(append([]byte{}, invalidPoint...), validPoint...)

	parsed := parseAggregatedNonces(payload, 1, signAlgo)
	if len(parsed) != 1 {
		t.Fatalf("task count mismatch: got %d want 1", len(parsed))
	}
	if len(parsed[0]) != 0 {
		t.Fatalf("expected invalid nonce entry to be skipped, got %d entries", len(parsed[0]))
	}
}

func TestNormalizeSecretShareForBIP340OddCompressedPubkey(t *testing.T) {
	share := big.NewInt(12345)
	pubKey := append([]byte{0x03}, bytes.Repeat([]byte{0x11}, 32)...)

	got := normalizeSecretShareForBIP340(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340, pubKey, share)
	order := curve.NewSecp256k1Group().Order()
	want := new(big.Int).Sub(order, share)
	want.Mod(want, order)

	if got.Cmp(want) != 0 {
		t.Fatalf("normalized share mismatch: got=%s want=%s", got.String(), want.String())
	}
}

func TestNormalizeSecretShareForBIP340EvenCompressedPubkeyNoChange(t *testing.T) {
	share := big.NewInt(67890)
	pubKey := append([]byte{0x02}, bytes.Repeat([]byte{0x22}, 32)...)

	got := normalizeSecretShareForBIP340(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340, pubKey, share)
	if got.Cmp(share) != 0 {
		t.Fatalf("share should not change for even-Y pubkey: got=%s want=%s", got.String(), share.String())
	}
}

func TestParseAggregatedNoncesWithSignerIDsHeader(t *testing.T) {
	signAlgo := pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340
	numTasks := 2

	// signer-major nonce blob:
	// signerID=2: task0(h1,b2), task1(h3,b4)
	// signerID=5: task0(h5,b6), task1(h7,b8)
	raw := make([]byte, 0, 2*numTasks*2*33)
	appendNonce := func(hidingScalar, bindingScalar int64) {
		raw = append(raw, secpSerializedPointFromScalar(hidingScalar)...)
		raw = append(raw, secpSerializedPointFromScalar(bindingScalar)...)
	}
	appendNonce(1, 2)
	appendNonce(3, 4)
	appendNonce(5, 6)
	appendNonce(7, 8)

	payload := encodeAggregatedNoncePayload([]int{2, 5}, raw)
	parsed := parseAggregatedNonces(payload, numTasks, signAlgo)
	if len(parsed) != numTasks {
		t.Fatalf("task count mismatch: got %d want %d", len(parsed), numTasks)
	}
	if len(parsed[0]) != 2 || len(parsed[1]) != 2 {
		t.Fatalf("parsed nonce count mismatch: task0=%d task1=%d want=2,2", len(parsed[0]), len(parsed[1]))
	}

	if parsed[0][0].SignerID != 2 || parsed[0][1].SignerID != 5 {
		t.Fatalf("task0 signer ids mismatch: got [%d,%d] want [2,5]", parsed[0][0].SignerID, parsed[0][1].SignerID)
	}
	if parsed[1][0].SignerID != 2 || parsed[1][1].SignerID != 5 {
		t.Fatalf("task1 signer ids mismatch: got [%d,%d] want [2,5]", parsed[1][0].SignerID, parsed[1][1].SignerID)
	}
}
