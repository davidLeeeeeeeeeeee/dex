package dkg

import (
	_ "crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"github.com/btcsuite/btcd/btcec/v2"
	"golang.org/x/crypto/sha3"
	"io"
	"math/big"
)

// 使用可替换的随机数源，默认为 crypto/rand.Reader
var randReader io.Reader = rand.Reader

// 开发模式：允许强制使用同一个k进行阈值签名，以验证一致性
var devMode = false
var forceK *big.Int

// ------------------------- 基础工具函数 -------------------------

// 生成随机标量 [0, N-1]
func randomScalar(n *big.Int) *big.Int {
	scalar, err := rand.Int(randReader, n)
	if err != nil {
		panic(err)
	}
	return scalar
}

// ------------------------- DKG相关 -------------------------
// Polynomial 为t-1阶多项式，用于分布式密钥生成
type Polynomial struct {
	Coefficients []*big.Int // a_0, a_1, ..., a_(t-1)
}

// NewPolynomial 生成随机t-1阶多项式
func NewPolynomial(t int, curve Group) *Polynomial {
	coeffs := make([]*big.Int, t)
	for i := 0; i < t; i++ {
		coeffs[i] = randomScalar(curve.Order())
	}
	return &Polynomial{Coefficients: coeffs}
}

// Evaluate 在x处求值f(x) mod N
func (p *Polynomial) Evaluate(x *big.Int, curve Group) *big.Int {
	N := curve.Order()
	result := big.NewInt(0)
	temp := big.NewInt(1)
	for i, coef := range p.Coefficients {
		temp.Exp(x, big.NewInt(int64(i)), N)
		temp.Mul(temp, coef)
		temp.Mod(temp, N)
		result.Add(result, temp)
		result.Mod(result, N)
	}
	return result
}

// ------------------------- 拉格朗日插值 -------------------------

// LagrangeInterpolation 根据t个份额恢复f(0)
func LagrangeInterpolation(shares [][2]*big.Int, N *big.Int) *big.Int {
	f0 := big.NewInt(0)
	for i := range shares {
		xi, yi := shares[i][0], shares[i][1]
		num := big.NewInt(1)
		den := big.NewInt(1)
		for j := range shares {
			if i == j {
				continue
			}
			xj := shares[j][0]
			temp := new(big.Int).Neg(xj)
			temp.Mod(temp, N)
			num.Mul(num, temp)
			num.Mod(num, N)

			diff := new(big.Int).Sub(xi, xj)
			diff.Mod(diff, N)
			den.Mul(den, diff)
			den.Mod(den, N)
		}

		denInv := new(big.Int).ModInverse(den, N)
		li := new(big.Int).Mul(num, denInv)
		li.Mod(li, N)

		term := new(big.Int).Mul(yi, li)
		term.Mod(term, N)
		f0.Add(f0, term)
		f0.Mod(f0, N)
	}
	return f0
}

// ComputeLagrangeCoefficients 为给定的一组参与者ID计算拉格朗日系数
func ComputeLagrangeCoefficients(selectedIDs []*big.Int, N *big.Int) []*big.Int {
	coeffs := make([]*big.Int, len(selectedIDs))
	for i := range selectedIDs {
		xi := selectedIDs[i]
		num := big.NewInt(1)
		den := big.NewInt(1)
		for j := range selectedIDs {
			if i == j {
				continue
			}
			xj := selectedIDs[j]
			tmp := new(big.Int).Neg(xj)
			tmp.Mod(tmp, N)
			num.Mul(num, tmp)
			num.Mod(num, N)

			diff := new(big.Int).Sub(xi, xj)
			diff.Mod(diff, N)
			den.Mul(den, diff)
			den.Mod(den, N)
		}
		denInv := new(big.Int).ModInverse(den, N)
		li := new(big.Int).Mul(num, denInv)
		li.Mod(li, N)
		coeffs[i] = li
	}
	return coeffs
}

// ------------------------- Schnorr签名与验证 -------------------------

// 固定的 32 byte RFC-6979 extra data —— btcsuite 源码里的 rfc6979ExtraDataV0
var rfc6979ExtraDataV0 = [32]byte{
	0xa3, 0xeb, 0x4c, 0x18, 0x2f, 0xae, 0x7e, 0xf4,
	0xe8, 0x10, 0xc6, 0xee, 0x13, 0xb0, 0xe9, 0x26,
	0x68, 0x6d, 0x71, 0xe8, 0x7f, 0x39, 0x4f, 0x79,
	0x9c, 0x00, 0xa5, 0x21, 0x03, 0xcb, 0x4e, 0x17,
} // == SHA256("BIP-340")

