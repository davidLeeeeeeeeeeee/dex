package dkg

import (
	"math/big"

	"github.com/ethereum/go-ethereum/crypto/bn256/cloudflare"
)

// BN256Group 将 alt_bn128 曲线封装为 Group 接口
type BN256Group struct{}

// bn256_adapter.go（摘录）

func (g *BN256Group) Order() *big.Int {
	return new(big.Int).Set(bn256.Order)
}

func (g *BN256Group) Modulus() *big.Int {
	return new(big.Int).Set(bn256.P)
}

func (g *BN256Group) BitSize() int {
	return bn256.P.BitLen() // 254
}

func (g *BN256Group) ScalarBaseMult(k *big.Int) Point {
	R := new(bn256.G1).ScalarBaseMult(k)
	return unmarshalG1(R)
}

func (g *BN256Group) ScalarMult(pt Point, k *big.Int) Point {
	P := &bn256.G1{}
	buf := append(pad32(pt.X), pad32(pt.Y)...) // 32B||32B
	if _, err := P.Unmarshal(buf); err != nil {
		panic("bn256_adapter: unmarshal failed")
	}
	R := new(bn256.G1).ScalarMult(P, k)
	return unmarshalG1(R)
}

func (g *BN256Group) Add(a, b Point) Point {
	P1 := &bn256.G1{}
	buf1 := append(pad32(a.X), pad32(a.Y)...)
	if _, err := P1.Unmarshal(buf1); err != nil {
		panic("bn256_adapter: unmarshal a failed")
	}
	P2 := &bn256.G1{}
	buf2 := append(pad32(b.X), pad32(b.Y)...)
	if _, err := P2.Unmarshal(buf2); err != nil {
		panic("bn256_adapter: unmarshal b failed")
	}
	R := new(bn256.G1).Add(P1, P2)
	return unmarshalG1(R)
}

// pad32 将 big.Int 填充到 32 字节
func pad32(x *big.Int) []byte {
	out := make([]byte, 32)
	b := x.Bytes()
	copy(out[32-len(b):], b)
	return out
}

// unmarshalG1 将 *cloudflare.G1 转换为通用 Point
func unmarshalG1(P *bn256.G1) Point {
	b := P.Marshal() // 64 字节
	return Point{
		X: new(big.Int).SetBytes(b[:32]),
		Y: new(big.Int).SetBytes(b[32:]),
	}
}

// bn256_adapter.go
func (g *BN256Group) ScalarBaseMultBytes(k []byte) Point {
	kInt := new(big.Int).SetBytes(k)
	return g.ScalarBaseMult(kInt) // 调用已有的 *big.Int 版本
}
func (g *BN256Group) ScalarMultBytes(P Point, k []byte) Point {
	kInt := new(big.Int).SetBytes(k)
	return g.ScalarMult(P, kInt)
}
