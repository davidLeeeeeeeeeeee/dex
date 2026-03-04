package roast

// 本测试隔离 P.y 奇数（share 取反）时签名是否有效。
// 强制 R.y 为偶数，排除 adjustNoncePairForBIP340 的影响。

import (
	"crypto/sha256"
	"math/big"
	"testing"

	"dex/frost/core/curve"
	frost "dex/frost/core/frost"
	coreRoast "dex/frost/core/roast"
	"dex/pb"
)

// TestBIP340_OddPubkeyY_EvenRy 隔离测试：P.y 奇 + R.y 偶
// 只有 normalizeSecretShareForBIP340 在起作用
func TestBIP340_OddPubkeyY_EvenRy(t *testing.T) {
	group := curve.NewSecp256k1Group()
	masterSecret := findMasterSecretWithOddGroupPubY(group)
	groupPub := group.ScalarBaseMult(masterSecret)
	groupPubSerialized := group.SerializePoint(groupPub)
	if groupPubSerialized[0] != 0x03 {
		t.Fatalf("expected prefix 0x03, got 0x%02x", groupPubSerialized[0])
	}
	t.Logf("group pubkey prefix=0x%02x (Y odd)", groupPubSerialized[0])

	threshold, numSigners := 2, 4
	shares, _ := generateTestShares(masterSecret, threshold, numSigners, group)
	signerIDs := []int{1, 2, 3}
	msg := sha256.Sum256([]byte("BIP340 odd-P even-R test"))

	// 找 R.y 为偶数的 nonce 组合
	var nonces []coreRoast.SignerNonce
	var R curve.Point
	for attempt := 0; attempt < 10000; attempt++ {
		nonces = make([]coreRoast.SignerNonce, len(signerIDs))
		for i, sid := range signerIDs {
			signer := NewSignerService(NodeID("n"), nil, nil)
			n, _ := signer.GenerateNonce()
			nonces[i] = coreRoast.SignerNonce{
				SignerID: sid, HidingNonce: n.HidingNonce, BindingNonce: n.BindingNonce,
				HidingPoint: n.HidingPoint, BindingPoint: n.BindingPoint,
			}
		}
		var err error
		R, err = coreRoast.ComputeGroupCommitment(nonces, msg[:], group)
		if err == nil && R.Y != nil && R.Y.Bit(0) == 0 {
			break
		}
	}
	t.Logf("R.y parity=%d (even)", R.Y.Bit(0))

	lambdas := coreRoast.ComputeLagrangeCoefficientsForSet(signerIDs, group.Order())
	e := coreRoast.ComputeChallenge(R, groupPub.X, msg[:], group)

	// ---- 方案 A: 带 normalizeSecretShareForBIP340（当前 Participant 行为）----
	partialSigsA := make([]coreRoast.SignerShare, len(signerIDs))
	for i, sid := range signerIDs {
		myShare := new(big.Int).Set(shares[sid])
		normalizedShare := normalizeSecretShareForBIP340(
			pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340, groupPubSerialized, myShare,
		)
		// R.y 偶 → adjustNoncePairForBIP340 不改变
		rho := coreRoast.ComputeBindingCoefficient(sid, msg[:], nonces, group)
		z := coreRoast.ComputePartialSignature(
			sid, nonces[i].HidingNonce, nonces[i].BindingNonce, normalizedShare,
			rho, lambdas[sid], e, group,
		)
		partialSigsA[i] = coreRoast.SignerShare{SignerID: sid, Share: z}
	}

	sigA, _ := coreRoast.AggregateSignaturesBIP340(R, partialSigsA, group)
	xOnly := groupPubSerialized[1:]
	verifyA := func() (valid bool, panicVal any) {
		defer func() { panicVal = recover() }()
		valid, _ = frost.VerifyBIP340(xOnly, msg[:], sigA)
		return
	}
	validA, panicA := verifyA()
	t.Logf("方案 A (normalize share): valid=%v panic=%v", validA, panicA)

	// ---- 方案 B: 不做 normalizeSecretShareForBIP340（原始 share）----
	partialSigsB := make([]coreRoast.SignerShare, len(signerIDs))
	for i, sid := range signerIDs {
		myShare := new(big.Int).Set(shares[sid])
		// 不 normalize
		rho := coreRoast.ComputeBindingCoefficient(sid, msg[:], nonces, group)
		z := coreRoast.ComputePartialSignature(
			sid, nonces[i].HidingNonce, nonces[i].BindingNonce, myShare,
			rho, lambdas[sid], e, group,
		)
		partialSigsB[i] = coreRoast.SignerShare{SignerID: sid, Share: z}
	}

	sigB, _ := coreRoast.AggregateSignaturesBIP340(R, partialSigsB, group)
	verifyB := func() (valid bool, panicVal any) {
		defer func() { panicVal = recover() }()
		valid, _ = frost.VerifyBIP340(xOnly, msg[:], sigB)
		return
	}
	validB, panicB := verifyB()
	t.Logf("方案 B (原始 share): valid=%v panic=%v", validB, panicB)

	// ---- 方案 C: 不 normalize share，但在 AggregateSignatures 里处理 P.y（即让聚合器翻转 z）----
	sigC, _ := coreRoast.AggregateSignatures(R, partialSigsB, group) // legacy: 翻转 R.y 奇的 z
	// R.y 偶数，所以 AggregateSignatures 不翻转 z，和 BIP340 一样
	// 但我们手动翻转 z（因为 P.y odd）
	zC := new(big.Int).SetBytes(sigC[32:])
	zFlipped := new(big.Int).Sub(group.Order(), zC)
	zFlipped.Mod(zFlipped, group.Order())
	sigC2 := make([]byte, 64)
	copy(sigC2[:32], sigC[:32])
	zFlipped.FillBytes(sigC2[32:])

	verifyC := func() (valid bool, panicVal any) {
		defer func() { panicVal = recover() }()
		valid, _ = frost.VerifyBIP340(xOnly, msg[:], sigC2)
		return
	}
	validC, panicC := verifyC()
	t.Logf("方案 C (原始 share + 手动翻转 z for P.y): valid=%v panic=%v", validC, panicC)

	if !validA && !validB {
		t.Logf("⚠️ 方案 A 和 B 都失败——说明 BIP340 FROST 需要在聚合层而非 share 层处理 P.y parity")
	}
}
