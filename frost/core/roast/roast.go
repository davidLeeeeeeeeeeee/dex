// frost/core/roast/roast.go
// ROAST: Robust Asynchronous Schnorr Threshold Signatures
// 实现 ROAST 核心算法：子集选择、重试策略、聚合者切换

package roast

import (
	"crypto/sha256"
	"errors"
	"math/big"
	"sort"

	"dex/frost/core/curve"
	"dex/frost/core/dkg"
)

// ========== 错误定义 ==========

var (
	// ErrInsufficientSigners 签名者不足
	ErrInsufficientSigners = errors.New("insufficient signers")
	// ErrInvalidShare 无效的签名份额
	ErrInvalidShare = errors.New("invalid signature share")
	// ErrAggregationFailed 聚合失败
	ErrAggregationFailed = errors.New("signature aggregation failed")
)

// ========== 类型定义 ==========

// SignerNonce 签名者的 nonce 数据
type SignerNonce struct {
	SignerID     int         // 签名者 ID
	HidingNonce  *big.Int    // k_i (hiding nonce 标量)
	BindingNonce *big.Int    // k'_i (binding nonce 标量)
	HidingPoint  curve.Point // R_i = k_i * G
	BindingPoint curve.Point // R'_i = k'_i * G
}

// SignerShare 签名者的签名份额
type SignerShare struct {
	SignerID int      // 签名者 ID
	Share    *big.Int // z_i = k_i + ρ_i * λ_i * e * s_i
}

// ROASTParams ROAST 参数
type ROASTParams struct {
	Threshold  int         // 门限 t
	NumSigners int         // 总签名者数 n
	Message    []byte      // 待签名消息（32 字节哈希）
	GroupPubX  *big.Int    // 群公钥 X 坐标
	GroupPubY  *big.Int    // 群公钥 Y 坐标
	Group      curve.Group // 椭圆曲线群
}

// ========== 核心函数 ==========

// ComputeBindingCoefficient 计算绑定系数 ρ_i
// ρ_i = H("rho" || i || msg || R_1 || R'_1 || ... || R_n || R'_n)
func ComputeBindingCoefficient(
	signerID int,
	msg []byte,
	nonces []SignerNonce,
	group curve.Group,
) *big.Int {
	// 按 ID 排序 nonces
	sortedNonces := make([]SignerNonce, len(nonces))
	copy(sortedNonces, nonces)
	sort.Slice(sortedNonces, func(i, j int) bool {
		return sortedNonces[i].SignerID < sortedNonces[j].SignerID
	})

	// 构造哈希输入
	h := sha256.New()
	h.Write([]byte("rho"))
	h.Write(big.NewInt(int64(signerID)).Bytes())
	h.Write(msg)

	for _, n := range sortedNonces {
		h.Write(n.HidingPoint.X.Bytes())
		h.Write(n.HidingPoint.Y.Bytes())
		h.Write(n.BindingPoint.X.Bytes())
		h.Write(n.BindingPoint.Y.Bytes())
	}

	hash := h.Sum(nil)
	rho := new(big.Int).SetBytes(hash)
	return rho.Mod(rho, group.Order())
}

// ComputeGroupCommitment 计算群承诺 R = Σ(R_i + ρ_i * R'_i)
func ComputeGroupCommitment(
	nonces []SignerNonce,
	msg []byte,
	group curve.Group,
) (curve.Point, error) {
	if len(nonces) == 0 {
		return curve.Point{}, ErrInsufficientSigners
	}

	// 初始化为零点
	resultX := big.NewInt(0)
	resultY := big.NewInt(0)
	first := true

	for _, n := range nonces {
		// 计算 ρ_i
		rho := ComputeBindingCoefficient(n.SignerID, msg, nonces, group)

		// 计算 ρ_i * R'_i
		rhoRPrime := group.ScalarMultBytes(n.BindingPoint, rho.Bytes())

		// 计算 R_i + ρ_i * R'_i
		combined := group.Add(n.HidingPoint, rhoRPrime)

		// 累加
		if first {
			resultX = combined.X
			resultY = combined.Y
			first = false
		} else {
			result := group.Add(curve.Point{X: resultX, Y: resultY}, combined)
			resultX = result.X
			resultY = result.Y
		}
	}

	return curve.Point{X: resultX, Y: resultY}, nil
}

// ComputeChallenge 计算挑战值 e = H("BIP0340/challenge" || R.x || P.x || msg)
func ComputeChallenge(R curve.Point, Px *big.Int, msg []byte, group curve.Group) *big.Int {
	// BIP-340 tagged hash
	tagSum := sha256.Sum256([]byte("BIP0340/challenge"))
	h := sha256.New()
	h.Write(tagSum[:])
	h.Write(tagSum[:])

	// R.x (32 bytes)
	rxBytes := make([]byte, 32)
	R.X.FillBytes(rxBytes)
	h.Write(rxBytes)

	// P.x (32 bytes)
	pxBytes := make([]byte, 32)
	Px.FillBytes(pxBytes)
	h.Write(pxBytes)

	// msg (32 bytes)
	h.Write(msg)

	hash := h.Sum(nil)
	e := new(big.Int).SetBytes(hash)
	return e.Mod(e, group.Order())
}

