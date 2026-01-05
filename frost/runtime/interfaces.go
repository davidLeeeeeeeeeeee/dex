// frost/runtime/interfaces.go
// 定义 Runtime 层与 Core 层之间的抽象接口，实现解耦

package runtime

import "math/big"

// ========== 通用类型 ==========

// CurvePoint 抽象椭圆曲线点
type CurvePoint struct {
	X, Y *big.Int
}

// ========== ROAST 相关接口 ==========

// NonceInput 签名者的 nonce 输入
type NonceInput struct {
	SignerID     int
	HidingNonce  *big.Int // 标量（仅签名者本地使用）
	BindingNonce *big.Int // 标量（仅签名者本地使用）
	HidingPoint  CurvePoint
	BindingPoint CurvePoint
}

// ShareInput 签名份额输入
type ShareInput struct {
	SignerID int
	Share    *big.Int
}

// PartialSignParams 计算部分签名的参数
type PartialSignParams struct {
	SignerID     int
	HidingNonce  *big.Int
	BindingNonce *big.Int
	SecretShare  *big.Int // 签名者的密钥份额
	Rho          *big.Int // 绑定系数
	Lambda       *big.Int // 拉格朗日系数
	Challenge    *big.Int // 挑战值
}

// ROASTExecutor 抽象 ROAST 密码学操作
// Runtime 通过此接口调用 core/roast，避免直接依赖具体实现
type ROASTExecutor interface {
	// ComputeGroupCommitment 计算群承诺 R = Σ(R_i + ρ_i * R'_i)
	ComputeGroupCommitment(nonces []NonceInput, msg []byte) (CurvePoint, error)

	// ComputeBindingCoefficient 计算绑定系数 ρ_i
	ComputeBindingCoefficient(signerID int, msg []byte, nonces []NonceInput) *big.Int

	// ComputeChallenge 计算挑战值 e
	ComputeChallenge(R CurvePoint, groupPubX *big.Int, msg []byte) *big.Int

	// ComputeLagrangeCoefficients 计算拉格朗日系数
	ComputeLagrangeCoefficients(signerIDs []int) map[int]*big.Int

	// ComputePartialSignature 计算部分签名 z_i
	ComputePartialSignature(params PartialSignParams) *big.Int

	// AggregateSignatures 聚合签名份额，返回最终签名
	AggregateSignatures(R CurvePoint, shares []ShareInput) ([]byte, error)

	// GenerateNoncePair 生成 nonce 对
	GenerateNoncePair() (hiding, binding *big.Int, hidingPoint, bindingPoint CurvePoint, err error)

	// ScalarBaseMult 基点乘法 k*G
	ScalarBaseMult(k *big.Int) CurvePoint
}

// ========== DKG 相关接口 ==========

// PolynomialHandle 多项式句柄（不暴露内部结构）
type PolynomialHandle interface {
	// Evaluate 在 x 处求值
	Evaluate(x int) *big.Int
	// Coefficients 获取系数（只读）
	Coefficients() []*big.Int
}

// DKGExecutor 抽象 DKG 密码学操作
// Runtime 通过此接口调用 core/dkg，避免直接依赖具体实现
type DKGExecutor interface {
	// GeneratePolynomial 生成 t-1 阶随机多项式
	GeneratePolynomial(threshold int) (PolynomialHandle, error)

	// ComputeCommitments 计算 Feldman VSS 承诺点 A_k = g^{a_k}
	ComputeCommitments(poly PolynomialHandle) [][]byte

	// EvaluateShare 计算发送给 receiverIndex 的 share
	EvaluateShare(poly PolynomialHandle, receiverIndex int) []byte

	// AggregateShares 累加收到的所有 shares
	AggregateShares(shares [][]byte) []byte

	// ComputeGroupPubkey 计算群公钥（所有 A_0 之和）
	ComputeGroupPubkey(commitments [][]byte) []byte

	// VerifyShare 验证 share 与 commitment 一致
	VerifyShare(share []byte, commitments [][]byte, senderIndex, receiverIndex int) bool

	// SchnorrSign 使用私钥生成 Schnorr 签名（用于 DKG 验证）
	SchnorrSign(privateKey *big.Int, msgHash []byte) ([]byte, error)

	// ScalarBaseMult 基点乘法 k*G
	ScalarBaseMult(k *big.Int) CurvePoint
}

// ========== 工厂接口 ==========

// CryptoExecutorFactory 创建指定算法的密码学执行器
type CryptoExecutorFactory interface {
	// NewROASTExecutor 根据签名算法创建 ROAST 执行器
	NewROASTExecutor(signAlgo int32) (ROASTExecutor, error)

	// NewDKGExecutor 根据签名算法创建 DKG 执行器
	NewDKGExecutor(signAlgo int32) (DKGExecutor, error)
}

