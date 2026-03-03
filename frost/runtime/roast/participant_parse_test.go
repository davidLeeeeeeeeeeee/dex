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

func TestAdjustNoncePairForBIP340OddGroupCommit(t *testing.T) {
	group := curve.NewSecp256k1Group()
	order := group.Order()

	var odd CurvePoint
	found := false
	for i := int64(1); i < 2048; i++ {
		p := group.ScalarBaseMult(big.NewInt(i))
		if p.Y.Bit(0) == 1 {
			odd = CurvePoint{X: p.X, Y: p.Y}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("failed to find odd-Y point")
	}

	hiding := big.NewInt(12345)
	binding := big.NewInt(67890)
	adjHiding, adjBinding := adjustNoncePairForBIP340(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340, odd, hiding, binding)

	wantHiding := new(big.Int).Sub(order, hiding)
	wantHiding.Mod(wantHiding, order)
	wantBinding := new(big.Int).Sub(order, binding)
	wantBinding.Mod(wantBinding, order)

	if adjHiding.Cmp(wantHiding) != 0 {
		t.Fatalf("adjusted hiding nonce mismatch: got=%s want=%s", adjHiding.String(), wantHiding.String())
	}
	if adjBinding.Cmp(wantBinding) != 0 {
		t.Fatalf("adjusted binding nonce mismatch: got=%s want=%s", adjBinding.String(), wantBinding.String())
	}
}

func TestAdjustNoncePairForBIP340EvenGroupCommitNoChange(t *testing.T) {
	group := curve.NewSecp256k1Group()

	var even CurvePoint
	found := false
	for i := int64(1); i < 2048; i++ {
		p := group.ScalarBaseMult(big.NewInt(i))
		if p.Y.Bit(0) == 0 {
			even = CurvePoint{X: p.X, Y: p.Y}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("failed to find even-Y point")
	}

	hiding := big.NewInt(111)
	binding := big.NewInt(222)
	adjHiding, adjBinding := adjustNoncePairForBIP340(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340, even, hiding, binding)
	if adjHiding.Cmp(hiding) != 0 {
		t.Fatalf("hiding nonce changed unexpectedly: got=%s want=%s", adjHiding.String(), hiding.String())
	}
	if adjBinding.Cmp(binding) != 0 {
		t.Fatalf("binding nonce changed unexpectedly: got=%s want=%s", adjBinding.String(), binding.String())
	}

	altHiding, altBinding := adjustNoncePairForBIP340(pb.SignAlgo_SIGN_ALGO_ED25519, even, hiding, binding)
	if altHiding.Cmp(hiding) != 0 || altBinding.Cmp(binding) != 0 {
		t.Fatalf("non-BIP340 nonce adjustment should be no-op")
	}
}

func TestFindSignerIDByNoncePoints(t *testing.T) {
	taskNonces := []NonceInput{
		{
			SignerID:     4,
			HidingPoint:  secpPointFromScalar(11),
			BindingPoint: secpPointFromScalar(12),
		},
		{
			SignerID:     9,
			HidingPoint:  secpPointFromScalar(21),
			BindingPoint: secpPointFromScalar(22),
		},
	}

	id, ok := findSignerIDByNoncePoints(taskNonces, secpPointFromScalar(21), secpPointFromScalar(22))
	if !ok {
		t.Fatal("expected signer id to be found")
	}
	if id != 9 {
		t.Fatalf("unexpected signer id: got=%d want=9", id)
	}
}

func TestFindSignerIDByNoncePointsNotFound(t *testing.T) {
	taskNonces := []NonceInput{
		{
			SignerID:     4,
			HidingPoint:  secpPointFromScalar(11),
			BindingPoint: secpPointFromScalar(12),
		},
	}

	id, ok := findSignerIDByNoncePoints(taskNonces, secpPointFromScalar(31), secpPointFromScalar(32))
	if ok {
		t.Fatalf("expected signer id to be absent, got=%d", id)
	}
	if id != 0 {
		t.Fatalf("unexpected signer id for not found case: got=%d want=0", id)
	}
}
