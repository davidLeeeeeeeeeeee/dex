package roast

import (
	"crypto/sha256"
	"math/big"
	"testing"

	"dex/frost/core/curve"
	frost "dex/frost/core/frost"
	coreRoast "dex/frost/core/roast"
	"dex/frost/runtime/session"
	"dex/pb"
	"dex/utils"
)

func findMasterSecretWithEvenGroupPubYAndOddTweakedPubY(group curve.Group, receiver string) (*big.Int, []byte, []byte) {
	seed := new(big.Int).SetBytes([]byte{0xca, 0xfe, 0xba, 0xbe, 0x13, 0x37, 0x42, 0x99})
	for i := 0; i < 10000; i++ {
		candidate := new(big.Int).Add(seed, big.NewInt(int64(i)))
		groupPub := group.ScalarBaseMult(candidate)
		groupPubSerialized := group.SerializePoint(groupPub)
		if groupPubSerialized[0] != 0x02 {
			continue
		}
		tweak := utils.ComputeUserTweak(groupPubSerialized[1:33], receiver)
		tweakedPubkey, err := utils.ComputeTweakedPubkey(groupPubSerialized, tweak)
		if err != nil {
			continue
		}
		if len(tweakedPubkey) == 33 && tweakedPubkey[0] == 0x03 {
			return candidate, groupPubSerialized, tweak
		}
	}
	panic("unreachable: could not find even internal key with odd tweaked key")
}

func TestDeriveTaskSecretShareBIP340AppliesTweakAfterOddYNormalization(t *testing.T) {
	group := curve.NewSecp256k1Group()
	masterSecret := findMasterSecretWithOddGroupPubY(group)
	groupPub := group.ScalarBaseMult(masterSecret)
	groupPubSerialized := group.SerializePoint(groupPub)
	if groupPubSerialized[0] != 0x03 {
		t.Fatalf("expected odd-Y group pubkey, got prefix=0x%02x", groupPubSerialized[0])
	}

	rawShare := masterSecret.FillBytes(make([]byte, 32))
	tweak := utils.ComputeUserTweak(groupPubSerialized[1:33], "bc1q8ra2f338djg0pzgms35tpkdxjx9lr39mkyw84y")
	got, shareNegated, tweakApplied, err := deriveTaskSecretShare(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340, groupPubSerialized, rawShare, tweak)
	if err != nil {
		t.Fatalf("deriveTaskSecretShare failed: %v", err)
	}

	order := group.Order()
	want := new(big.Int).Sub(order, masterSecret)
	want.Add(want, new(big.Int).SetBytes(tweak))
	want.Mod(want, order)

	if !shareNegated {
		t.Fatal("expected share to be negated for odd-Y group pubkey")
	}
	if !tweakApplied {
		t.Fatal("expected tweak compensation to be applied")
	}
	if got.Cmp(want) != 0 {
		t.Fatalf("derived task share mismatch: got=%x want=%x", got.FillBytes(make([]byte, 32)), want.FillBytes(make([]byte, 32)))
	}
}

func TestDeriveTaskSecretShareBIP340NormalizesOddTweakedPubkeyAfterTweak(t *testing.T) {
	group := curve.NewSecp256k1Group()
	receiver := "bc1qqaw9tguaurp8t29cgu933ahlsn54xjjfqhwqyj"
	masterSecret, groupPubSerialized, tweak := findMasterSecretWithEvenGroupPubYAndOddTweakedPubY(group, receiver)
	if groupPubSerialized[0] != 0x02 {
		t.Fatalf("expected even-Y group pubkey, got prefix=0x%02x", groupPubSerialized[0])
	}
	tweakedPubkey, err := utils.ComputeTweakedPubkey(groupPubSerialized, tweak)
	if err != nil {
		t.Fatalf("ComputeTweakedPubkey failed: %v", err)
	}
	if tweakedPubkey[0] != 0x03 {
		t.Fatalf("expected odd-Y tweaked pubkey, got prefix=0x%02x", tweakedPubkey[0])
	}

	rawShare := masterSecret.FillBytes(make([]byte, 32))
	got, shareNegated, tweakApplied, err := deriveTaskSecretShare(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340, groupPubSerialized, rawShare, tweak)
	if err != nil {
		t.Fatalf("deriveTaskSecretShare failed: %v", err)
	}

	order := group.Order()
	want := new(big.Int).Add(masterSecret, new(big.Int).SetBytes(tweak))
	want.Mod(want, order)
	want.Sub(order, want)
	want.Mod(want, order)

	if !shareNegated {
		t.Fatal("expected share to be negated for odd-Y tweaked pubkey")
	}
	if !tweakApplied {
		t.Fatal("expected tweak compensation to be applied")
	}
	if got.Cmp(want) != 0 {
		t.Fatalf("derived task share mismatch: got=%x want=%x", got.FillBytes(make([]byte, 32)), want.FillBytes(make([]byte, 32)))
	}
}

