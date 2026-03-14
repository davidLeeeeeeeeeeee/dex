// frost/runtime/roast/roast_e2e_ed25519_test.go
// 端到端 ROAST Ed25519 (SOL) 门限签名测试：
// 使用已知正确的 DKG 份额，模拟完整的 FROST 门限签名流程，
// 然后用 crypto/ed25519.Verify 验证最终签名，并交叉验证 challenge 计算
// 与 Solana Ed25519SigVerify 指令一致。

package roast

import (
	"crypto"
	"crypto/ed25519"
	"encoding/hex"
	"math/big"
	"testing"

	"dex/frost/core/curve"
	"dex/frost/core/dkg"
	frost "dex/frost/core/frost"
	coreRoast "dex/frost/core/roast"
	"dex/pb"
)

// TestROAST_EndToEnd_Ed25519 端到端 ROAST Ed25519 门限签名测试
//
// 测试策略：
// 1. 用已知 masterSecret 生成 Shamir 份额（Ed25519 曲线）
// 2. 模拟各参与者生成 nonce 对
// 3. 每个参与者计算 partial signature
// 4. 聚合签名
// 5. 用 crypto/ed25519.Verify 验证
// 6. 用 frost.VerifyEd25519 交叉验证
// 7. 验签方程 s·G == R + e·A（点运算层面）
func TestROAST_EndToEnd_Ed25519(t *testing.T) {
	group := curve.NewEd25519Group()

	tests := []struct {
		name       string
		threshold  int
		numSigners int
		signerIDs  []int
		secret     *big.Int
		msgText    string
	}{
		{
			name: "3-of-4_basic", threshold: 3, numSigners: 4,
			signerIDs: []int{1, 2, 3, 4},
			secret:    new(big.Int).SetBytes([]byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0}),
			msgText:   "e2e test message for Ed25519 ROAST signing",
		},
		{
			name: "2-of-3_minimal", threshold: 2, numSigners: 3,
			signerIDs: []int{1, 2, 3},
			secret:    new(big.Int).SetBytes([]byte{0xab, 0xcd, 0xef}),
			msgText:   "minimal threshold test Ed25519",
		},
		{
			name: "3-of-5_subset", threshold: 3, numSigners: 5,
			signerIDs: []int{2, 3, 4, 5},
			secret:    new(big.Int).SetBytes([]byte{0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x10}),
			msgText:   "subset signing test Ed25519",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runEndToEndEd25519(t, group, tc.threshold, tc.numSigners, tc.signerIDs, tc.secret, tc.msgText)
		})
	}
}

