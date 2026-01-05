// frost/runtime/adapters/crypto.go
// 实现 ROASTExecutor 和 DKGExecutor 接口，适配到 core 包

package adapters

import (
	"crypto/sha256"
	"errors"
	"math/big"

	"dex/frost/core/curve"
	"dex/frost/core/dkg"
	"dex/frost/core/roast"
	"dex/frost/runtime"
	"dex/pb"
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

// AggregateSignatures 聚合签名
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
		result[i] = serializePoint(point)
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

	return serializePoint(result)
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
	h.Write(serializePoint(R))
	h.Write(serializePoint(P))
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

// serializePoint 序列化点为压缩格式
func serializePoint(p curve.Point) []byte {
	result := make([]byte, 33)
	if p.Y.Bit(0) == 0 {
		result[0] = 0x02
	} else {
		result[0] = 0x03
	}
	p.X.FillBytes(result[1:])
	return result
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
		// TODO: 实现 BN128 执行器
		return nil, ErrUnsupportedSignAlgo
	case pb.SignAlgo_SIGN_ALGO_ED25519:
		// TODO: 实现 Ed25519 执行器
		return nil, ErrUnsupportedSignAlgo
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
		return nil, ErrUnsupportedSignAlgo
	case pb.SignAlgo_SIGN_ALGO_ED25519:
		return nil, ErrUnsupportedSignAlgo
	default:
		return nil, ErrUnsupportedSignAlgo
	}
}
