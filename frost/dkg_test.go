package dkg

import (
	"crypto/sha256"
	"fmt"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"math/big"
	"testing"
)

func Test_CompareWithBtcecSchnorr(t *testing.T) {
	curve := NewSecp2561Group()
	// 随机私钥
	privScalar := randomScalar(curve.Order())
	privKey, _ := btcec.PrivKeyFromBytes(privScalar.Bytes())

	// 原始消息
	msg := []byte("consistency check")
	// 先做一次 sha256，得到 32 字节
	digest := sha256.Sum256(msg)

	// —— 用内部实现签名 ——
	Rx, Ry, z, _ := SchnorrSign(curve, privScalar, digest[:])

	// —— 用外部库签名 ——
	sig, err := schnorr.Sign(privKey, digest[:])
	if err != nil {
		t.Fatalf("外部 schnorr.Sign 失败: %v", err)
	}
	sigBytes := sig.Serialize() // 64 字节
	if len(sigBytes) != 64 {
		t.Fatalf("外部签名长度不对: got %d, want 64", len(sigBytes))
	}

	// 拆分为 R.x 和 s
	RxExt := new(big.Int).SetBytes(sigBytes[:32])
	sExt := new(big.Int).SetBytes(sigBytes[32:])

	// 打印对比
	t.Logf(`
=== 内部 SchnorrSign ===
  R = (%s, %s)
  z = %s
=== 外部 schnorr.Sign ===
  R.x = %s
  s   = %s
`, Rx.Text(16), Ry.Text(16), z.Text(16), RxExt.Text(16), sExt.Text(16))

	// 自动断言
	if Rx.Cmp(RxExt) != 0 {
		t.Errorf("R.x 不一致：内部 %s，外部 %s", Rx.Text(16), RxExt.Text(16))
	}
	if z.Cmp(sExt) != 0 {
		t.Errorf("s/z 不一致：内部 %s，外部 %s", z.Text(16), sExt.Text(16))
	}
}

