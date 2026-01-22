// frost/runtime/adapters/crypto.go
// 实现 ROASTExecutor 和 DKGExecutor 接口，适配到 core 包

package adapters

import (
	"crypto"
	"crypto/sha256"
	_ "crypto/sha512" // 必须包含 sha512
	"errors"
	"math/big"

	"dex/frost/core/curve"
	"dex/frost/core/dkg"
	"dex/frost/core/roast"
	"dex/frost/runtime"
	"dex/pb"

	"golang.org/x/crypto/sha3"
)

// ========== 错误定义 ==========

var (
	ErrUnsupportedSignAlgo = errors.New("unsupported sign algorithm")
)

// ========== ROAST Executor 实现 ==========

// Secp256k1ROASTExecutor secp256k1 曲线的 ROAST 执行器
type Secp256k1ROASTExecutor struct {
	group curve.Group
}

// NewSecp256k1ROASTExecutor 创建 secp256k1 ROAST 执行器
func NewSecp256k1ROASTExecutor() *Secp256k1ROASTExecutor {
	return &Secp256k1ROASTExecutor{
		group: curve.NewSecp256k1Group(),
	}
}

// toRoastNonces 转换 NonceInput 到 roast.SignerNonce
func (e *Secp256k1ROASTExecutor) toRoastNonces(nonces []runtime.NonceInput) []roast.SignerNonce {
	result := make([]roast.SignerNonce, len(nonces))
	for i, n := range nonces {
		result[i] = roast.SignerNonce{
			SignerID:     n.SignerID,
			HidingNonce:  n.HidingNonce,
			BindingNonce: n.BindingNonce,
			HidingPoint:  curve.Point{X: n.HidingPoint.X, Y: n.HidingPoint.Y},
			BindingPoint: curve.Point{X: n.BindingPoint.X, Y: n.BindingPoint.Y},
		}
	}
	return result
}

// ComputeGroupCommitment 计算群承诺
func (e *Secp256k1ROASTExecutor) ComputeGroupCommitment(nonces []runtime.NonceInput, msg []byte) (runtime.CurvePoint, error) {
	roastNonces := e.toRoastNonces(nonces)
	R, err := roast.ComputeGroupCommitment(roastNonces, msg, e.group)
	if err != nil {
		return runtime.CurvePoint{}, err
	}
	return runtime.CurvePoint{X: R.X, Y: R.Y}, nil
}

// ComputeBindingCoefficient 计算绑定系数
func (e *Secp256k1ROASTExecutor) ComputeBindingCoefficient(signerID int, msg []byte, nonces []runtime.NonceInput) *big.Int {
	roastNonces := e.toRoastNonces(nonces)
	return roast.ComputeBindingCoefficient(signerID, msg, roastNonces, e.group)
}

// ComputeChallenge 计算挑战值
func (e *Secp256k1ROASTExecutor) ComputeChallenge(R runtime.CurvePoint, groupPubX *big.Int, msg []byte) *big.Int {
	return roast.ComputeChallenge(curve.Point{X: R.X, Y: R.Y}, groupPubX, msg, e.group)
}

// ComputeLagrangeCoefficients 计算拉格朗日系数
func (e *Secp256k1ROASTExecutor) ComputeLagrangeCoefficients(signerIDs []int) map[int]*big.Int {
	return roast.ComputeLagrangeCoefficientsForSet(signerIDs, e.group.Order())
}

// ComputePartialSignature 计算部分签名
func (e *Secp256k1ROASTExecutor) ComputePartialSignature(params runtime.PartialSignParams) *big.Int {
	return roast.ComputePartialSignature(
		params.SignerID,
		params.HidingNonce,
		params.BindingNonce,
		params.SecretShare,
		params.Rho,
		params.Lambda,
		params.Challenge,
		e.group,
	)
}

// AggregateSignatures 聚合签名份额，返回最终签名
func (e *Secp256k1ROASTExecutor) AggregateSignatures(R runtime.CurvePoint, shares []runtime.ShareInput) ([]byte, error) {
	roastShares := make([]roast.SignerShare, len(shares))
	for i, s := range shares {
		roastShares[i] = roast.SignerShare{
			SignerID: s.SignerID,
			Share:    s.Share,
		}
	}
	return roast.AggregateSignatures(curve.Point{X: R.X, Y: R.Y}, roastShares, e.group)
}

