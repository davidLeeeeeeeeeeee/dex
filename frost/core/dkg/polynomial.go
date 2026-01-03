package dkg

import (
	"crypto/rand"
	"io"
	"math/big"

	"dex/frost/core/curve"
)

// 使用可替换的随机数源，默认为 crypto/rand.Reader
var randReader io.Reader = rand.Reader

// RandomScalar 生成随机标量 [0, N-1]
func RandomScalar(n *big.Int) *big.Int {
	scalar, err := rand.Int(randReader, n)
	if err != nil {
		panic(err)
	}
	return scalar
}

// Polynomial 为t-1阶多项式，用于分布式密钥生成
type Polynomial struct {
	Coefficients []*big.Int // a_0, a_1, ..., a_(t-1)
}

// NewPolynomial 生成随机t-1阶多项式
func NewPolynomial(t int, grp curve.Group) *Polynomial {
	coeffs := make([]*big.Int, t)
	for i := 0; i < t; i++ {
		coeffs[i] = RandomScalar(grp.Order())
	}
	return &Polynomial{Coefficients: coeffs}
}

// Evaluate 在x处求值f(x) mod N
func (p *Polynomial) Evaluate(x *big.Int, grp curve.Group) *big.Int {
	N := grp.Order()
	result := big.NewInt(0)
	temp := big.NewInt(1)
	for i, coef := range p.Coefficients {
		temp.Exp(x, big.NewInt(int64(i)), N)
		temp.Mul(temp, coef)
		temp.Mod(temp, N)
		result.Add(result, temp)
		result.Mod(result, N)
	}
	return result
}
