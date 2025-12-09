package dkg

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
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

// Test_GenerateThresholdKeys 生成各参与者的私钥份额和聚合 Taproot 地址
// 运行后可将输出的私钥份额用于 Test_ThresholdSignRawTX
func Test_GenerateThresholdKeys(t *testing.T) {
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
	fmt.Println("\n========== 门限密钥生成结果 ==========")

	// 1) 打印 tweaked 私钥（十六进制）
	fmt.Printf("\n[tweak] Q0' (聚合私钥): %s\n", Q0t.Text(16))

	// 2) 打印 tweaked 公钥 (x, y)
	fmt.Printf("[tweak] Q'   (聚合公钥): (%s, %s)\n", Qtx.Text(16), Qty.Text(16))

	// 3) 序列化为 *btcec.PrivateKey，再转 WIF
	privTweaked, _ := btcec.PrivKeyFromBytes(Q0t.FillBytes(make([]byte, 32)))
	wifT, _ := btcutil.NewWIF(privTweaked, &chaincfg.TestNet3Params, true)
	fmt.Println("[tweak] 聚合私钥 (WIF):", wifT.String())

	// 4) 用 tweaked 私钥生成 Taproot 地址
	addrPriv, _ := btcutil.NewAddressTaproot(
		schnorr.SerializePubKey(privTweaked.PubKey()),
		&chaincfg.TestNet3Params,
	)
	fmt.Println("[tweak] Taproot 地址：", addrPriv.EncodeAddress())

	// 5) 用 tweaked 公钥生成 Taproot 地址（校验）
	pubTweaked := btcec.NewPublicKey(
		bigIntToFieldVal(Qtx),
		bigIntToFieldVal(Qty),
	)
	addrPub, _ := btcutil.NewAddressTaproot(
		schnorr.SerializePubKey(pubTweaked),
		&chaincfg.TestNet3Params,
	)

	// 6) 双重校验（理论上两条地址必须一致）
	if addrPub.EncodeAddress() != addrPriv.EncodeAddress() {
		t.Fatalf("公钥/私钥 tweak 地址不一致")
	}

	// 7) ★★ 打印每个参与者 tweak‑后的 s_j ★★
	fmt.Println("\n[tweak] 各参与者私钥份额 s_j' (已加 tweak 并偶‑Y):")
	for i, share := range sj {
		fmt.Printf("  参与者 %d : %s\n", i+1, share.Text(16))
	}

	// 8) 打印聚合公钥 X 坐标（用于签名验证）
	fmt.Printf("\n[tweak] 聚合公钥 X (用于签名): %s\n", Qtx.Text(16))

	fmt.Println("\n========================================")
	fmt.Println("请将上述私钥份额和公钥 X 坐标复制到 Test_ThresholdSignRawTX 中使用")
	fmt.Println("========================================")

	t.Logf("门限密钥生成成功: %d-of-%d, 地址: %s", thr, nPart, addrPriv.EncodeAddress())
}