// GenerateNoncePair 生成 nonce 对
func (e *Secp256k1ROASTExecutor) GenerateNoncePair() (hiding, binding *big.Int, hidingPoint, bindingPoint runtime.CurvePoint, err error) {
	hiding = dkg.RandomScalar(e.group.Order())
	binding = dkg.RandomScalar(e.group.Order())

	hp := e.group.ScalarBaseMult(hiding)
	bp := e.group.ScalarBaseMult(binding)

	hidingPoint = runtime.CurvePoint{X: hp.X, Y: hp.Y}
	bindingPoint = runtime.CurvePoint{X: bp.X, Y: bp.Y}
	return
}

// ScalarBaseMult 基点乘法
func (e *Secp256k1ROASTExecutor) ScalarBaseMult(k *big.Int) runtime.CurvePoint {
	p := e.group.ScalarBaseMult(k)
	return runtime.CurvePoint{X: p.X, Y: p.Y}
}

// SerializePoint 序列化点
func (e *Secp256k1ROASTExecutor) SerializePoint(P runtime.CurvePoint) []byte {
	return e.group.SerializePoint(curve.Point{X: P.X, Y: P.Y})
}

// BN256ROASTExecutor BN256 曲线的 ROAST 执行器
type BN256ROASTExecutor struct {
	group curve.Group
}

func NewBN256ROASTExecutor() *BN256ROASTExecutor {
	return &BN256ROASTExecutor{
		group: curve.NewBN256Group(),
	}
}

func (e *BN256ROASTExecutor) toRoastNonces(nonces []runtime.NonceInput) []roast.SignerNonce {
	result := make([]roast.SignerNonce, len(nonces))
	for i, n := range nonces {
		result[i] = roast.SignerNonce{
			SignerID:     n.SignerID,
			HidingNonce:  n.HidingNonce,
			BindingNonce: n.BindingNonce,
			HidingPoint:  curve.Point{X: n.HidingPoint.X, Y: n.HidingPoint.Y},
			BindingPoint: curve.Point{X: n.BindingPoint.X, Y: n.BindingPoint.Y},
		}
	}
	return result
}

func (e *BN256ROASTExecutor) ComputeGroupCommitment(nonces []runtime.NonceInput, msg []byte) (runtime.CurvePoint, error) {
	roastNonces := e.toRoastNonces(nonces)
	R, err := roast.ComputeGroupCommitment(roastNonces, msg, e.group)
	if err != nil {
		return runtime.CurvePoint{}, err
	}
	return runtime.CurvePoint{X: R.X, Y: R.Y}, nil
}

func (e *BN256ROASTExecutor) ComputeBindingCoefficient(signerID int, msg []byte, nonces []runtime.NonceInput) *big.Int {
	roastNonces := e.toRoastNonces(nonces)
	return roast.ComputeBindingCoefficient(signerID, msg, roastNonces, e.group)
}

func (e *BN256ROASTExecutor) ComputeChallenge(R runtime.CurvePoint, groupPubX *big.Int, msg []byte) *big.Int {
	// e = Keccak256(R.x || P.x || msg) mod n
	h := sha3.NewLegacyKeccak256()
	pad32 := func(n *big.Int) []byte {
		out := make([]byte, 32)
		b := n.Bytes()
		copy(out[32-len(b):], b)
		return out
	}
	h.Write(pad32(R.X))
	h.Write(pad32(groupPubX))
	h.Write(msg)
	eBytes := h.Sum(nil)
	res := new(big.Int).SetBytes(eBytes)
	return res.Mod(res, e.group.Order())
}

func (e *BN256ROASTExecutor) ComputeLagrangeCoefficients(signerIDs []int) map[int]*big.Int {
	return roast.ComputeLagrangeCoefficientsForSet(signerIDs, e.group.Order())
}

