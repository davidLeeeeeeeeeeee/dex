package dkg

import "math/big"

// LagrangeInterpolation 根据t个份额恢复f(0)
func LagrangeInterpolation(shares [][2]*big.Int, N *big.Int) *big.Int {
	f0 := big.NewInt(0)
	for i := range shares {
		xi, yi := shares[i][0], shares[i][1]
		num := big.NewInt(1)
		den := big.NewInt(1)
		for j := range shares {
			if i == j {
				continue
			}
			xj := shares[j][0]
			temp := new(big.Int).Neg(xj)
			temp.Mod(temp, N)
			num.Mul(num, temp)
			num.Mod(num, N)

			diff := new(big.Int).Sub(xi, xj)
			diff.Mod(diff, N)
			den.Mul(den, diff)
			den.Mod(den, N)
		}

		denInv := new(big.Int).ModInverse(den, N)
		li := new(big.Int).Mul(num, denInv)
		li.Mod(li, N)

		term := new(big.Int).Mul(yi, li)
		term.Mod(term, N)
		f0.Add(f0, term)
		f0.Mod(f0, N)
	}
	return f0
}

// ComputeLagrangeCoefficients 为给定的一组参与者ID计算拉格朗日系数
func ComputeLagrangeCoefficients(selectedIDs []*big.Int, N *big.Int) []*big.Int {
	coeffs := make([]*big.Int, len(selectedIDs))
	for i := range selectedIDs {
		xi := selectedIDs[i]
		num := big.NewInt(1)
		den := big.NewInt(1)
		for j := range selectedIDs {
			if i == j {
				continue
			}
			xj := selectedIDs[j]
			tmp := new(big.Int).Neg(xj)
			tmp.Mod(tmp, N)
			num.Mul(num, tmp)
			num.Mod(num, N)

			diff := new(big.Int).Sub(xi, xj)
			diff.Mod(diff, N)
			den.Mul(den, diff)
			den.Mod(den, N)
		}
		denInv := new(big.Int).ModInverse(den, N)
		li := new(big.Int).Mul(num, denInv)
		li.Mod(li, N)
		coeffs[i] = li
	}
	return coeffs
}