// SchnorrSign  —— 100 % 对齐 btcsuite schnorr.Sign 的内部算法
// 入参：secp256k1 私钥标量 x、32 byte 消息哈希 m
// 出参：R.x, R.y, z(==s), k
func SchnorrSign(curve Group, x *big.Int, m []byte) (Rx, Ry, z, k *big.Int) {
	//if curve != btcec.S256() {
	//	panic("SchnorrSign: only secp256k1 supported")
	//}
	if len(m) != 32 {
		panic("BIP-340: message must be a 32-byte hash")
	}
	n := curve.Order()

	// ---------- 1. 处理私钥/公钥 ----------
	d := new(big.Int).Set(x)
	Px, Py := curve.ScalarBaseMult(d).XY()
	if Py.Bit(0) == 1 { // 公钥 Y 奇数 → d = n-d
		d.Sub(n, d)
	}

	// ---------- 2. RFC6979 nonce ----------
	priv32 := make([]byte, 32)
	copy(priv32[32-len(d.Bytes()):], d.Bytes())

	var kScalar *btcec.ModNScalar
	for iter := uint32(0); ; iter++ {
		kScalar = btcec.NonceRFC6979(priv32, m, rfc6979ExtraDataV0[:], nil, iter)

		if !kScalar.IsZero() {
			break
		}
	}

	// 转成 *big.Int 方便后续运算
	var kArr [32]byte
	kScalar.PutBytes(&kArr)
	kInt := new(big.Int).SetBytes(kArr[:])

	// ---------- 3. 计算 R，确保 R.y 为偶数 ----------
	Rx, Ry = curve.ScalarBaseMultBytes(kArr[:]).XY()
	if Ry.Bit(0) == 1 {
		kInt.Sub(n, kInt)
		kBytes := kInt.FillBytes(make([]byte, 32))
		Rx, Ry = curve.ScalarBaseMultBytes(kBytes).XY()
	}

	// ---------- 4. challenge e = tagged_hash("BIP0340/challenge", Rx || Px || m) ----------
	eBytes := taggedHash("BIP0340/challenge",
		Rx.Bytes(),
		Px.Bytes(),
		m,
	)
	e := new(big.Int).SetBytes(eBytes)
	e.Mod(e, n)

	// ---------- 5. z = k + e·d (mod n) ----------
	z = new(big.Int).Mul(e, d)
	z.Add(z, kInt)
	z.Mod(z, n)

	return Rx, Ry, z, kInt
}

// taggedHash —— BIP-340 域分离哈希:  SHA256( SHA256(tag)‖SHA256(tag)‖msg… )
func taggedHash(tag string, data ...[]byte) []byte {
	tagSum := sha256.Sum256([]byte(tag))
	h := sha256.New()
	h.Write(tagSum[:])
	h.Write(tagSum[:])
	for _, b := range data {
		h.Write(b)
	}
	return h.Sum(nil)
}

// SchnorrVerify  – BIP-340 版本（保持原型不变）
func SchnorrVerify(curve Group, Xx, Xy, Rx, Ry, z *big.Int, m []byte) bool {
	n := curve.Order()
	if len(m) != 32 || Ry.Bit(0) == 1 { // Ry 必须是偶数
		return false
	}

	// e = H_tag("BIP0340/challenge", Rx || Xx || m) mod n
	// 若公钥的 Y 是奇数，翻到偶数（与签名侧保持一致）
	if Xy.Bit(0) == 1 {
		Xy = new(big.Int).Sub(curve.Modulus(), Xy)
	}
	eBytes := taggedHash("BIP0340/challenge",
		Rx.Bytes(),
		Xx.Bytes(),
		m,
	)
	e := new(big.Int).SetBytes(eBytes)
	e.Mod(e, n)

	// 检查:  z·G  ?=  R + e·X
	Gz_x, Gz_y := curve.ScalarBaseMultBytes(z.Bytes()).XY()
	X := Point{X: Xx, Y: Xy}
	eX := curve.ScalarMultBytes(X, e.Bytes())
	R := Point{X: Rx, Y: Ry}
	sum_x, sum_y := curve.Add(R, eX).XY()

	return Gz_x.Cmp(sum_x) == 0 && Gz_y.Cmp(sum_y) == 0
}

