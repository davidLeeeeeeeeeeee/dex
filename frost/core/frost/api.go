// frost/core/frost/api.go
// 统一签名验证 API，抽象不同签名算法
package frost

import (
	"errors"
	"math/big"

	"dex/frost/core/curve"
	"dex/pb"
)

// ErrUnsupportedSignAlgo 不支持的签名算法
var ErrUnsupportedSignAlgo = errors.New("unsupported sign algorithm")

// ErrInvalidSignature 无效签名
var ErrInvalidSignature = errors.New("invalid signature")

// ErrInvalidPublicKey 无效公钥
var ErrInvalidPublicKey = errors.New("invalid public key")

// ErrInvalidMessage 无效消息（长度不符合要求）
var ErrInvalidMessage = errors.New("invalid message")

// Verify 通用验签接口
// signAlgo: 签名算法类型
// pubkey: 公钥字节（格式由 signAlgo 决定）
// msg: 消息（对于 BIP340，必须是 32 字节哈希）
// sig: 签名字节
// 返回验证是否通过
func Verify(signAlgo pb.SignAlgo, pubkey, msg, sig []byte) (bool, error) {
	switch signAlgo {
	case pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340:
		return VerifyBIP340(pubkey, msg, sig)
	case pb.SignAlgo_SIGN_ALGO_SCHNORR_ALT_BN128:
		// TODO: 实现 bn128 验签
		return false, ErrUnsupportedSignAlgo
	case pb.SignAlgo_SIGN_ALGO_ED25519:
		// TODO: 实现 Ed25519 验签
		return false, ErrUnsupportedSignAlgo
	case pb.SignAlgo_SIGN_ALGO_ECDSA_SECP256K1:
		// TRX: 需要 GG20/CGGMP，v1 暂不支持
		return false, ErrUnsupportedSignAlgo
	default:
		return false, ErrUnsupportedSignAlgo
	}
}

// VerifyBIP340 验证 BIP-340 Schnorr 签名（BTC Taproot）
// pubkey: 32 字节 x-only 公钥
// msg: 32 字节消息哈希
// sig: 64 字节签名 (R.x || s)
func VerifyBIP340(pubkey, msg, sig []byte) (bool, error) {
	// 验证参数长度
	if len(pubkey) != 32 {
		return false, ErrInvalidPublicKey
	}
	if len(msg) != 32 {
		return false, ErrInvalidMessage
	}
	if len(sig) != 64 {
		return false, ErrInvalidSignature
	}

	// 解析公钥（x-only，需要恢复 y 坐标）
	grp := curve.NewSecp256k1Group()
	Xx := new(big.Int).SetBytes(pubkey)

	// 从 x 恢复 y（选择偶数 y）
	Xy := recoverYFromX(grp, Xx, false) // 选择偶数 y
	if Xy == nil {
		return false, ErrInvalidPublicKey
	}

	// 解析签名
	Rx := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])

	// 从 Rx 恢复 Ry（BIP-340 要求 Ry 为偶数）
	Ry := recoverYFromX(grp, Rx, false)
	if Ry == nil {
		return false, ErrInvalidSignature
	}

	// 调用底层验签
	valid := SchnorrVerify(grp, Xx, Xy, Rx, Ry, s, msg)
	return valid, nil
}

// recoverYFromX 从 x 坐标恢复 y 坐标
// grp: 椭圆曲线群
// x: x 坐标
// odd: true 返回奇数 y，false 返回偶数 y
func recoverYFromX(grp *curve.Secp256k1Group, x *big.Int, odd bool) *big.Int {
	// y² = x³ + 7 (mod p) for secp256k1
	p := grp.Modulus()

	// x³
	x3 := new(big.Int).Mul(x, x)
	x3.Mod(x3, p)
	x3.Mul(x3, x)
	x3.Mod(x3, p)

	// x³ + 7
	y2 := new(big.Int).Add(x3, big.NewInt(7))
	y2.Mod(y2, p)

	// 计算平方根: y = y2^((p+1)/4) mod p
	// secp256k1 的 p ≡ 3 (mod 4)，所以可以用这个公式
	exp := new(big.Int).Add(p, big.NewInt(1))
	exp.Rsh(exp, 2) // (p+1)/4
	y := new(big.Int).Exp(y2, exp, p)

	// 验证 y² = y2
	check := new(big.Int).Mul(y, y)
	check.Mod(check, p)
	if check.Cmp(y2) != 0 {
		return nil // x 不在曲线上
	}

	// 根据 odd 参数选择正确的 y
	if y.Bit(0) == 1 && !odd {
		y.Sub(p, y)
	} else if y.Bit(0) == 0 && odd {
		y.Sub(p, y)
	}

	return y
}

