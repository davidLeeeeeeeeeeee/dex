package roast

// 本测试证明 BIP340 签名聚合存在 R.y parity 双重取反 bug：
// - Participant 端通过 adjustNoncePairForBIP340 + normalizeSecretShareForBIP340 已经处理了 parity
// - Coordinator 端 AggregateSignatures (core/roast) 又做了一次 R.y 奇数时取反 z
// - 导致最终签名无效
//
// 修复方案：Secp256k1ROASTExecutor.AggregateSignatures 应调用 AggregateSignaturesBIP340

import (
	"crypto/sha256"
	"math/big"
	"testing"

	"dex/frost/core/curve"
	frost "dex/frost/core/frost"
	coreRoast "dex/frost/core/roast"
	"dex/pb"
)

// findMasterSecretWithOddGroupPubY 寻找一个 masterSecret，使得其群公钥 Y 为奇数（前缀 03）
func findMasterSecretWithOddGroupPubY(group curve.Group) *big.Int {
	// 从一个基准值开始搜索
	seed := new(big.Int).SetBytes([]byte{0xde, 0xad, 0xbe, 0xef, 0xca, 0xfe, 0xba, 0xbe})
	for i := 0; i < 10000; i++ {
		candidate := new(big.Int).Add(seed, big.NewInt(int64(i)))
		pub := group.ScalarBaseMult(candidate)
		if pub.Y.Bit(0) == 1 { // Y 为奇数
			return candidate
		}
	}
	panic("unreachable: could not find master secret with odd Y group pubkey")
}

// findNoncesWithOddRy 反复生成 nonce 直到群承诺 R.y 为奇数
func findNoncesWithOddRy(signerIDs []int, msg []byte, group curve.Group) ([]coreRoast.SignerNonce, curve.Point) {
	for attempt := 0; attempt < 10000; attempt++ {
		nonces := make([]coreRoast.SignerNonce, len(signerIDs))
		for i, sid := range signerIDs {
			signer := NewSignerService(NodeID("node"), nil, nil)
			n, _ := signer.GenerateNonce()
			nonces[i] = coreRoast.SignerNonce{
				SignerID: sid, HidingNonce: n.HidingNonce, BindingNonce: n.BindingNonce,
				HidingPoint: n.HidingPoint, BindingPoint: n.BindingPoint,
			}
		}
		R, err := coreRoast.ComputeGroupCommitment(nonces, msg, group)
		if err != nil {
			continue
		}
		if R.Y != nil && R.Y.Bit(0) == 1 { // R.y 为奇数
			return nonces, R
		}
	}
	panic("unreachable: could not find nonces with odd R.y")
}