// 阈值部分签名
func ThresholdSchnorrPartialSign(curve Group, s_j *big.Int, Qx, Qy *big.Int, m []byte) (k, R_x, R_y *big.Int) {
	N := curve.Order()
	if devMode && forceK != nil {
		k = new(big.Int).Set(forceK)
	} else {
		k = randomScalar(N)
	}
	// ThresholdSchnorrPartialSign 内部
	R_x, R_y = curve.ScalarBaseMultBytes(k.Bytes()).XY()
	if R_y.Bit(0) == 1 {
		k = new(big.Int).Sub(N, k)
		R_x, R_y = curve.ScalarBaseMultBytes(k.Bytes()).XY()
	}

	return k, R_x, R_y
}

// Round‑2：用自己的 k_i、λ_i、e、s_i′ 计算 z_i
// 计算 z_i = k_i + λ_i·e·s_i′
func ThresholdSchnorrFinalize(curve Group,
	k_i, lambda_i, e, shareSi *big.Int) *big.Int {

	zi := new(big.Int).Mul(e, shareSi) // e·s_i′
	zi.Mul(zi, lambda_i)               // λ_i·e·s_i′
	zi.Add(zi, k_i)                    // k_i + ...
	zi.Mod(zi, curve.Order())
	return zi
}

// -----------------------------------------------------------------------------
// —— 聚合 (ids, R_i, z_i) → (R, z)
// -----------------------------------------------------------------------------
func AggregateThresholdSignature(
	curve Group,
	ids, RxParts, RyParts, zParts []*big.Int,
	Qx *big.Int, msg []byte,
) (R_x, R_y, zAgg *big.Int) {

	N := curve.Order()
	t := len(ids)
	if len(RxParts) != t || len(RyParts) != t || len(zParts) != t {
		panic("AggregateThresholdSignature: slice length mismatch")
	}

	// 1) R = Σ R_i
	R_x, R_y = RxParts[0], RyParts[0]
	for i := 1; i < t; i++ {
		R := Point{X: R_x, Y: R_y}
		RParts := Point{X: RxParts[i], Y: RyParts[i]}
		R_x, R_y = curve.Add(R, RParts).XY()
	}

	// 2) z = Σ z_i
	zAgg = big.NewInt(0)
	for _, zi := range zParts {
		zAgg.Add(zAgg, zi)
	}
	zAgg.Mod(zAgg, N)

	// 此时保证输入的 R 已经是偶‑Y
	return R_x, R_y, zAgg
}

// —— 计算 BIP-340 challenge e = tagged_hash("BIP0340/challenge", Rx.x || Px || m) ——
func bip340Challenge(Rx, Px *big.Int, msg []byte, curve Group) *big.Int {
	tagHash := sha256.Sum256([]byte("BIP0340/challenge"))

	h := sha256.New()
	h.Write(tagHash[:])
	h.Write(tagHash[:])

	// 保证定长 32 B
	pad32 := func(b *big.Int) []byte {
		out := make([]byte, 32)
		z := b.Bytes()
		copy(out[32-len(z):], z)
		return out
	}
	h.Write(pad32(Rx))
	h.Write(pad32(Px))
	h.Write(msg) // 这里已经是 32 B 摘要

	e := new(big.Int).SetBytes(h.Sum(nil))
	e.Mod(e, curve.Order())
	return e
}

