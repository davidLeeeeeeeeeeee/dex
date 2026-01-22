package curve

import (
	"math/big"

	"go.dedis.ch/kyber/v3"
	"go.dedis.ch/kyber/v3/group/edwards25519"
)

// Ed25519Group 将 Edwards25519 曲线封装为 Group 接口
// 注意：Solana 使用标准 Ed25519，此处基于 Kyber 实现其点运算
type Ed25519Group struct {
	suite *edwards25519.SuiteEd25519
}

func NewEd25519Group() *Ed25519Group {
	return &Ed25519Group{
		suite: edwards25519.NewBlakeSHA256Ed25519(),
	}
}

func (g *Ed25519Group) Order() *big.Int {
	// Ed25519 群阶 L = 2^252 + 27742317777372353535851937790883648493
	order, _ := new(big.Int).SetString("7237005577332262213973186563042994240857116359379907606001950938285454250989", 10)
	return order
}

func (g *Ed25519Group) Modulus() *big.Int {
	// Ed25519 素数域 P = 2^255 - 19
	p, _ := new(big.Int).SetString("57896044618658097711785492504343953926634992332820282019728792003956564819949", 10)
	return p
}

func (g *Ed25519Group) BitSize() int {
	return 255
}

func (g *Ed25519Group) ScalarBaseMult(k *big.Int) Point {
	kb := make([]byte, 32)
	k.FillBytes(kb)
	s := g.suite.Scalar().SetBytes(reverse(kb))

	p := g.suite.Point().Mul(s, nil)
	return g.toPoint(p)
}

func (g *Ed25519Group) ScalarMult(P Point, k *big.Int) Point {
	kp := g.fromPoint(P)

	kb := make([]byte, 32)
	k.FillBytes(kb)
	s := g.suite.Scalar().SetBytes(reverse(kb))

	res := g.suite.Point().Mul(s, kp)
	return g.toPoint(res)
}

func (g *Ed25519Group) Add(P, Q Point) Point {
	pp := g.fromPoint(P)
	qp := g.fromPoint(Q)
	res := g.suite.Point().Add(pp, qp)
	return g.toPoint(res)
}

func (g *Ed25519Group) ScalarBaseMultBytes(k []byte) Point {
	// Kyber Scalar.SetBytes usually expects little-endian for Ed25519
	// If input k is big-endian, we must reverse it.
	// But our interface definition for ScalarBaseMultBytes usually expects bytes that match the curve's expected format?
	// Actually, DKG/ROAST code handles scalars as big.Int in many places.
	// Let's assume input is 32 bytes.
	s := g.suite.Scalar().SetBytes(reverse(k))
	p := g.suite.Point().Mul(s, nil)
	return g.toPoint(p)
}

func (g *Ed25519Group) ScalarMultBytes(P Point, k []byte) Point {
	kp := g.fromPoint(P)
	s := g.suite.Scalar().SetBytes(reverse(k))
	res := g.suite.Point().Mul(s, kp)
	return g.toPoint(res)
}

func (g *Ed25519Group) SerializePoint(P Point) []byte {
	kp := g.fromPoint(P)
	b, _ := kp.MarshalBinary() // 返回 32 字节压缩格式
	return b
}

func (g *Ed25519Group) DecompressPoint(data []byte) Point {
	p := g.suite.Point()
	if err := p.UnmarshalBinary(data); err != nil {
		return Point{}
	}
	return g.toPoint(p)
}

// 辅助方法：Kyber Point -> generic Point
func (g *Ed25519Group) toPoint(p kyber.Point) Point {
	b, _ := p.MarshalBinary()
	// 注意：Ed25519 MarshalBinary 返回的是 32 字节压缩格式，不是 X, Y big.Int
	// 这里做一个 HACK: 将 32 字节序列化结果存入 X 字段，Y 字段保留为 0
	return Point{
		X: new(big.Int).SetBytes(b),
		Y: big.NewInt(0),
	}
}

// 辅助方法：generic Point -> Kyber Point
func (g *Ed25519Group) fromPoint(p Point) kyber.Point {
	kp := g.suite.Point()
	b := make([]byte, 32)
	p.X.FillBytes(b)
	_ = kp.UnmarshalBinary(b)
	return kp
}

func reverse(s []byte) []byte {
	res := make([]byte, len(s))
	for i, j := 0, len(s)-1; i < len(s); i, j = i+1, j-1 {
		res[i] = s[j]
	}
	return res
}
