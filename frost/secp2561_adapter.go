package dkg

import (
	"crypto/elliptic"
	"math/big"

	"github.com/btcsuite/btcd/btcec/v2"
)

// Secp2561Group 将 btcec.S256() 封装为 Group 接口
// 注意：文件名中 "2561" 对应 secp256k1
// 使用：grp := NewSecp2561Group()
type Secp2561Group struct {
	curve elliptic.Curve
}

// NewSecp2561Group 返回一个新的 Secp2561Group 实例
func NewSecp2561Group() *Secp2561Group {
	return &Secp2561Group{curve: btcec.S256()}
}

func (g *Secp2561Group) Order() *big.Int {
	return g.curve.Params().N
}

func (g *Secp2561Group) ScalarBaseMult(k *big.Int) Point {
	x, y := g.curve.ScalarBaseMult(k.Bytes())
	return Point{X: x, Y: y}
}

func (g *Secp2561Group) ScalarMult(P Point, k *big.Int) Point {
	x, y := g.curve.ScalarMult(P.X, P.Y, k.Bytes())
	return Point{X: x, Y: y}
}

func (g *Secp2561Group) Add(P, Q Point) Point {
	x, y := g.curve.Add(P.X, P.Y, Q.X, Q.Y)
	return Point{X: x, Y: y}
}

func (g *Secp2561Group) Modulus() *big.Int {
	return g.curve.Params().P
}

func (g *Secp2561Group) BitSize() int {
	return g.curve.Params().BitSize
}

// secp2561_adapter.go
func (g *Secp2561Group) ScalarBaseMultBytes(k []byte) Point {
	x, y := g.curve.ScalarBaseMult(k) // elliptic.Curve 本来就接收 []byte
	return Point{X: x, Y: y}
}
func (g *Secp2561Group) ScalarMultBytes(P Point, k []byte) Point {
	x, y := g.curve.ScalarMult(P.X, P.Y, k)
	return Point{X: x, Y: y}
}