func (e *BN256ROASTExecutor) ComputePartialSignature(params runtime.PartialSignParams) *big.Int {
	return roast.ComputePartialSignature(
		params.SignerID,
		params.HidingNonce,
		params.BindingNonce,
		params.SecretShare,
		params.Rho,
		params.Lambda,
		params.Challenge,
		e.group,
	)
}

func (e *BN256ROASTExecutor) AggregateSignatures(R runtime.CurvePoint, shares []runtime.ShareInput) ([]byte, error) {
	if len(shares) == 0 {
		return nil, errors.New("insufficient shares")
	}
	// z = Σ z_i mod n
	z := big.NewInt(0)
	for _, s := range shares {
		z.Add(z, s.Share)
	}
	z.Mod(z, e.group.Order())

	// Signature = R.x || z (64 bytes)
	sig := make([]byte, 64)
	RxBytes := R.X.Bytes()
	copy(sig[32-len(RxBytes):32], RxBytes)
	zBytes := z.Bytes()
	copy(sig[64-len(zBytes):], zBytes)
	return sig, nil
}

func (e *BN256ROASTExecutor) GenerateNoncePair() (hiding, binding *big.Int, hidingPoint, bindingPoint runtime.CurvePoint, err error) {
	hiding = dkg.RandomScalar(e.group.Order())
	binding = dkg.RandomScalar(e.group.Order())
	hp := e.group.ScalarBaseMult(hiding)
	bp := e.group.ScalarBaseMult(binding)
	hidingPoint = runtime.CurvePoint{X: hp.X, Y: hp.Y}
	bindingPoint = runtime.CurvePoint{X: bp.X, Y: bp.Y}
	return
}

func (e *BN256ROASTExecutor) ScalarBaseMult(k *big.Int) runtime.CurvePoint {
	p := e.group.ScalarBaseMult(k)
	return runtime.CurvePoint{X: p.X, Y: p.Y}
}

func (e *BN256ROASTExecutor) SerializePoint(P runtime.CurvePoint) []byte {
	return e.group.SerializePoint(curve.Point{X: P.X, Y: P.Y})
}

// Ed25519ROASTExecutor Ed25519 曲线的 ROAST 执行器
type Ed25519ROASTExecutor struct {
	group curve.Group
}

func NewEd25519ROASTExecutor() *Ed25519ROASTExecutor {
	return &Ed25519ROASTExecutor{
		group: curve.NewEd25519Group(),
	}
}

func (e *Ed25519ROASTExecutor) toRoastNonces(nonces []runtime.NonceInput) []roast.SignerNonce {
	result := make([]roast.SignerNonce, len(nonces))
	for i, n := range nonces {
		result[i] = roast.SignerNonce{
			SignerID:     n.SignerID,
			HidingNonce:  n.HidingNonce,
			BindingNonce: n.BindingNonce,
			HidingPoint:  curve.Point{X: n.HidingPoint.X, Y: n.HidingPoint.Y},
			BindingPoint: curve.Point{X: n.BindingPoint.X, Y: n.BindingPoint.Y},
		}
	}
	return result
}

func (e *Ed25519ROASTExecutor) ComputeGroupCommitment(nonces []runtime.NonceInput, msg []byte) (runtime.CurvePoint, error) {
	roastNonces := e.toRoastNonces(nonces)
	R, err := roast.ComputeGroupCommitment(roastNonces, msg, e.group)
	if err != nil {
		return runtime.CurvePoint{}, err
	}
	return runtime.CurvePoint{X: R.X, Y: R.Y}, nil
}

func (e *Ed25519ROASTExecutor) ComputeBindingCoefficient(signerID int, msg []byte, nonces []runtime.NonceInput) *big.Int {
	roastNonces := e.toRoastNonces(nonces)
	return roast.ComputeBindingCoefficient(signerID, msg, roastNonces, e.group)
}