// returns a 64‑byte BIP‑340 signature:
//
//	sig = R.x(32) || z(32)
//
// caller supplies:
//   - idsSel   – t 个参与者 ID（都是 *big.Int）
//   - sjSel    – 这 t 个参与者手上的私钥份额 s_j′（偶‑Y 体系下的数值）
//   - msg32    – 32‑byte Taproot signature hash (see CalcTaprootSignatureHash)
//   - Qx       – 聚合公钥 x 坐标（Taproot key‑path 地址里隐含的公钥）
//
// ThresholdSign 泛化版阈值签名，支持任意 ChallengeFunc
func ThresholdSign(
	curve Group,
	idsSel, sjSel []*big.Int,
	msg32 []byte,
	Qx *big.Int,
	challenge ChallengeFunc,
) ([]byte, error) {
	const tMin = 3
	if len(idsSel) != len(sjSel) || len(idsSel) < tMin {
		return nil, errors.New("ids / shares length mismatch or too few signers")
	}
	N := curve.Order()
	t := len(idsSel)

	// —— Round-1，各参与者产生 (k_i, R_i) ——
	k := make([]*big.Int, t)
	Rx := make([]*big.Int, t)
	Ry := make([]*big.Int, t)
	for i := 0; i < t; i++ {
		ki, Rxi, Ryi := ThresholdSchnorrPartialSign(curve, sjSel[i], nil, nil, msg32)
		k[i], Rx[i], Ry[i] = ki, Rxi, Ryi
	}
	// —— 可选的 R.y 翻转 ——
	RxSum, RySum := Rx[0], Ry[0]
	for i := 1; i < t; i++ {
		sum := Point{X: RxSum, Y: RySum}
		part := Point{X: Rx[i], Y: Ry[i]}
		RxSum, RySum = curve.Add(sum, part).XY()
	}
	if RySum.Bit(0) == 1 {
		for i := range k {
			k[i].Sub(N, k[i])
			Ry[i].Sub(curve.Modulus(), Ry[i])
		}
		RySum.Sub(curve.Modulus(), RySum)
	}

	// —— 核心：使用注入的 challenge 函数 ——
	e := challenge(RxSum, Qx, msg32, curve)

	// —— 计算每个 z_i 并累加 ——
	zAgg := big.NewInt(0)
	λ := ComputeLagrangeCoefficients(idsSel, N)
	for i := 0; i < t; i++ {
		zi := ThresholdSchnorrFinalize(curve, k[i], λ[i], e, sjSel[i])
		zAgg.Add(zAgg, zi)
	}
	zAgg.Mod(zAgg, N)

	// —— 序列化 R.x || zAgg ——
	sig := make([]byte, 64)
	RxSum.FillBytes(sig[:32])
	zAgg.FillBytes(sig[32:])
	return sig, nil
}

// ChallengeFunc 定义了任意阈值签名协议中，用于计算 challenge e = f(Rx, P, msg) 的函数签名
type ChallengeFunc func(Rx, Px *big.Int, msg []byte, curve Group) *big.Int

// BIP340Challenge 基于 BIP-340 的 tagged hash 实现 ChallengeFunc
func BIP340Challenge(Rx, Px *big.Int, msg []byte, curve Group) *big.Int {
	// 1) 计算 tagHash = SHA256("BIP0340/challenge")
	tagHash := sha256.Sum256([]byte("BIP0340/challenge"))

	// 2) 域分离哈希：SHA256(tagHash || tagHash || Rx || Px || msg)
	h := sha256.New()
	h.Write(tagHash[:])
	h.Write(tagHash[:])

	// pad32 确保 big.Int 始终以 32 字节大端表示
	pad32 := func(b *big.Int) []byte {
		out := make([]byte, 32)
		bb := b.Bytes()
		copy(out[32-len(bb):], bb)
		return out
	}
	h.Write(pad32(Rx))
	h.Write(pad32(Px))
	h.Write(msg) // 这里 msg 应当已经是 32 字节摘要

	// 3) 将哈希结果 mod n，得到 challenge e
	e := new(big.Int).SetBytes(h.Sum(nil))
	e.Mod(e, curve.Order())
	return e
}

// ETHKeccakChallenge 基于 eth_schnorr.go 中的 e = keccak256(Rx||Ry||Px||Py||msg) mod r
// 由于 ThresholdSign 的 ChallengeFunc 只接收 Rx 和 Px，如果需要 Ry/Py
// 也可以扩展签名流程传入 Y 坐标；这里先按最简 Rx||Px||msg 实现。
func ETHKeccakChallenge(Rx, Px *big.Int, msg []byte, curve Group) *big.Int {
	h := sha3.NewLegacyKeccak256()

	// pad32 同 eth_schnorr.go 中的 uintPad / putBigBuf 保持一致
	pad32 := func(n *big.Int) []byte {
		out := make([]byte, 32)
		b := n.Bytes()
		copy(out[32-len(b):], b)
		return out
	}

	h.Write(pad32(Rx)) // R.x
	h.Write(pad32(Px)) // P.x
	h.Write(msg)       // message（需要是 keccak256 之前的原始 bytes）

	eBytes := h.Sum(nil)
	e := new(big.Int).SetBytes(eBytes)
	e.Mod(e, curve.Order())
	return e
}