// Test_ThresholdSignRawTX 使用预设的私钥份额和 UTXO 生成门限签名 Raw TX
// 私钥份额来自 Test_GenerateThresholdKeys 的输出
func Test_ThresholdSignRawTX(t *testing.T) {
	curve := NewSecp2561Group()
	thr := 3 // 门限值

	// ========== 配置区：请根据 Test_GenerateThresholdKeys 输出填写 ==========

	// 聚合公钥 X 坐标（用于签名验证）
	QtxHex := "19ea65f3443b5e5253326a0234803ffd2a1cd5dd2f0b0a6c10dc2fd449f05f0f" // 示例值，请替换

	// 各参与者的私钥份额（已 tweak 并偶-Y）
	// 格式: map[参与者ID] = 私钥份额(hex)
	participantShares := map[int]string{
		1: "406125103d66fc2f17d15c9ba1e72866dc2d4b162a506bd1246fdc61d7dd3041", // 示例值
		2: "d4c1013eb479010e89c147a58553fe623105fdbb902b240a6e18bfb04b0819ef", // 示例值
		3: "9ec069c8ecd99a4db99350c283896b10814426889757ee66d8f657cfd7f11762", // 示例值
		4: "9e5f5eaee688c7eca74777f29c876e708796a263ef1f6b2224db034d4ece69db", // 示例值
		5: "d39ddff0a18689eb52ddbd35d04e088243fd714d97819a3c51c6c228afa0115a", // 示例值
	}

	// 选择参与签名的参与者（需要 >= thr 个）
	chosenParticipants := []int{1, 2, 4}

	// UTXO 配置
	prevTxHashStr := "b61c72b21030e0306478b3fb8c01827ee15e108a4652a6f994ba9a382861cf9a" // 前序交易哈希
	prevOutIndex := uint32(0)                                                           // 输出索引
	prevOutAmount := int64(500000)                                                      // UTXO 金额 (satoshis)

	// 输出配置
	outputAddr := "tb1pr84xtu6y8d09y5ejdgprfqpll54pe4wa9u9s5mqsmshagj0stu8sxha5ss" // 接收地址
	outputAmount := int64(499500)                                                  // 输出金额 (satoshis)

	// ========== 配置区结束 ==========

	// ---------- 1. 解析配置 ----------
	Qtx, ok := new(big.Int).SetString(QtxHex, 16)
	if !ok {
		t.Fatalf("解析聚合公钥 X 失败")
	}

	// 解析选中参与者的私钥份额
	idsSel := make([]*big.Int, thr)
	sjSel := make([]*big.Int, thr)
	for i, pid := range chosenParticipants {
		idsSel[i] = big.NewInt(int64(pid))
		shareHex, exists := participantShares[pid]
		if !exists {
			t.Fatalf("参与者 %d 的私钥份额不存在", pid)
		}
		share, ok := new(big.Int).SetString(shareHex, 16)
		if !ok {
			t.Fatalf("解析参与者 %d 的私钥份额失败", pid)
		}
		sjSel[i] = share
	}

	// ---------- 2. 构造交易 ----------
	tx := wire.NewMsgTx(wire.TxVersion)

	// 添加输入 (UTXO)
	prevTxHash, err := chainhash.NewHashFromStr(prevTxHashStr)
	if err != nil {
		t.Fatalf("解析前序交易哈希失败: %v", err)
	}
	outPoint := wire.NewOutPoint(prevTxHash, prevOutIndex)
	txIn := wire.NewTxIn(outPoint, nil, nil)
	tx.AddTxIn(txIn)

	// 添加输出
	addr, err := btcutil.DecodeAddress(outputAddr, &chaincfg.TestNet3Params)
	if err != nil {
		t.Fatalf("解析输出地址失败: %v", err)
	}
	pkScript, err := txscript.PayToAddrScript(addr)
	if err != nil {
		t.Fatalf("生成输出脚本失败: %v", err)
	}
	txOut := wire.NewTxOut(outputAmount, pkScript)
	tx.AddTxOut(txOut)

	// ---------- 3. 计算签名哈希 ----------
	fetcher := txscript.NewCannedPrevOutputFetcher(pkScript, prevOutAmount)
	sigHashes := txscript.NewTxSigHashes(tx, fetcher)
	sigHash, err := txscript.CalcTaprootSignatureHash(
		sigHashes,
		txscript.SigHashDefault,
		tx,
		0, // 第一个输入
		fetcher,
	)
	if err != nil {
		t.Fatalf("计算签名哈希失败: %v", err)
	}

	// ---------- 4. 门限签名 ----------
	fmt.Println("\n========== 门限签名过程 ==========")
	fmt.Printf("参与者: %v\n", chosenParticipants)
	fmt.Printf("签名哈希: %s\n", hex.EncodeToString(sigHash))

	sigBytes, err := ThresholdSign(
		curve,
		idsSel,
		sjSel,
		sigHash,
		Qtx,
		bip340Challenge, // 使用BIP-340 challenge函数
	)
	if err != nil {
		t.Fatalf("门限签名失败: %v", err)
	}

	// ---------- 5. 添加witness到交易 ----------
	tx.TxIn[0].Witness = wire.TxWitness{sigBytes}

	// ---------- 6. 输出Raw TX ----------
	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		t.Fatalf("序列化交易失败: %v", err)
	}

	rawTx := hex.EncodeToString(buf.Bytes())
	fmt.Println("\n========== 门限签名生成的 Raw TX ==========")
	fmt.Printf("Raw TX: %s\n", rawTx)
	fmt.Printf("TX Size: %d bytes\n", len(buf.Bytes()))
	fmt.Printf("Signature: %s\n", hex.EncodeToString(sigBytes))
	fmt.Println("============================================")

	t.Logf("门限签名Raw TX生成成功，长度: %d bytes", len(buf.Bytes()))
}