func (e *Ed25519ROASTExecutor) ComputeChallenge(R runtime.CurvePoint, groupPubX *big.Int, msg []byte) *big.Int {
	// Standard Ed25519 challenge: H(R || A || M)
	// Here we use SHA512
	h := crypto.SHA512.New()

	// R (32 bytes compressed)
	h.Write(e.group.SerializePoint(curve.Point{X: R.X, Y: R.Y}))

	// A (32 bytes compressed)
	// groupPubX here contains the 32-byte compressed public key (stored in X)
	pubBytes := make([]byte, 32)
	groupPubX.FillBytes(pubBytes)
	h.Write(pubBytes)

	h.Write(msg)

	digest := h.Sum(nil)
	// Ed25519 uses the standard reduction mod L
	res := new(big.Int).SetBytes(reverse(digest)) // SHA512 output is treated as little-endian?
	// Actually Ed25519 challenge is typically computed as a 64-byte hash and reduced mod L
	// The reduction should handle the large value.
	return res.Mod(res, e.group.Order())
}

func reverse(s []byte) []byte {
	res := make([]byte, len(s))
	for i, j := 0, len(s)-1; i < len(s); i, j = i+1, j-1 {
		res[i] = s[j]
	}
	return res
}

func (e *Ed25519ROASTExecutor) ComputeLagrangeCoefficients(signerIDs []int) map[int]*big.Int {
	return roast.ComputeLagrangeCoefficientsForSet(signerIDs, e.group.Order())
}

func (e *Ed25519ROASTExecutor) ComputePartialSignature(params runtime.PartialSignParams) *big.Int {
	return roast.ComputePartialSignature(
		params.SignerID,
		params.HidingNonce,
		params.BindingNonce,
		params.SecretShare,
		params.Rho,
		params.Lambda,
		params.Challenge,
		e.group,
	)
}

func (e *Ed25519ROASTExecutor) AggregateSignatures(R runtime.CurvePoint, shares []runtime.ShareInput) ([]byte, error) {
	if len(shares) == 0 {
		return nil, errors.New("insufficient shares")
	}
	// s = Σ s_i mod L
	s := big.NewInt(0)
	for _, share := range shares {
		s.Add(s, share.Share)
	}
	s.Mod(s, e.group.Order())

	// Signature = R || s (64 bytes)
	// R is already compressed 32 bytes (stored in R.X)
	// s must be little-endian 32 bytes
	sig := make([]byte, 64)

	rBytes := e.group.SerializePoint(curve.Point{X: R.X, Y: R.Y})
	copy(sig[:32], rBytes)

	sBytes := make([]byte, 32)
	s.FillBytes(sBytes)
	copy(sig[32:], reverse(sBytes)) // serialize as little-endian

	return sig, nil
}

func (e *Ed25519ROASTExecutor) GenerateNoncePair() (hiding, binding *big.Int, hidingPoint, bindingPoint runtime.CurvePoint, err error) {
	hiding = dkg.RandomScalar(e.group.Order())
	binding = dkg.RandomScalar(e.group.Order())
	hp := e.group.ScalarBaseMult(hiding)
	bp := e.group.ScalarBaseMult(binding)
	hidingPoint = runtime.CurvePoint{X: hp.X, Y: hp.Y}
	bindingPoint = runtime.CurvePoint{X: bp.X, Y: bp.Y}
	return
}

func (e *Ed25519ROASTExecutor) ScalarBaseMult(k *big.Int) runtime.CurvePoint {
	p := e.group.ScalarBaseMult(k)
	return runtime.CurvePoint{X: p.X, Y: p.Y}
}

func (e *Ed25519ROASTExecutor) SerializePoint(P runtime.CurvePoint) []byte {
	return e.group.SerializePoint(curve.Point{X: P.X, Y: P.Y})
}

// ========== DKG Executor 实现 ==========

// Secp256k1DKGExecutor secp256k1 曲线的 DKG 执行器
type Secp256k1DKGExecutor struct {
	group curve.Group
}

// NewSecp256k1DKGExecutor 创建 secp256k1 DKG 执行器
func NewSecp256k1DKGExecutor() *Secp256k1DKGExecutor {
	return &Secp256k1DKGExecutor{
		group: curve.NewSecp256k1Group(),
	}
}

// polynomialAdapter 实现 PolynomialHandle 接口
type polynomialAdapter struct {
	poly  *dkg.Polynomial
	group curve.Group
}

