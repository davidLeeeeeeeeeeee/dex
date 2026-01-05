// frost/core/frost/api.go
// 统一签名验证 API，抽象不同签名算法
package frost

import (
	"crypto/ed25519"
	"errors"
	"math/big"

	"dex/frost/core/curve"
	"dex/pb"

	"golang.org/x/crypto/sha3"
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
		return VerifyBN128(pubkey, msg, sig)
	case pb.SignAlgo_SIGN_ALGO_ED25519:
		return VerifyEd25519(pubkey, msg, sig)
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

// VerifyBN128 验证 alt_bn128 曲线上的 Schnorr 签名（ETH/BNB）
// pubkey: 64 字节公钥 (x || y)
// msg: 消息（任意长度，内部会 keccak256 哈希）
// sig: 64 字节签名 (R.x || s) 或 96 字节 (R.x || R.y || s)
func VerifyBN128(pubkey, msg, sig []byte) (bool, error) {
	// 验证公钥长度
	if len(pubkey) != 64 {
		return false, ErrInvalidPublicKey
	}

	// 解析公钥
	Px := new(big.Int).SetBytes(pubkey[:32])
	Py := new(big.Int).SetBytes(pubkey[32:])

	grp := &curve.BN256Group{}

	// 解析签名（支持两种格式）
	var Rx, s *big.Int
	var RyOptions []*big.Int

	if len(sig) == 64 {
		// 64 字节格式: R.x || s（需要恢复 R.y）
		Rx = new(big.Int).SetBytes(sig[:32])
		s = new(big.Int).SetBytes(sig[32:])
		// 从 Rx 恢复 Ry（bn128: y² = x³ + 3）
		// 有两个可能的 Y 值，需要都尝试
		Ry := recoverYFromXBN128(grp, Rx)
		if Ry == nil {
			return false, ErrInvalidSignature
		}
		// 另一个 Y 值是 p - Ry
		RyNeg := new(big.Int).Sub(grp.Modulus(), Ry)
		RyOptions = []*big.Int{Ry, RyNeg}
	} else if len(sig) == 96 {
		// 96 字节格式: R.x || R.y || s
		Rx = new(big.Int).SetBytes(sig[:32])
		Ry := new(big.Int).SetBytes(sig[32:64])
		s = new(big.Int).SetBytes(sig[64:])
		RyOptions = []*big.Int{Ry}
	} else {
		return false, ErrInvalidSignature
	}

	// 计算 challenge: e = keccak256(Rx || Px || msg) mod n
	e := bn128Challenge(Rx, Px, msg, grp)

	// 验证: s * G == R + e * P
	// 尝试两个可能的 R.y 值
	sG := grp.ScalarBaseMult(s)
	eP := grp.ScalarMult(curve.Point{X: Px, Y: Py}, e)

	for _, Ry := range RyOptions {
		R := curve.Point{X: Rx, Y: Ry}
		expected := grp.Add(R, eP)
		if sG.X.Cmp(expected.X) == 0 && sG.Y.Cmp(expected.Y) == 0 {
			return true, nil
		}
	}

	return false, nil
}

// bn128Challenge 计算 BN128 Schnorr challenge
// e = keccak256(Rx || Px || msg) mod n
func bn128Challenge(Rx, Px *big.Int, msg []byte, grp *curve.BN256Group) *big.Int {
	h := sha3.NewLegacyKeccak256()

	// pad32 填充到 32 字节
	pad32 := func(n *big.Int) []byte {
		out := make([]byte, 32)
		b := n.Bytes()
		copy(out[32-len(b):], b)
		return out
	}

	h.Write(pad32(Rx))
	h.Write(pad32(Px))
	h.Write(msg)

	eBytes := h.Sum(nil)
	e := new(big.Int).SetBytes(eBytes)
	e.Mod(e, grp.Order())
	return e
}

// recoverYFromXBN128 从 x 坐标恢复 y 坐标（bn128 曲线）
// bn128: y² = x³ + 3 (mod p)
func recoverYFromXBN128(grp *curve.BN256Group, x *big.Int) *big.Int {
	p := grp.Modulus()

	// x³
	x3 := new(big.Int).Mul(x, x)
	x3.Mod(x3, p)
	x3.Mul(x3, x)
	x3.Mod(x3, p)

	// x³ + 3
	y2 := new(big.Int).Add(x3, big.NewInt(3))
	y2.Mod(y2, p)

	// 计算平方根（使用 Tonelli-Shanks 或直接 (p+1)/4）
	// bn128 的 p ≡ 3 (mod 4)，可以用简化公式
	exp := new(big.Int).Add(p, big.NewInt(1))
	exp.Rsh(exp, 2) // (p+1)/4
	y := new(big.Int).Exp(y2, exp, p)

	// 验证 y² = y2
	check := new(big.Int).Mul(y, y)
	check.Mod(check, p)
	if check.Cmp(y2) != 0 {
		return nil
	}

	return y
}

// VerifyEd25519 验证 Ed25519 签名（SOL）
// 使用标准 Ed25519 验签（非 FROST 聚合签名时可直接用标准库）
// pubkey: 32 字节公钥
// msg: 消息（任意长度）
// sig: 64 字节签名
func VerifyEd25519(pubkey, msg, sig []byte) (bool, error) {
	// 验证参数长度
	if len(pubkey) != ed25519.PublicKeySize { // 32 bytes
		return false, ErrInvalidPublicKey
	}
	if len(sig) != ed25519.SignatureSize { // 64 bytes
		return false, ErrInvalidSignature
	}

	// 使用 Go 标准库验证
	// 注意：FROST-Ed25519 聚合签名与标准 Ed25519 签名格式兼容
	valid := ed25519.Verify(pubkey, msg, sig)
	return valid, nil
}