func runEndToEndEd25519(
	t *testing.T, group curve.Group,
	threshold, numSigners int, signerIDs []int,
	masterSecret *big.Int, msgText string,
) {
	t.Helper()

	// ============ Step 1: 生成 DKG 份额 ============
	groupPub := group.ScalarBaseMult(masterSecret)
	shares, _ := generateTestShares(masterSecret, threshold, numSigners, group)
	groupPubSerialized := group.SerializePoint(groupPub) // 32 字节压缩格式

	t.Logf("=== DKG 份额 ===")
	t.Logf("  masterSecret = %s", masterSecret.Text(16))
	t.Logf("  groupPub     = %s", hex.EncodeToString(groupPubSerialized))
	for _, sid := range signerIDs {
		t.Logf("  share[%d] = %s", sid, shares[sid].Text(16))
	}

	// 验证 Shamir 份额的正确性
	verifyShamirReconstructionEd25519(t, signerIDs, shares, masterSecret, group)

	// ============ Step 2: 生成 nonce 对 ============
	msg := []byte(msgText)

	type nonceData struct {
		hidingK, bindingK   *big.Int
		hidingPt, bindingPt curve.Point
	}
	signerNonces := make(map[int]*nonceData)

	for _, sid := range signerIDs {
		hk := dkg.RandomScalar(group.Order())
		bk := dkg.RandomScalar(group.Order())
		hp := group.ScalarBaseMult(hk)
		bp := group.ScalarBaseMult(bk)
		signerNonces[sid] = &nonceData{hk, bk, hp, bp}
	}
	t.Logf("=== Nonce 对 (共 %d 个签名者) ===", len(signerIDs))

	// ============ Step 3: 计算群承诺 R ============
	coreNonces := make([]coreRoast.SignerNonce, len(signerIDs))
	for i, sid := range signerIDs {
		n := signerNonces[sid]
		coreNonces[i] = coreRoast.SignerNonce{
			SignerID: sid, HidingNonce: n.hidingK, BindingNonce: n.bindingK,
			HidingPoint: n.hidingPt, BindingPoint: n.bindingPt,
		}
	}

	R, err := coreRoast.ComputeGroupCommitment(coreNonces, msg, group)
	if err != nil {
		t.Fatalf("ComputeGroupCommitment failed: %v", err)
	}
	rSerialized := group.SerializePoint(R)
	t.Logf("=== 群承诺 R ===")
	t.Logf("  R = %s", hex.EncodeToString(rSerialized))

	// ============ Step 4: 计算 challenge（RFC 8032 Ed25519 规范）============
	// e = SHA-512(R_compressed || pk_compressed || msg) mod L（小端序解读）
	e := computeEd25519ChallengeForTest(rSerialized, groupPubSerialized, msg, group.Order())
	lambdas := coreRoast.ComputeLagrangeCoefficientsForSet(signerIDs, group.Order())

	t.Logf("=== Challenge & Lagrange ===")
	t.Logf("  e = %s", e.Text(16))

	// ============ Step 5: 每个参与者计算 partial signature ============
	partialSigs := make(map[int]*big.Int)
	for _, sid := range signerIDs {
		n := signerNonces[sid]
		rho := coreRoast.ComputeBindingCoefficient(sid, msg, coreNonces, group)

		// Ed25519 不需要 BIP340 parity 调整，直接计算
		z := coreRoast.ComputePartialSignature(sid,
			n.hidingK, n.bindingK, shares[sid],
			rho, lambdas[sid], e, group)
		partialSigs[sid] = z

		t.Logf("  signer[%d] z_i = %s", sid, z.Text(16))
	}

	// ============ Step 6: 聚合签名 ============
	// z = Σ z_i mod L
	zAgg := big.NewInt(0)
	for _, sid := range signerIDs {
		zAgg.Add(zAgg, partialSigs[sid])
	}
	zAgg.Mod(zAgg, group.Order())

	// Ed25519 签名格式: R_compressed(32) || z_le(32) = 64 字节
	sig := make([]byte, 64)
	copy(sig[:32], rSerialized)
	zBytes := make([]byte, 32)
	zAgg.FillBytes(zBytes)
	copy(sig[32:], reverseBytes(zBytes)) // big-endian -> little-endian

	t.Logf("=== 聚合签名 ===")
	t.Logf("  sig = %s", hex.EncodeToString(sig))

	// ============ Step 7: 用 crypto/ed25519.Verify 验证 ============
	pubKey := ed25519.PublicKey(groupPubSerialized)
	stdValid := ed25519.Verify(pubKey, msg, sig)

	t.Logf("=== ed25519.Verify ===")
	t.Logf("  valid=%v", stdValid)

	if !stdValid {
		t.Fatalf("❌ ed25519.Verify 验证失败")
	}

	// ============ Step 8: 用 frost.VerifyEd25519 交叉验证 ============
	frostValid, frostErr := frost.VerifyEd25519(groupPubSerialized, msg, sig)
	t.Logf("=== frost.VerifyEd25519 ===")
	t.Logf("  valid=%v err=%v", frostValid, frostErr)

	if frostErr != nil {
		t.Fatalf("❌ frost.VerifyEd25519 返回错误: %v", frostErr)
	}
	if !frostValid {
		t.Fatalf("❌ frost.VerifyEd25519 验证失败")
	}

	// ============ Step 9: 通过 Verify API 验证 ============
	apiValid, apiErr := frost.Verify(pb.SignAlgo_SIGN_ALGO_ED25519, groupPubSerialized, msg, sig)
	if apiErr != nil {
		t.Fatalf("❌ frost.Verify API 返回错误: %v", apiErr)
	}
	if !apiValid {
		t.Fatalf("❌ frost.Verify API 验证失败")
	}

	// ============ Step 10: 独立 challenge 交叉验证 ============
	// 重新计算 challenge，确保与 Step 4 一致
	eIndependent := computeEd25519ChallengeIndependent(rSerialized, groupPubSerialized, msg, group.Order())
	if e.Cmp(eIndependent) != 0 {
		t.Fatalf("❌ Ed25519 challenge 不一致:\n  Step4:       %s\n  Independent: %s",
			e.Text(16), eIndependent.Text(16))
	}
	t.Logf("=== Ed25519 challenge 交叉验证 ===")
	t.Logf("  ✅ challenge 一致 = %s", e.Text(16))

	// ============ Step 11: 验证 s·G == R + e·A（点运算验签方程）============
	sG := group.ScalarBaseMult(zAgg)
	eA := group.ScalarMult(groupPub, e)
	rhs := group.Add(R, eA)
	sGSerialized := group.SerializePoint(sG)
	rhsSerialized := group.SerializePoint(rhs)
	if hex.EncodeToString(sGSerialized) != hex.EncodeToString(rhsSerialized) {
		t.Fatalf("❌ 验签方程不满足: s·G ≠ R + e·A\n  s·G   = %s\n  R+e·A = %s",
			hex.EncodeToString(sGSerialized), hex.EncodeToString(rhsSerialized))
	}
	t.Logf("  ✅ s·G == R + e·A 验证通过")

	t.Logf("✅ 端到端 ROAST Ed25519 门限签名验证全部通过")
}

