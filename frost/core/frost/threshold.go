package frost

import (
	"errors"
	"math/big"

	"dex/frost/core/curve"
	"dex/frost/core/dkg"
)

// 开发模式：允许强制使用同一个k进行阈值签名，以验证一致性
var devMode = false
var forceK *big.Int

// ThresholdSchnorrPartialSign 阈值部分签名
func ThresholdSchnorrPartialSign(grp curve.Group, s_j *big.Int, Qx, Qy *big.Int, m []byte) (k, R_x, R_y *big.Int) {
	N := grp.Order()
	if devMode && forceK != nil {
		k = new(big.Int).Set(forceK)
	} else {
		k = dkg.RandomScalar(N)
	}
	R_x, R_y = grp.ScalarBaseMultBytes(k.Bytes()).XY()
	if R_y.Bit(0) == 1 {
		k = new(big.Int).Sub(N, k)
		R_x, R_y = grp.ScalarBaseMultBytes(k.Bytes()).XY()
	}

	return k, R_x, R_y
}

// ThresholdSchnorrFinalize Round‑2：用自己的 k_i、λ_i、e、s_i′ 计算 z_i
// 计算 z_i = k_i + λ_i·e·s_i′
func ThresholdSchnorrFinalize(grp curve.Group,
	k_i, lambda_i, e, shareSi *big.Int) *big.Int {

	zi := new(big.Int).Mul(e, shareSi) // e·s_i′
	zi.Mul(zi, lambda_i)               // λ_i·e·s_i′
	zi.Add(zi, k_i)                    // k_i + ...
	zi.Mod(zi, grp.Order())
	return zi
}

// AggregateThresholdSignature 聚合 (ids, R_i, z_i) → (R, z)
func AggregateThresholdSignature(
	grp curve.Group,
	ids, RxParts, RyParts, zParts []*big.Int,
	Qx *big.Int, msg []byte,
) (R_x, R_y, zAgg *big.Int) {

	N := grp.Order()
	t := len(ids)
	if len(RxParts) != t || len(RyParts) != t || len(zParts) != t {
		panic("AggregateThresholdSignature: slice length mismatch")
	}

	// 1) R = Σ R_i
	R_x, R_y = RxParts[0], RyParts[0]
	for i := 1; i < t; i++ {
		R := curve.Point{X: R_x, Y: R_y}
		RParts := curve.Point{X: RxParts[i], Y: RyParts[i]}
		R_x, R_y = grp.Add(R, RParts).XY()
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

// ChallengeFunc 定义了任意阈值签名协议中，用于计算 challenge e = f(Rx, P, msg) 的函数签名
type ChallengeFunc func(Rx, Px *big.Int, msg []byte, grp curve.Group) *big.Int

// ThresholdSign 泛化版阈值签名，支持任意 ChallengeFunc
// returns a 64‑byte BIP‑340 signature: sig = R.x(32) || z(32)
func ThresholdSign(
	grp curve.Group,
	idsSel, sjSel []*big.Int,
	msg32 []byte,
	Qx *big.Int,
	challenge ChallengeFunc,
) ([]byte, error) {
	const tMin = 3
	if len(idsSel) != len(sjSel) || len(idsSel) < tMin {
		return nil, errors.New("ids / shares length mismatch or too few signers")
	}
	N := grp.Order()
	t := len(idsSel)

	// —— Round-1，各参与者产生 (k_i, R_i) ——
	k := make([]*big.Int, t)
	Rx := make([]*big.Int, t)
	Ry := make([]*big.Int, t)
	for i := 0; i < t; i++ {
		ki, Rxi, Ryi := ThresholdSchnorrPartialSign(grp, sjSel[i], nil, nil, msg32)
		k[i], Rx[i], Ry[i] = ki, Rxi, Ryi
	}
	// —— 可选的 R.y 翻转 ——
	RxSum, RySum := Rx[0], Ry[0]
	for i := 1; i < t; i++ {
		sum := curve.Point{X: RxSum, Y: RySum}
		part := curve.Point{X: Rx[i], Y: Ry[i]}
		RxSum, RySum = grp.Add(sum, part).XY()
	}
	if RySum.Bit(0) == 1 {
		for i := range k {
			k[i].Sub(N, k[i])
			Ry[i].Sub(grp.Modulus(), Ry[i])
		}
		RySum.Sub(grp.Modulus(), RySum)
	}

	// —— 核心：使用注入的 challenge 函数 ——
	e := challenge(RxSum, Qx, msg32, grp)

	// —— 计算每个 z_i 并累加 ——
	zAgg := big.NewInt(0)
	λ := dkg.ComputeLagrangeCoefficients(idsSel, N)
	for i := 0; i < t; i++ {
		zi := ThresholdSchnorrFinalize(grp, k[i], λ[i], e, sjSel[i])
		zAgg.Add(zAgg, zi)
	}
	zAgg.Mod(zAgg, N)

	// —— 序列化 R.x || zAgg ——
	sig := make([]byte, 64)
	RxSum.FillBytes(sig[:32])
	zAgg.FillBytes(sig[32:])
	return sig, nil
}
