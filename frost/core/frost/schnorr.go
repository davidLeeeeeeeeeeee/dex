package frost

import (
	"crypto/sha256"
	"math/big"

	"dex/frost/core/curve"

	"github.com/btcsuite/btcd/btcec/v2"
)

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
func SchnorrSign(grp curve.Group, x *big.Int, m []byte) (Rx, Ry, z, k *big.Int) {
	if len(m) != 32 {
		panic("BIP-340: message must be a 32-byte hash")
	}
	n := grp.Order()

	// ---------- 1. 处理私钥/公钥 ----------
	d := new(big.Int).Set(x)
	Px, Py := grp.ScalarBaseMult(d).XY()
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
	Rx, Ry = grp.ScalarBaseMultBytes(kArr[:]).XY()
	if Ry.Bit(0) == 1 {
		kInt.Sub(n, kInt)
		kBytes := kInt.FillBytes(make([]byte, 32))
		Rx, Ry = grp.ScalarBaseMultBytes(kBytes).XY()
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
func SchnorrVerify(grp curve.Group, Xx, Xy, Rx, Ry, z *big.Int, m []byte) bool {
	n := grp.Order()
	if len(m) != 32 || Ry.Bit(0) == 1 { // Ry 必须是偶数
		return false
	}

	// e = H_tag("BIP0340/challenge", Rx || Xx || m) mod n
	// 若公钥的 Y 是奇数，翻到偶数（与签名侧保持一致）
	if Xy.Bit(0) == 1 {
		Xy = new(big.Int).Sub(grp.Modulus(), Xy)
	}
	eBytes := taggedHash("BIP0340/challenge",
		Rx.Bytes(),
		Xx.Bytes(),
		m,
	)
	e := new(big.Int).SetBytes(eBytes)
	e.Mod(e, n)

	// 检查:  z·G  ?=  R + e·X
	Gz_x, Gz_y := grp.ScalarBaseMultBytes(z.Bytes()).XY()
	X := curve.Point{X: Xx, Y: Xy}
	eX := grp.ScalarMultBytes(X, e.Bytes())
	R := curve.Point{X: Rx, Y: Ry}
	sum_x, sum_y := grp.Add(R, eX).XY()

	return Gz_x.Cmp(sum_x) == 0 && Gz_y.Cmp(sum_y) == 0
}
