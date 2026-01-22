package curve

import (
	"crypto/sha256"
	"math/big"

	"github.com/btcsuite/btcd/btcec/v2"
)

// -----------------------------------------------------------------------------
// Taproot Key‑Path tweak helpers  —— 纯 *big.Int 接口
// -----------------------------------------------------------------------------

// PrivBytesToBig 把 "私钥序列化字节" 转 big.Int 的小工具。
func PrivBytesToBig(priv *btcec.PrivateKey) *big.Int {
	return new(big.Int).SetBytes(priv.Serialize()) // 32 bytes → big.Int
}

// TapTweakScalar(Px) = SHA256( H(tag) || H(tag) || Px )  mod n
func TapTweakScalar(Px *big.Int, tag string) *big.Int {
	curve := btcec.S256()
	Htag := sha256.Sum256([]byte(tag))

	h := sha256.New()
	h.Write(Htag[:])
	h.Write(Htag[:])
	h.Write(Px.Bytes())

	t := new(big.Int).SetBytes(h.Sum(nil))
	t.Mod(t, curve.Params().N)
	return t
}

// TweakPrivScalar 私钥级 tweak，入参/出参都是 *big.Int
func TweakPrivScalar(d *big.Int, tag string) *big.Int {
	curve := btcec.S256()
	n := curve.Params().N

	Px, _ := curve.ScalarBaseMult(d.Bytes())
	t := TapTweakScalar(Px, tag)

	dp := new(big.Int).Add(d, t)
	dp.Mod(dp, n)

	_, Py := curve.ScalarBaseMult(dp.Bytes())
	if Py.Bit(0) == 1 {
		dp.Sub(n, dp)
	}
	return dp
}

// TweakPubPoint 公钥级 tweak，入参 Px,Py 都是 *big.Int；返回 Qx,Qy 也是 *big.Int
func TweakPubPoint(Px, Py *big.Int, tag string) (*big.Int, *big.Int) {
	curve := btcec.S256()

	if Py.Bit(0) == 1 { // 翻到偶‑Y
		Py = new(big.Int).Sub(curve.Params().P, Py)
	}
	t := TapTweakScalar(Px, tag)
	tx, ty := curve.ScalarBaseMult(t.Bytes())
	return curve.Add(Px, Py, tx, ty)
}

// -----------------------------------------------------------------------------
// big.Int  →  *FieldVal   转换（适配 btcec/v2）
// -----------------------------------------------------------------------------

// BigIntToFieldVal packs a non‑negative *big.Int (≤ P‑1) into a new FieldVal.
//
// It panics if x ≥ 2^256.
func BigIntToFieldVal(x *big.Int) *btcec.FieldVal {
	if x.Sign() < 0 || x.BitLen() > 256 {
		panic("bigIntToFieldVal: out‑of‑range")
	}

	var be32 [32]byte
	b := x.Bytes()            // big‑endian without leading zeros
	copy(be32[32-len(b):], b) // right‑align

	var fv btcec.FieldVal
	fv.SetBytes(&be32) // constant‑time pack
	return &fv
}