// TestBIP340_DoubleNegation_Bug 证明双重取反 bug 的存在
//
// 完整模拟 Participant + Coordinator 流程：
//  1. 构造 group pubkey Y 为奇数（03 前缀）的密钥
//  2. 构造 R.y 为奇数的 nonce 组合（同时触发两个 parity 路径）
//  3. Participant 端做 BIP340 调整后计算 partial signature
//  4. Coordinator 端用 AggregateSignatures 聚合 → 验证失败（bug）
//  5. 用 AggregateSignaturesBIP340 聚合 → 验证成功（修复）
func TestBIP340_DoubleNegation_Bug(t *testing.T) {
	group := curve.NewSecp256k1Group()

	// ---- 1. 构造 odd-Y group pubkey ----
	masterSecret := findMasterSecretWithOddGroupPubY(group)
	groupPub := group.ScalarBaseMult(masterSecret)
	groupPubSerialized := group.SerializePoint(groupPub) // 33 字节，前缀应为 03
	if groupPubSerialized[0] != 0x03 {
		t.Fatalf("expected group pubkey prefix 0x03, got 0x%02x", groupPubSerialized[0])
	}
	t.Logf("✅ group pubkey prefix = 0x03 (Y is odd)")

	threshold, numSigners := 2, 4
	shares, _ := generateTestShares(masterSecret, threshold, numSigners, group)
	signerIDs := []int{1, 2, 3} // threshold+1

	msg := sha256.Sum256([]byte("BIP340 double negation test"))

	// ---- 2. 找到 R.y 为奇数的 nonce 组合 ----
	nonces, R := findNoncesWithOddRy(signerIDs, msg[:], group)
	t.Logf("✅ R.y is odd (parity=%d)", R.Y.Bit(0))

	// ---- 3. 模拟 Participant 端 BIP340 调整 ----
	lambdas := coreRoast.ComputeLagrangeCoefficientsForSet(signerIDs, group.Order())
	e := coreRoast.ComputeChallenge(R, groupPub.X, msg[:], group)

	partialSigs := make([]coreRoast.SignerShare, len(signerIDs))
	for i, sid := range signerIDs {
		myShare := new(big.Int).Set(shares[sid])

		// Participant: normalizeSecretShareForBIP340
		// 当 groupPub Y 为奇时，取反 share
		normalizedShare := normalizeSecretShareForBIP340(
			pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
			groupPubSerialized,
			myShare,
		)

		// Participant: adjustNoncePairForBIP340
		// 当 R.y 为奇时，取反 nonces
		adjH, adjB := adjustNoncePairForBIP340(
			pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
			CurvePoint(R),
			nonces[i].HidingNonce,
			nonces[i].BindingNonce,
		)

		rho := coreRoast.ComputeBindingCoefficient(sid, msg[:], nonces, group)

		// Participant: ComputePartialSignature（非 BIP340 版本，不做内部取反）
		z := coreRoast.ComputePartialSignature(
			sid, adjH, adjB, normalizedShare, rho, lambdas[sid], e, group,
		)
		partialSigs[i] = coreRoast.SignerShare{SignerID: sid, Share: z}
	}
	t.Logf("✅ Participant 端 partial signatures 已生成（含 BIP340 parity 调整）")

	// ---- 4. Coordinator 端：用当前的 AggregateSignatures（有 bug）----
	sigBuggy, err := coreRoast.AggregateSignatures(R, partialSigs, group)
	if err != nil {
		t.Fatalf("AggregateSignatures failed: %v", err)
	}

	// x-only pubkey for verification
	xOnlyPubkey := groupPubSerialized[1:] // 去掉前缀，取 32 字节

	// 验证——应该失败（双重取反 bug）
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("✅ AggregateSignatures (legacy) 验证 panic（符合预期）: %v", r)
			}
		}()
		valid, verifyErr := frost.VerifyBIP340(xOnlyPubkey, msg[:], sigBuggy)
		if verifyErr != nil {
			t.Logf("✅ AggregateSignatures (legacy) 验证出错（符合预期）: %v", verifyErr)
			return
		}
		if valid {
			t.Fatalf("❌ 不应通过验证！AggregateSignatures 不应该产生合法的 BIP340 签名（当 participant 已做 parity 调整时）")
		}
		t.Logf("✅ AggregateSignatures (legacy) 验证失败（符合预期）: valid=false")
	}()

	// ---- 5. 修复：用 AggregateSignaturesBIP340（不做额外取反）----
	sigFixed, err := coreRoast.AggregateSignaturesBIP340(R, partialSigs, group)
	if err != nil {
		t.Fatalf("AggregateSignaturesBIP340 failed: %v", err)
	}

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("❌ AggregateSignaturesBIP340 验证 panic（不应发生）: %v", r)
			}
		}()
		valid, verifyErr := frost.VerifyBIP340(xOnlyPubkey, msg[:], sigFixed)
		if verifyErr != nil {
			t.Fatalf("❌ AggregateSignaturesBIP340 验证出错: %v", verifyErr)
		}
		if !valid {
			t.Fatalf("❌ AggregateSignaturesBIP340 验证失败")
		}
		t.Logf("✅ AggregateSignaturesBIP340 验证通过！签名合法")
	}()

	t.Logf("\n=== 结论 ===")
	t.Logf("当 Participant 端已做 BIP340 parity 调整时：")
	t.Logf("  AggregateSignatures   → ❌ 验证失败（R.y 奇数时 z 被重复取反）")
	t.Logf("  AggregateSignaturesBIP340 → ✅ 验证成功")
	t.Logf("修复：Secp256k1ROASTExecutor.AggregateSignatures 应调用 AggregateSignaturesBIP340")
}