func TestCoordinatorAggregateSignaturesUsesTweakedPubkey(t *testing.T) {
	group := curve.NewSecp256k1Group()
	signAlgo := int32(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340)
	masterSecret := findMasterSecretWithOddGroupPubY(group)
	groupPub := group.ScalarBaseMult(masterSecret)
	groupPubSerialized := group.SerializePoint(groupPub)

	threshold, numSigners := 2, 4
	shares, _ := generateTestShares(masterSecret, threshold, numSigners, group)
	signerIDs := []int{1, 2, 3}
	msg := sha256.Sum256([]byte("coordinator taproot tweak test"))
	tweak := utils.ComputeUserTweak(groupPubSerialized[1:33], "bc1q8ra2f338djg0pzgms35tpkdxjx9lr39mkyw84y")

	coreNonces := make([]coreRoast.SignerNonce, len(signerIDs))
	for i, sid := range signerIDs {
		h, b, hp, bp := generateNoncePairForTest(group)
		coreNonces[i] = coreRoast.SignerNonce{
			SignerID:     sid,
			HidingNonce:  h,
			BindingNonce: b,
			HidingPoint:  hp,
			BindingPoint: bp,
		}
	}

	R, err := coreRoast.ComputeGroupCommitment(coreNonces, msg[:], group)
	if err != nil {
		t.Fatalf("ComputeGroupCommitment failed: %v", err)
	}
	tweakedPubkey, err := utils.ComputeTweakedPubkey(groupPubSerialized, tweak)
	if err != nil {
		t.Fatalf("ComputeTweakedPubkey failed: %v", err)
	}
	e := coreRoast.ComputeChallenge(R, new(big.Int).SetBytes(tweakedPubkey[1:33]), msg[:], group)
	lambdas := coreRoast.ComputeLagrangeCoefficientsForSet(signerIDs, group.Order())

	partialSigs := make([]coreRoast.SignerShare, len(signerIDs))
	for i, sid := range signerIDs {
		taskShare, shareNegated, tweakApplied, err := deriveTaskSecretShare(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340, groupPubSerialized, shares[sid].FillBytes(make([]byte, 32)), tweak)
		if err != nil {
			t.Fatalf("deriveTaskSecretShare failed: %v", err)
		}
		if !shareNegated || !tweakApplied {
			t.Fatalf("unexpected share state for signer %d: shareNegated=%v tweakApplied=%v", sid, shareNegated, tweakApplied)
		}

		rho := coreRoast.ComputeBindingCoefficient(sid, msg[:], coreNonces, group)
		adjH, adjB := adjustNoncePairForBIP340(
			pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
			CurvePoint(R),
			new(big.Int).Set(coreNonces[i].HidingNonce),
			new(big.Int).Set(coreNonces[i].BindingNonce),
		)
		z := coreRoast.ComputePartialSignature(sid, adjH, adjB, taskShare, rho, lambdas[sid], e, group)
		partialSigs[i] = coreRoast.SignerShare{SignerID: sid, Share: z}
	}

	coordinator := newTestCoordinator(groupPubSerialized)
	committee := make([]session.Participant, numSigners)
	for i := 0; i < numSigners; i++ {
		committee[i] = session.Participant{ID: "node", Index: i}
	}
	sess := session.NewSession(session.SessionParams{
		JobID:     "taproot_tweak",
		VaultID:   1,
		Chain:     "btc",
		KeyEpoch:  1,
		SignAlgo:  signAlgo,
		Messages:  [][]byte{msg[:]},
		Committee: committee,
		Threshold: threshold,
		Tweaks:    [][]byte{tweak},
	})
	sess.Start()
	sess.SelectedSet = []int{0, 1, 2}
	sess.SetState(session.SignSessionStateCollectingNonces)
	for i, sid := range signerIDs {
		sess.AddNonce(sid-1,
			[][]byte{group.SerializePoint(coreNonces[i].HidingPoint)},
			[][]byte{group.SerializePoint(coreNonces[i].BindingPoint)},
		)
	}
	sess.SetState(session.SignSessionStateCollectingShares)
	for i, sid := range signerIDs {
		shareBytes := make([]byte, 32)
		partialSigs[i].Share.FillBytes(shareBytes)
		sess.AddShare(sid-1, [][]byte{shareBytes})
	}

	signatures, err := coordinator.aggregateSignatures(sess)
	if err != nil {
		t.Fatalf("aggregateSignatures failed: %v", err)
	}
	if len(signatures) != 1 || len(signatures[0]) == 0 {
		t.Fatalf("unexpected signatures result: %v", signatures)
	}

	valid, err := frost.Verify(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340, tweakedPubkey[1:33], msg[:], signatures[0])
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !valid {
		t.Fatal("expected aggregated signature to verify against tweaked pubkey")
	}
}