func (p *polynomialAdapter) Evaluate(x int) *big.Int {
	return p.poly.Evaluate(big.NewInt(int64(x)), p.group)
}

func (p *polynomialAdapter) Coefficients() []*big.Int {
	result := make([]*big.Int, len(p.poly.Coefficients))
	copy(result, p.poly.Coefficients)
	return result
}

// GeneratePolynomial 生成随机多项式
func (e *Secp256k1DKGExecutor) GeneratePolynomial(threshold int) (runtime.PolynomialHandle, error) {
	poly := dkg.NewPolynomial(threshold, e.group)
	return &polynomialAdapter{poly: poly, group: e.group}, nil
}

// ComputeCommitments 计算 Feldman VSS 承诺点
func (e *Secp256k1DKGExecutor) ComputeCommitments(poly runtime.PolynomialHandle) [][]byte {
	coeffs := poly.Coefficients()
	result := make([][]byte, len(coeffs))
	for i, coef := range coeffs {
		point := e.group.ScalarBaseMult(coef)
		result[i] = e.group.SerializePoint(point)
	}
	return result
}

// EvaluateShare 计算发送给接收者的 share
func (e *Secp256k1DKGExecutor) EvaluateShare(poly runtime.PolynomialHandle, receiverIndex int) []byte {
	share := poly.Evaluate(receiverIndex)
	result := make([]byte, 32)
	share.FillBytes(result)
	return result
}

// AggregateShares 累加 shares
func (e *Secp256k1DKGExecutor) AggregateShares(shares [][]byte) []byte {
	sum := big.NewInt(0)
	order := e.group.Order()
	for _, s := range shares {
		val := new(big.Int).SetBytes(s)
		sum.Add(sum, val)
		sum.Mod(sum, order)
	}
	result := make([]byte, 32)
	sum.FillBytes(result)
	return result
}

// ComputeGroupPubkey 计算群公钥（所有 A_0 之和）
func (e *Secp256k1DKGExecutor) ComputeGroupPubkey(ai0List [][]byte) []byte {
	if len(ai0List) == 0 {
		return nil
	}

	// 解析第一个点
	result := e.group.DecompressPoint(ai0List[0])

	// 累加其他点
	for i := 1; i < len(ai0List); i++ {
		p := e.group.DecompressPoint(ai0List[i])
		result = e.group.Add(result, p)
	}

	return e.group.SerializePoint(result)
}

// VerifyShare 验证 share 与 commitment 一致
func (e *Secp256k1DKGExecutor) VerifyShare(share []byte, commitments [][]byte, senderIndex, receiverIndex int) bool {
	if len(share) == 0 || len(commitments) == 0 {
		return false
	}

	// 计算 g^share
	shareVal := new(big.Int).SetBytes(share)
	expectedPoint := e.group.ScalarBaseMult(shareVal)

	// 计算 Σ A_k * (receiverIndex)^k
	x := big.NewInt(int64(receiverIndex))
	var computedPoint curve.Point
	first := true

	for k, commitBytes := range commitments {
		Ak := e.group.DecompressPoint(commitBytes)
		// x^k
		xPowK := new(big.Int).Exp(x, big.NewInt(int64(k)), e.group.Order())
		// A_k * x^k
		term := e.group.ScalarMult(Ak, xPowK)

		if first {
			computedPoint = term
			first = false
		} else {
			computedPoint = e.group.Add(computedPoint, term)
		}
	}

	// 比较两个点
	return expectedPoint.X.Cmp(computedPoint.X) == 0 && expectedPoint.Y.Cmp(computedPoint.Y) == 0
}

// SchnorrSign 使用私钥生成 Schnorr 签名
func (e *Secp256k1DKGExecutor) SchnorrSign(privateKey *big.Int, msgHash []byte) ([]byte, error) {
	// 生成随机 nonce
	k := dkg.RandomScalar(e.group.Order())

	// R = g^k
	R := e.group.ScalarBaseMult(k)

	// P = g^privateKey
	P := e.group.ScalarBaseMult(privateKey)

	// e = H(R || P || m)
	h := sha256.New()
	h.Write(e.group.SerializePoint(R))
	h.Write(e.group.SerializePoint(P))
	h.Write(msgHash)
	eBytes := h.Sum(nil)
	eVal := new(big.Int).SetBytes(eBytes)
	eVal.Mod(eVal, e.group.Order())

	// s = k + e * privateKey
	s := new(big.Int).Mul(eVal, privateKey)
	s.Add(s, k)
	s.Mod(s, e.group.Order())

	// 签名 = R.x || s
	sig := make([]byte, 64)
	R.X.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return sig, nil
}

