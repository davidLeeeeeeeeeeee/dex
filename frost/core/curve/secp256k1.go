package curve

import (
	"crypto/elliptic"
	"math/big"

	"github.com/btcsuite/btcd/btcec/v2"
)

// Secp256k1Group 将 btcec.S256() 封装为 Group 接口
type Secp256k1Group struct {
	curve elliptic.Curve
}

// NewSecp256k1Group 返回一个新的 Secp256k1Group 实例
func NewSecp256k1Group() *Secp256k1Group {
	return &Secp256k1Group{curve: btcec.S256()}
}

func (g *Secp256k1Group) Order() *big.Int {
	return g.curve.Params().N
}

func (g *Secp256k1Group) ScalarBaseMult(k *big.Int) Point {
	x, y := g.curve.ScalarBaseMult(k.Bytes())
	return Point{X: x, Y: y}
}

func (g *Secp256k1Group) ScalarMult(P Point, k *big.Int) Point {
	x, y := g.curve.ScalarMult(P.X, P.Y, k.Bytes())
	return Point{X: x, Y: y}
}

func (g *Secp256k1Group) Add(P, Q Point) Point {
	x, y := g.curve.Add(P.X, P.Y, Q.X, Q.Y)
	return Point{X: x, Y: y}
}

func (g *Secp256k1Group) Modulus() *big.Int {
	return g.curve.Params().P
}

func (g *Secp256k1Group) BitSize() int {
	return g.curve.Params().BitSize
}

func (g *Secp256k1Group) ScalarBaseMultBytes(k []byte) Point {
	x, y := g.curve.ScalarBaseMult(k) // elliptic.Curve 本来就接收 []byte
	return Point{X: x, Y: y}
}

func (g *Secp256k1Group) ScalarMultBytes(P Point, k []byte) Point {
	x, y := g.curve.ScalarMult(P.X, P.Y, k)
	return Point{X: x, Y: y}
}

// DecompressPoint 从压缩格式解析点
// 压缩格式：1 字节前缀 (0x02/0x03) + 32 字节 X 坐标
func (g *Secp256k1Group) DecompressPoint(data []byte) Point {
	if len(data) != 33 {
		return Point{}
	}

	// 使用 btcec 解析压缩公钥
	pubKey, err := btcec.ParsePubKey(data)
	if err != nil {
		return Point{}
	}

	return Point{
		X: pubKey.X(),
		Y: pubKey.Y(),
	}
}