func Test_3_4_tweak(t *testing.T) {
	curve := NewSecp2561Group()
	N := curve.Order()
	thr, nPart := 3, 5 // t = 3  n = 5

	// ---------- 1. DKG：生成 s_j ----------
	parts := make([]*Polynomial, nPart)
	for i := range parts {
		parts[i] = NewPolynomial(thr, curve)
	}

	ids := make([]*big.Int, nPart) // 1..n
	sj := make([]*big.Int, nPart)
	for j := 0; j < nPart; j++ {
		ids[j] = big.NewInt(int64(j + 1))
		sum := big.NewInt(0)
		for _, p := range parts {
			sum.Add(sum, p.Evaluate(ids[j], curve))
		}
		sj[j] = sum.Mod(sum, N)
	}

	// ---------- 2. 聚合私钥 Q0 （偶‑Y 制度） ----------
	Q0 := big.NewInt(0)
	for _, p := range parts {
		Q0.Add(Q0, p.Coefficients[0])
	}
	Q0.Mod(Q0, N)
	Qx, Qy := curve.ScalarBaseMult(Q0).XY()
	if Qy.Bit(0) == 1 {
		for j := range sj {
			sj[j].Sub(N, sj[j])
		}
		Q0.Sub(N, Q0)
		Qy.Sub(curve.Modulus(), Qy)
	}

	// ---------- 3. TapTweak ----------
	tweak := tapTweakScalar(Qx, "TapTweak")
	Q0t := new(big.Int).Add(Q0, tweak)
	Q0t.Mod(Q0t, N)
	for j := range sj {
		sj[j].Add(sj[j], tweak)
		sj[j].Mod(sj[j], N)
	}

	Qtx, Qty := curve.ScalarBaseMult(Q0t).XY()
	if Qty.Bit(0) == 1 {
		Q0t.Sub(N, Q0t)
		Qty.Sub(curve.Modulus(), Qty)
		for j := range sj {
			sj[j].Sub(N, sj[j])
		}
	}
	// ★★ tweak后的私钥/公钥与 Taproot 地址 ★★
	{
		// 1) 打印 tweaked 私钥（十六进制）
		fmt.Printf("\n[tweak] Q0' (私钥): %s\n", Q0t.Text(16))

		// 2) 打印 tweaked 公钥 (x, y)
		fmt.Printf("[tweak] Q'   (公钥): (%s, %s)\n", Qtx.Text(16), Qty.Text(16))

		// 3) 序列化为 *btcec.PrivateKey，再转 WIF
		privTweaked, _ := btcec.PrivKeyFromBytes(Q0t.FillBytes(make([]byte, 32)))
		wifT, _ := btcutil.NewWIF(privTweaked, &chaincfg.TestNet3Params, true)
		fmt.Println("[tweak] 私钥 (WIF):", wifT.String())

		// 4) 用 tweaked 私钥生成 Taproot 地址
		addrPriv, _ := btcutil.NewAddressTaproot(
			schnorr.SerializePubKey(privTweaked.PubKey()),
			&chaincfg.TestNet3Params,
		)
		fmt.Println("[tweak] 私钥→地址：", addrPriv.EncodeAddress())

		// 5) 用 tweaked 公钥生成 Taproot 地址
		pubTweaked := btcec.NewPublicKey(
			bigIntToFieldVal(Qtx),
			bigIntToFieldVal(Qty),
		)
		addrPub, _ := btcutil.NewAddressTaproot(
			schnorr.SerializePubKey(pubTweaked),
			&chaincfg.TestNet3Params,
		)
		fmt.Println("[tweak] 公钥→地址：", addrPub.EncodeAddress())

		// 6) 双重校验（理论上两条地址必须一致）
		if addrPub.EncodeAddress() != addrPriv.EncodeAddress() {
			t.Fatalf("公钥/私钥 tweak 地址不一致")
		}
		// 7) ★★ 打印每个参与者 tweak‑后的 s_j ★★
		fmt.Println("\n[tweak] 各参与者私钥份额 s_j' (已加 tweak 并偶‑Y):")
		for i, share := range sj {
			fmt.Printf("  参与者 %d : %s\n", i+1, share.Text(16))
		}
	}
	// ---------- 4. 单签（对照） ----------
	msg := sha256.Sum256([]byte("Hello, threshold sig"))
	RxDir, RyDir, zDir, kDir := SchnorrSign(curve, Q0t, msg[:])

	// ---------- 5. Round‑1：挑 3 个参与者 ----------
	choose := []int{1, 2, 4}
	if devMode {
		forceK = kDir
		defer func() { forceK = nil }()
	}

	idsSel := make([]*big.Int, thr)
	kSel := make([]*big.Int, thr)
	RxSel := make([]*big.Int, thr)
	RySel := make([]*big.Int, thr)
	sSel := make([]*big.Int, thr)

	for i, idx := range choose {
		k, Rx, Ry := ThresholdSchnorrPartialSign(curve, sj[idx], Qtx, Qty, msg[:])
		idsSel[i], kSel[i] = ids[idx], k
		RxSel[i], RySel[i] = Rx, Ry
		sSel[i] = sj[idx]
	}

	// ---------- 6. 收集承诺点 R，看奇偶，广播RxSum ----------
	RxSum, RySum := RxSel[0], RySel[0]
	for i := 1; i < thr; i++ {
		RSum := Point{X: RxSum, Y: RySum}
		RSel := Point{X: RxSel[i], Y: RySel[i]}
		RxSum, RySum = curve.Add(RSum, RSel).XY()
	}

	if RySum.Bit(0) == 1 { // 若总 R.y 为奇 → 全体取反
		for i := 0; i < thr; i++ {
			kSel[i].Sub(N, kSel[i])                 // k_i ← n-k_i
			RySel[i].Sub(curve.Modulus(), RySel[i]) // R_i ← -R_i
		}
		RySum.Sub(curve.Modulus(), RySum) // R_sum.y 变偶
	}

	// ---------- 7. 计算 λ_i, e, z_i ----------
	λ := ComputeLagrangeCoefficients(idsSel, N)
	// Qtx：聚合公钥的x坐标
	e := bip340Challenge(RxSum, Qtx, msg[:], curve)

	zSel := make([]*big.Int, thr)
	for i := 0; i < thr; i++ {
		zSel[i] = ThresholdSchnorrFinalize(curve,
			kSel[i], λ[i], e, sSel[i])
	}

	// ---------- 8. 协调者收集z_i聚合 ----------
	Rax, Ray, zAgg := AggregateThresholdSignature(
		curve, idsSel, RxSel, RySel, zSel,
		Qtx, msg[:],
	)

	// ---------- 9. 验证 ----------
	if !SchnorrVerify(curve, Qtx, Qty, Rax, Ray, zAgg, msg[:]) {
		t.Fatalf("阈值聚合签名验证失败")
	}
	if !SchnorrVerify(curve, Qtx, Qty, RxDir, RyDir, zDir, msg[:]) {
		t.Fatalf("完整私钥签名验证失败")
	}
}