// ScalarBaseMult 基点乘法
func (e *Secp256k1DKGExecutor) ScalarBaseMult(k *big.Int) runtime.CurvePoint {
	p := e.group.ScalarBaseMult(k)
	return runtime.CurvePoint{X: p.X, Y: p.Y}
}

func (e *Secp256k1DKGExecutor) SerializePoint(P runtime.CurvePoint) []byte {
	return e.group.SerializePoint(curve.Point{X: P.X, Y: P.Y})
}

// BN256DKGExecutor BN256 曲线的 DKG 执行器
type BN256DKGExecutor struct {
	group curve.Group
}

// NewBN256DKGExecutor 创建 BN256 DKG 执行器
func NewBN256DKGExecutor() *BN256DKGExecutor {
	return &BN256DKGExecutor{
		group: curve.NewBN256Group(),
	}
}

func (e *BN256DKGExecutor) GeneratePolynomial(threshold int) (runtime.PolynomialHandle, error) {
	poly := dkg.NewPolynomial(threshold, e.group)
	return &polynomialAdapter{poly: poly, group: e.group}, nil
}

func (e *BN256DKGExecutor) ComputeCommitments(poly runtime.PolynomialHandle) [][]byte {
	coeffs := poly.Coefficients()
	result := make([][]byte, len(coeffs))
	for i, coef := range coeffs {
		point := e.group.ScalarBaseMult(coef)
		result[i] = e.group.SerializePoint(point)
	}
	return result
}

func (e *BN256DKGExecutor) EvaluateShare(poly runtime.PolynomialHandle, receiverIndex int) []byte {
	share := poly.Evaluate(receiverIndex)
	result := make([]byte, 32)
	share.FillBytes(result)
	return result
}

func (e *BN256DKGExecutor) AggregateShares(shares [][]byte) []byte {
	sum := big.NewInt(0)
	order := e.group.Order()
	for _, s := range shares {
		val := new(big.Int).SetBytes(s)
		sum.Add(sum, val)
		sum.Mod(sum, order)
	}
	result := make([]byte, 32)
	sum.FillBytes(result)
	return result
}

func (e *BN256DKGExecutor) ComputeGroupPubkey(ai0List [][]byte) []byte {
	if len(ai0List) == 0 {
		return nil
	}
	result := e.group.DecompressPoint(ai0List[0])
	for i := 1; i < len(ai0List); i++ {
		p := e.group.DecompressPoint(ai0List[i])
		result = e.group.Add(result, p)
	}
	return e.group.SerializePoint(result)
}

func (e *BN256DKGExecutor) VerifyShare(share []byte, commitments [][]byte, senderIndex, receiverIndex int) bool {
	if len(share) == 0 || len(commitments) == 0 {
		return false
	}
	shareVal := new(big.Int).SetBytes(share)
	expectedPoint := e.group.ScalarBaseMult(shareVal)
	x := big.NewInt(int64(receiverIndex))
	var computedPoint curve.Point
	first := true
	for k, commitBytes := range commitments {
		Ak := e.group.DecompressPoint(commitBytes)
		xPowK := new(big.Int).Exp(x, big.NewInt(int64(k)), e.group.Order())
		term := e.group.ScalarMult(Ak, xPowK)
		if first {
			computedPoint = term
			first = false
		} else {
			computedPoint = e.group.Add(computedPoint, term)
		}
	}
	return expectedPoint.X.Cmp(computedPoint.X) == 0 && expectedPoint.Y.Cmp(computedPoint.Y) == 0
}

