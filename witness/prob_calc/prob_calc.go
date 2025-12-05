package main

import (
	"fmt"
	"math"
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ln(C(n,k)) = ln(n!) - ln(k!) - ln((n-k)!)
func logChoose(n, k int) float64 {
	if k < 0 || k > n {
		return math.Inf(-1)
	}
	lnN, _ := math.Lgamma(float64(n + 1))
	lnK, _ := math.Lgamma(float64(k + 1))
	lnNK, _ := math.Lgamma(float64(n - k + 1))
	return lnN - lnK - lnNK
}

func logAddExp(a, b float64) float64 {
	if math.IsInf(a, -1) {
		return b
	}
	if math.IsInf(b, -1) {
		return a
	}
	if a < b {
		a, b = b, a
	}
	return a + math.Log1p(math.Exp(b-a))
}

// P(X >= k0), X ~ Hypergeom(N, K, n)
func hypergeomTail(N, K, n, k0 int) (p float64, logP float64) {
	logDen := logChoose(N, n)
	logSum := math.Inf(-1)

	maxK := min(n, K)
	for k := k0; k <= maxK; k++ {
		if n-k > N-K { // B 不够时跳过
			continue
		}
		logTerm := logChoose(K, k) + logChoose(N-K, n-k) - logDen
		logSum = logAddExp(logSum, logTerm)
	}
	return math.Exp(logSum), logSum
}

func main() {
	N := 10000
	K := 2000
	n := 1000

	thresholdPercent := 20                     // 至少50%
	k0 := (n*thresholdPercent + 100 - 1) / 100 // ceil(n*p/100)

	p, logP := hypergeomTail(N, K, n, k0)

	fmt.Printf("N=%d, K=%d, n=%d, 至少%d%% => X >= %d\n", N, K, n, thresholdPercent, k0)
	fmt.Printf("P(X >= %d) ≈ %.12f  (≈ %e)\n", k0, p, p)
	fmt.Printf("log10(P) ≈ %.6f\n", logP/math.Ln10)
}