// computeEd25519ChallengeForTest 计算 Ed25519 challenge（RFC 8032 规范）
// e = SHA-512(R || pk || msg) mod L，SHA-512 输出以小端序解读为整数
func computeEd25519ChallengeForTest(rCompressed, pkCompressed, msg []byte, order *big.Int) *big.Int {
	h := crypto.SHA512.New()
	h.Write(rCompressed)
	h.Write(pkCompressed)
	h.Write(msg)
	digest := h.Sum(nil)
	// SHA-512 输出 64 字节，以小端序解读为整数后 mod L
	e := new(big.Int).SetBytes(reverseBytes(digest))
	return e.Mod(e, order)
}

// computeEd25519ChallengeIndependent 独立实现的 Ed25519 challenge 计算（用于交叉验证）
// 逻辑与 computeEd25519ChallengeForTest 完全相同，确保无笔误
func computeEd25519ChallengeIndependent(rBytes, pkBytes, msg []byte, order *big.Int) *big.Int {
	h := crypto.SHA512.New()
	h.Write(rBytes)  // R point compressed (32 bytes)
	h.Write(pkBytes) // public key compressed (32 bytes)
	h.Write(msg)     // message
	raw := h.Sum(nil)
	// little-endian -> big.Int
	reversed := make([]byte, len(raw))
	for i, j := 0, len(raw)-1; i < len(raw); i, j = i+1, j-1 {
		reversed[i] = raw[j]
	}
	val := new(big.Int).SetBytes(reversed)
	return val.Mod(val, order)
}

func verifyShamirReconstructionEd25519(t *testing.T, signerIDs []int, shares map[int]*big.Int, expected *big.Int, group curve.Group) {
	t.Helper()
	order := group.Order()
	lambdas := coreRoast.ComputeLagrangeCoefficientsForSet(signerIDs, order)

	reconstructed := big.NewInt(0)
	for _, sid := range signerIDs {
		term := new(big.Int).Mul(lambdas[sid], shares[sid])
		term.Mod(term, order)
		reconstructed.Add(reconstructed, term)
		reconstructed.Mod(reconstructed, order)
	}

	if reconstructed.Cmp(expected) != 0 {
		t.Fatalf("❌ Shamir 重建失败: expected=%s got=%s", expected.Text(16), reconstructed.Text(16))
	}
	t.Logf("✅ Shamir 重建验证通过: Σλ_i·s_i = masterSecret")
}

// reverseBytes 返回字节切片的反转副本（字节序转换）
func reverseBytes(s []byte) []byte {
	res := make([]byte, len(s))
	for i, j := 0, len(s)-1; i < len(s); i, j = i+1, j-1 {
		res[i] = s[j]
	}
	return res
}