// ComputePartialSignature 计算部分签名 z_i = k_i + ρ_i * k'_i + λ_i * e * s_i
func ComputePartialSignature(
	signerID int,
	hidingNonce, bindingNonce *big.Int,
	share *big.Int, // 签名者的密钥份额
	rho *big.Int, // 绑定系数
	lambda *big.Int, // 拉格朗日系数
	e *big.Int, // 挑战值
	group curve.Group,
) *big.Int {
	n := group.Order()

	// k_i + ρ_i * k'_i
	z := new(big.Int).Mul(rho, bindingNonce)
	z.Add(z, hidingNonce)
	z.Mod(z, n)

	// λ_i * e * s_i
	term := new(big.Int).Mul(lambda, e)
	term.Mul(term, share)
	term.Mod(term, n)

	// z_i = (k_i + ρ_i * k'_i) + (λ_i * e * s_i)
	z.Add(z, term)
	z.Mod(z, n)

	return z
}

// AggregateSignatures 聚合签名份额
// 返回最终签名 (R.x || z)
func AggregateSignatures(
	R curve.Point,
	shares []SignerShare,
	group curve.Group,
) ([]byte, error) {
	if len(shares) == 0 {
		return nil, ErrInsufficientSigners
	}

	// z = Σ z_i
	z := big.NewInt(0)
	for _, s := range shares {
		z.Add(z, s.Share)
	}
	z.Mod(z, group.Order())

	// 确保 R.y 为偶数（BIP-340 要求）
	if R.Y.Bit(0) == 1 {
		// 如果 R.y 是奇数，需要翻转 z
		z.Sub(group.Order(), z)
	}

	// 序列化签名: R.x (32 bytes) || z (32 bytes)
	sig := make([]byte, 64)
	R.X.FillBytes(sig[:32])
	z.FillBytes(sig[32:])

	return sig, nil
}

// ========== ROAST 协调逻辑 ==========

// SelectSubset 选择签名者子集
// 使用确定性算法选择 t+1 个签名者
func SelectSubset(signerIDs []int, threshold int, seed []byte, retryCount int) []int {
	if len(signerIDs) <= threshold {
		return signerIDs
	}

	// 确定性洗牌
	permuted := make([]int, len(signerIDs))
	copy(permuted, signerIDs)

	// 使用种子和重试次数生成确定性随机序列
	for i := len(permuted) - 1; i > 0; i-- {
		seedWithRetry := append(seed, byte(retryCount), byte(i))
		hash := sha256.Sum256(seedWithRetry)
		j := int(new(big.Int).SetBytes(hash[:]).Uint64()) % (i + 1)
		permuted[i], permuted[j] = permuted[j], permuted[i]
	}

	// 返回前 t+1 个
	return permuted[:threshold+1]
}

// ComputeAggregatorIndex 计算当前聚合者索引
// 确定性算法：基于种子和区块高度
func ComputeAggregatorIndex(
	signerIDs []int,
	seed []byte,
	startHeight, currentHeight, rotateBlocks uint64,
) int {
	if len(signerIDs) == 0 {
		return 0
	}

	// 计算轮换次数
	rotations := (currentHeight - startHeight) / rotateBlocks

	// 确定性排列
	permuted := make([]int, len(signerIDs))
	copy(permuted, signerIDs)

	for i := len(permuted) - 1; i > 0; i-- {
		seedWithIdx := append(seed, byte(i))
		hash := sha256.Sum256(seedWithIdx)
		j := int(new(big.Int).SetBytes(hash[:]).Uint64()) % (i + 1)
		permuted[i], permuted[j] = permuted[j], permuted[i]
	}

	// 选择聚合者
	return permuted[int(rotations)%len(permuted)]
}

// VerifyPartialSignature 验证部分签名
// z_i * G ?= R_i + ρ_i * R'_i + λ_i * e * P_i
func VerifyPartialSignature(
	signerID int,
	share *big.Int,
	nonce SignerNonce,
	rho, lambda, e *big.Int,
	publicKeyShare curve.Point, // 签名者的公钥份额 P_i
	group curve.Group,
) bool {
	// z_i * G
	zG := group.ScalarBaseMultBytes(share.Bytes())

	// R_i + ρ_i * R'_i
	rhoRPrime := group.ScalarMultBytes(nonce.BindingPoint, rho.Bytes())
	RTotal := group.Add(nonce.HidingPoint, rhoRPrime)

	// λ_i * e * P_i
	lambdaE := new(big.Int).Mul(lambda, e)
	lambdaE.Mod(lambdaE, group.Order())
	lambdaEP := group.ScalarMultBytes(publicKeyShare, lambdaE.Bytes())

	// R_i + ρ_i * R'_i + λ_i * e * P_i
	expected := group.Add(RTotal, lambdaEP)

	// 比较
	return zG.X.Cmp(expected.X) == 0 && zG.Y.Cmp(expected.Y) == 0
}

// ComputeLagrangeCoefficientsForSet 为选中的签名者子集计算拉格朗日系数
func ComputeLagrangeCoefficientsForSet(signerIDs []int, order *big.Int) map[int]*big.Int {
	ids := make([]*big.Int, len(signerIDs))
	for i, id := range signerIDs {
		ids[i] = big.NewInt(int64(id))
	}

	lambdas := dkg.ComputeLagrangeCoefficients(ids, order)

	result := make(map[int]*big.Int)
	for i, id := range signerIDs {
		result[id] = lambdas[i]
	}
	return result
}
