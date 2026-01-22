package curve

import "math/big"

type Point struct{ X, Y *big.Int }

// 在 Point 里加：
func (p Point) XY() (*big.Int, *big.Int) { return p.X, p.Y }

type Group interface {
	// 群阶（原来 curve.Params().N）
	Order() *big.Int

	// 底层素数域（原来 curve.Params().P）
	Modulus() *big.Int

	// 安全位大小（原来 curve.Params().BitSize）
	BitSize() int

	// 基点乘
	ScalarBaseMult(k *big.Int) Point

	// 点乘
	ScalarMult(P Point, k *big.Int) Point

	// 点加
	Add(P, Q Point) Point
	ScalarBaseMultBytes(k []byte) Point
	ScalarMultBytes(P Point, k []byte) Point

	// 序列化点
	SerializePoint(P Point) []byte

	// 解析点
	DecompressPoint(data []byte) Point
}