func (e *BN256DKGExecutor) SchnorrSign(privateKey *big.Int, msgHash []byte) ([]byte, error) {
	// BN256 Schnorr using Keccak256
	k := dkg.RandomScalar(e.group.Order())
	R := e.group.ScalarBaseMult(k)
	P := e.group.ScalarBaseMult(privateKey)

	h := sha3.NewLegacyKeccak256()
	h.Write(e.group.SerializePoint(curve.Point{X: R.X, Y: R.Y}))
	h.Write(e.group.SerializePoint(curve.Point{X: P.X, Y: P.Y}))
	h.Write(msgHash)
	eVal := new(big.Int).SetBytes(h.Sum(nil))
	eVal.Mod(eVal, e.group.Order())

	s := new(big.Int).Mul(eVal, privateKey)
	s.Add(s, k)
	s.Mod(s, e.group.Order())

	// Signature = R || s (64+32 = 96 bytes for uncompressed or 64 bytes total if R is serialized)
	// For BN256 we use 64 bytes (R.x || s) or full R.
	// Since group.SerializePoint for BN256 is 64 bytes (X||Y), we use that + 32 bytes s?
	// Actually, the protocol usually expects 64 bytes for Schnorr.
	// Let's use R.x (32) || s (32) to keep it 64 bytes.
	sig := make([]byte, 64)
	R.X.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return sig, nil
}

func (e *BN256DKGExecutor) ScalarBaseMult(k *big.Int) runtime.CurvePoint {
	p := e.group.ScalarBaseMult(k)
	return runtime.CurvePoint{X: p.X, Y: p.Y}
}

func (e *BN256DKGExecutor) SerializePoint(P runtime.CurvePoint) []byte {
	return e.group.SerializePoint(curve.Point{X: P.X, Y: P.Y})
}

// Ed25519DKGExecutor Ed25519 曲线的 DKG 执行器
type Ed25519DKGExecutor struct {
	group curve.Group
}

// NewEd25519DKGExecutor 创建 Ed25519 DKG 执行器
func NewEd25519DKGExecutor() *Ed25519DKGExecutor {
	return &Ed25519DKGExecutor{
		group: curve.NewEd25519Group(),
	}
}

func (e *Ed25519DKGExecutor) GeneratePolynomial(threshold int) (runtime.PolynomialHandle, error) {
	poly := dkg.NewPolynomial(threshold, e.group)
	return &polynomialAdapter{poly: poly, group: e.group}, nil
}

func (e *Ed25519DKGExecutor) ComputeCommitments(poly runtime.PolynomialHandle) [][]byte {
	coeffs := poly.Coefficients()
	result := make([][]byte, len(coeffs))
	for i, coef := range coeffs {
		point := e.group.ScalarBaseMult(coef)
		result[i] = e.group.SerializePoint(point)
	}
	return result
}

func (e *Ed25519DKGExecutor) EvaluateShare(poly runtime.PolynomialHandle, receiverIndex int) []byte {
	share := poly.Evaluate(receiverIndex)
	result := make([]byte, 32)
	share.FillBytes(result)
	return result
}

func (e *Ed25519DKGExecutor) AggregateShares(shares [][]byte) []byte {
	sum := big.NewInt(0)
	order := e.group.Order()
	for _, s := range shares {
		val := new(big.Int).SetBytes(s)
		sum.Add(sum, val)
		sum.Mod(sum, order)
	}
	result := make([]byte, 32)
	sum.FillBytes(result)
	return result
}

func (e *Ed25519DKGExecutor) ComputeGroupPubkey(ai0List [][]byte) []byte {
	if len(ai0List) == 0 {
		return nil
	}
	result := e.group.DecompressPoint(ai0List[0])
	for i := 1; i < len(ai0List); i++ {
		p := e.group.DecompressPoint(ai0List[i])
		result = e.group.Add(result, p)
	}
	return e.group.SerializePoint(result)
}