func TestCoordinatorAggregateSignaturesUsesOddTweakedPubkeyNormalization(t *testing.T) {
	group := curve.NewSecp256k1Group()
	signAlgo := int32(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340)
	receiver := "bc1qqaw9tguaurp8t29cgu933ahlsn54xjjfqhwqyj"
	masterSecret, groupPubSerialized, tweak := findMasterSecretWithEvenGroupPubYAndOddTweakedPubY(group, receiver)

	threshold, numSigners := 2, 4
	shares, _ := generateTestShares(masterSecret, threshold, numSigners, group)
	signerIDs := []int{1, 2, 3}
	msg := sha256.Sum256([]byte("coordinator odd tweaked pubkey normalization test"))

	coreNonces := make([]coreRoast.SignerNonce, len(signerIDs))
	for i, sid := range signerIDs {
		h, b, hp, bp := generateNoncePairForTest(group)
		coreNonces[i] = coreRoast.SignerNonce{
			SignerID:     sid,
			HidingNonce:  h,
			BindingNonce: b,
			HidingPoint:  hp,
			BindingPoint: bp,
		}
	}

	R, err := coreRoast.ComputeGroupCommitment(coreNonces, msg[:], group)
	if err != nil {
		t.Fatalf("ComputeGroupCommitment failed: %v", err)
	}
	tweakedPubkey, err := utils.ComputeTweakedPubkey(groupPubSerialized, tweak)
	if err != nil {
		t.Fatalf("ComputeTweakedPubkey failed: %v", err)
	}
	if tweakedPubkey[0] != 0x03 {
		t.Fatalf("expected odd-Y tweaked pubkey, got prefix=0x%02x", tweakedPubkey[0])
	}
	e := coreRoast.ComputeChallenge(R, new(big.Int).SetBytes(tweakedPubkey[1:33]), msg[:], group)
	lambdas := coreRoast.ComputeLagrangeCoefficientsForSet(signerIDs, group.Order())

	partialSigs := make([]coreRoast.SignerShare, len(signerIDs))
	for i, sid := range signerIDs {
		taskShare, shareNegated, tweakApplied, err := deriveTaskSecretShare(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340, groupPubSerialized, shares[sid].FillBytes(make([]byte, 32)), tweak)
		if err != nil {
			t.Fatalf("deriveTaskSecretShare failed: %v", err)
		}
		if !shareNegated || !tweakApplied {
			t.Fatalf("unexpected share state for signer %d: shareNegated=%v tweakApplied=%v", sid, shareNegated, tweakApplied)
		}

		rho := coreRoast.ComputeBindingCoefficient(sid, msg[:], coreNonces, group)
		adjH, adjB := adjustNoncePairForBIP340(
			pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
			CurvePoint(R),
			new(big.Int).Set(coreNonces[i].HidingNonce),
			new(big.Int).Set(coreNonces[i].BindingNonce),
		)
		z := coreRoast.ComputePartialSignature(sid, adjH, adjB, taskShare, rho, lambdas[sid], e, group)
		partialSigs[i] = coreRoast.SignerShare{SignerID: sid, Share: z}
	}

	coordinator := newTestCoordinator(groupPubSerialized)
	committee := make([]session.Participant, numSigners)
	for i := 0; i < numSigners; i++ {
		committee[i] = session.Participant{ID: "node", Index: i}
	}
	sess := session.NewSession(session.SessionParams{
		JobID:     "taproot_tweak_odd_final",
		VaultID:   1,
		Chain:     "btc",
		KeyEpoch:  1,
		SignAlgo:  signAlgo,
		Messages:  [][]byte{msg[:]},
		Committee: committee,
		Threshold: threshold,
		Tweaks:    [][]byte{tweak},
	})
	sess.Start()
	sess.SelectedSet = []int{0, 1, 2}
	sess.SetState(session.SignSessionStateCollectingNonces)
	for i, sid := range signerIDs {
		sess.AddNonce(sid-1,
			[][]byte{group.SerializePoint(coreNonces[i].HidingPoint)},
			[][]byte{group.SerializePoint(coreNonces[i].BindingPoint)},
		)
	}
	sess.SetState(session.SignSessionStateCollectingShares)
	for i, sid := range signerIDs {
		shareBytes := make([]byte, 32)
		partialSigs[i].Share.FillBytes(shareBytes)
		sess.AddShare(sid-1, [][]byte{shareBytes})
	}

	signatures, err := coordinator.aggregateSignatures(sess)
	if err != nil {
		t.Fatalf("aggregateSignatures failed: %v", err)
	}
	if len(signatures) != 1 || len(signatures[0]) == 0 {
		t.Fatalf("unexpected signatures result: %v", signatures)
	}

	valid, err := frost.Verify(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340, tweakedPubkey[1:33], msg[:], signatures[0])
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !valid {
		t.Fatal("expected aggregated signature to verify against odd-parity tweaked pubkey x-only key")
	}
}