func (e *Ed25519DKGExecutor) VerifyShare(share []byte, commitments [][]byte, senderIndex, receiverIndex int) bool {
	if len(share) == 0 || len(commitments) == 0 {
		return false
	}
	shareVal := new(big.Int).SetBytes(share)
	expectedPoint := e.group.ScalarBaseMult(shareVal)
	x := big.NewInt(int64(receiverIndex))
	var computedPoint curve.Point
	first := true
	for k, commitBytes := range commitments {
		Ak := e.group.DecompressPoint(commitBytes)
		xPowK := new(big.Int).Exp(x, big.NewInt(int64(k)), e.group.Order())
		term := e.group.ScalarMult(Ak, xPowK)
		if first {
			computedPoint = term
			first = false
		} else {
			computedPoint = e.group.Add(computedPoint, term)
		}
	}
	return expectedPoint.X.Cmp(computedPoint.X) == 0 && expectedPoint.Y.Cmp(computedPoint.Y) == 0
}

func (e *Ed25519DKGExecutor) SchnorrSign(privateKey *big.Int, msgHash []byte) ([]byte, error) {
	// Ed25519 Schnorr using SHA512 and little-endian
	k := dkg.RandomScalar(e.group.Order())
	R := e.group.ScalarBaseMult(k)
	P := e.group.ScalarBaseMult(privateKey)

	h := crypto.SHA512.New()
	h.Write(e.group.SerializePoint(curve.Point{X: R.X, Y: R.Y}))
	h.Write(e.group.SerializePoint(curve.Point{X: P.X, Y: P.Y}))
	h.Write(msgHash)
	digest := h.Sum(nil)

	eVal := new(big.Int).SetBytes(reverse(digest))
	eVal.Mod(eVal, e.group.Order())

	s := new(big.Int).Mul(eVal, privateKey)
	s.Add(s, k)
	s.Mod(s, e.group.Order())

	// Signature = R (32 bytes compressed) || s (32 bytes little-endian)
	sig := make([]byte, 64)
	copy(sig[:32], e.group.SerializePoint(curve.Point{X: R.X, Y: R.Y}))
	sBytes := make([]byte, 32)
	s.FillBytes(sBytes)
	copy(sig[32:], reverse(sBytes))
	return sig, nil
}

func (e *Ed25519DKGExecutor) ScalarBaseMult(k *big.Int) runtime.CurvePoint {
	p := e.group.ScalarBaseMult(k)
	return runtime.CurvePoint{X: p.X, Y: p.Y}
}

func (e *Ed25519DKGExecutor) SerializePoint(P runtime.CurvePoint) []byte {
	return e.group.SerializePoint(curve.Point{X: P.X, Y: P.Y})
}

// ========== 工厂实现 ==========

// DefaultCryptoExecutorFactory 默认密码学执行器工厂
type DefaultCryptoExecutorFactory struct{}

// NewDefaultCryptoExecutorFactory 创建默认工厂
func NewDefaultCryptoExecutorFactory() *DefaultCryptoExecutorFactory {
	return &DefaultCryptoExecutorFactory{}
}

// NewROASTExecutor 根据签名算法创建 ROAST 执行器
func (f *DefaultCryptoExecutorFactory) NewROASTExecutor(signAlgo int32) (runtime.ROASTExecutor, error) {
	switch pb.SignAlgo(signAlgo) {
	case pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340:
		return NewSecp256k1ROASTExecutor(), nil
	case pb.SignAlgo_SIGN_ALGO_SCHNORR_ALT_BN128:
		return NewBN256ROASTExecutor(), nil
	case pb.SignAlgo_SIGN_ALGO_ED25519:
		return NewEd25519ROASTExecutor(), nil
	default:
		return nil, ErrUnsupportedSignAlgo
	}
}

// NewDKGExecutor 根据签名算法创建 DKG 执行器
func (f *DefaultCryptoExecutorFactory) NewDKGExecutor(signAlgo int32) (runtime.DKGExecutor, error) {
	switch pb.SignAlgo(signAlgo) {
	case pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340:
		return NewSecp256k1DKGExecutor(), nil
	case pb.SignAlgo_SIGN_ALGO_SCHNORR_ALT_BN128:
		return NewBN256DKGExecutor(), nil
	case pb.SignAlgo_SIGN_ALGO_ED25519:
		return NewEd25519DKGExecutor(), nil
	default:
		return nil, ErrUnsupportedSignAlgo
	}
}
