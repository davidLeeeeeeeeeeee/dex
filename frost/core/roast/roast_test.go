// frost/core/roast/roast_test.go
// ROAST 核心算法单元测试

package roast

import (
	"crypto/sha256"
	"math/big"
	"testing"

	"dex/frost/core/curve"
)

// TestComputeBindingCoefficient 测试绑定系数计算
func TestComputeBindingCoefficient(t *testing.T) {
	group := curve.NewSecp256k1Group()

	// 创建测试 nonces
	nonces := []SignerNonce{
		{
			SignerID:     1,
			HidingPoint:  group.ScalarBaseMult(big.NewInt(100)),
			BindingPoint: group.ScalarBaseMult(big.NewInt(200)),
		},
		{
			SignerID:     2,
			HidingPoint:  group.ScalarBaseMult(big.NewInt(300)),
			BindingPoint: group.ScalarBaseMult(big.NewInt(400)),
		},
	}

	msg := sha256.Sum256([]byte("test message"))

	// 计算绑定系数
	rho1 := ComputeBindingCoefficient(1, msg[:], nonces, group)
	rho2 := ComputeBindingCoefficient(2, msg[:], nonces, group)

	// 验证不同签名者有不同的绑定系数
	if rho1.Cmp(rho2) == 0 {
		t.Error("Different signers should have different binding coefficients")
	}

	// 验证相同输入产生相同输出（确定性）
	rho1Again := ComputeBindingCoefficient(1, msg[:], nonces, group)
	if rho1.Cmp(rho1Again) != 0 {
		t.Error("Same input should produce same binding coefficient")
	}

	t.Logf("rho_1 = %x", rho1.Bytes())
	t.Logf("rho_2 = %x", rho2.Bytes())
}

// TestComputeGroupCommitment 测试群承诺计算
func TestComputeGroupCommitment(t *testing.T) {
	group := curve.NewSecp256k1Group()

	// 创建测试 nonces
	nonces := []SignerNonce{
		{
			SignerID:     1,
			HidingNonce:  big.NewInt(100),
			BindingNonce: big.NewInt(200),
			HidingPoint:  group.ScalarBaseMult(big.NewInt(100)),
			BindingPoint: group.ScalarBaseMult(big.NewInt(200)),
		},
		{
			SignerID:     2,
			HidingNonce:  big.NewInt(300),
			BindingNonce: big.NewInt(400),
			HidingPoint:  group.ScalarBaseMult(big.NewInt(300)),
			BindingPoint: group.ScalarBaseMult(big.NewInt(400)),
		},
	}

	msg := sha256.Sum256([]byte("test message"))

	R, err := ComputeGroupCommitment(nonces, msg[:], group)
	if err != nil {
		t.Fatalf("ComputeGroupCommitment failed: %v", err)
	}

	// 验证结果不为零点
	if R.X.Cmp(big.NewInt(0)) == 0 && R.Y.Cmp(big.NewInt(0)) == 0 {
		t.Error("Group commitment should not be zero point")
	}

	t.Logf("R.X = %x", R.X.Bytes())
}

// TestComputeChallenge 测试挑战值计算
func TestComputeChallenge(t *testing.T) {
	group := curve.NewSecp256k1Group()

	R := group.ScalarBaseMult(big.NewInt(12345))
	Px := group.ScalarBaseMult(big.NewInt(67890)).X
	msg := sha256.Sum256([]byte("test message"))

	e := ComputeChallenge(R, Px, msg[:], group)

	// 验证挑战值在群阶范围内
	if e.Cmp(group.Order()) >= 0 {
		t.Error("Challenge should be less than group order")
	}

	// 验证确定性
	e2 := ComputeChallenge(R, Px, msg[:], group)
	if e.Cmp(e2) != 0 {
		t.Error("Same input should produce same challenge")
	}

	t.Logf("e = %x", e.Bytes())
}

// TestSelectSubset 测试子集选择
func TestSelectSubset(t *testing.T) {
	signerIDs := []int{1, 2, 3, 4, 5}
	threshold := 3
	seed := []byte("test_seed")

	// 选择 t+1 个签名者
	subset := SelectSubset(signerIDs, threshold, seed, 0)
	if len(subset) != threshold+1 {
		t.Errorf("Expected %d signers, got %d", threshold+1, len(subset))
	}

	// 验证确定性
	subset2 := SelectSubset(signerIDs, threshold, seed, 0)
	for i := range subset {
		if subset[i] != subset2[i] {
			t.Error("Same seed should produce same subset")
		}
	}

	// 验证不同重试次数产生不同子集
	subset3 := SelectSubset(signerIDs, threshold, seed, 1)
	different := false
	for i := range subset {
		if subset[i] != subset3[i] {
			different = true
			break
		}
	}
	if !different {
		t.Error("Different retry count should produce different subset")
	}

	t.Logf("Subset (retry 0): %v", subset)
	t.Logf("Subset (retry 1): %v", subset3)
}

// TestAggregateSignatures 测试签名聚合
func TestAggregateSignatures(t *testing.T) {
	group := curve.NewSecp256k1Group()

	// 创建测试 R 点
	R := group.ScalarBaseMult(big.NewInt(99999))

	// 创建测试签名份额
	shares := []SignerShare{
		{SignerID: 1, Share: big.NewInt(111)},
		{SignerID: 2, Share: big.NewInt(222)},
		{SignerID: 3, Share: big.NewInt(333)},
	}

	sig, err := AggregateSignatures(R, shares, group)
	if err != nil {
		t.Fatalf("AggregateSignatures failed: %v", err)
	}

	// 验证签名长度
	if len(sig) != 64 {
		t.Errorf("Expected 64 bytes signature, got %d", len(sig))
	}

	// 验证 R.x 在签名的前 32 字节
	RxBytes := make([]byte, 32)
	R.X.FillBytes(RxBytes)
	for i := 0; i < 32; i++ {
		if sig[i] != RxBytes[i] {
			t.Error("First 32 bytes should be R.x")
			break
		}
	}

	t.Logf("Signature: %x", sig)
}

// TestComputePartialSignature 测试部分签名计算
func TestComputePartialSignature(t *testing.T) {
	group := curve.NewSecp256k1Group()
	order := group.Order()

	// 测试参数
	hidingNonce := big.NewInt(100)
	bindingNonce := big.NewInt(200)
	share := big.NewInt(12345)
	rho := big.NewInt(5)
	lambda := big.NewInt(3)
	e := big.NewInt(7)

	z := ComputePartialSignature(1, hidingNonce, bindingNonce, share, rho, lambda, e, group)

	// 手动验证计算
	// z = k + ρ * k' + λ * e * s
	expected := new(big.Int).Mul(rho, bindingNonce)
	expected.Add(expected, hidingNonce)
	expected.Mod(expected, order)

	term := new(big.Int).Mul(lambda, e)
	term.Mul(term, share)
	term.Mod(term, order)

	expected.Add(expected, term)
	expected.Mod(expected, order)

	if z.Cmp(expected) != 0 {
		t.Errorf("Partial signature mismatch: expected %s, got %s", expected.String(), z.String())
	}

	t.Logf("z = %s", z.String())
}

// TestComputeLagrangeCoefficients 测试拉格朗日系数
func TestComputeLagrangeCoefficients(t *testing.T) {
	group := curve.NewSecp256k1Group()

	signerIDs := []int{1, 2, 3}
	lambdas := ComputeLagrangeCoefficientsForSet(signerIDs, group.Order())

	// 验证每个签名者都有系数
	for _, id := range signerIDs {
		if lambdas[id] == nil {
			t.Errorf("Missing lambda for signer %d", id)
		}
	}

	// 验证系数之和（对于 x=0 处的插值）
	// 这里简单验证系数不为零
	for id, lambda := range lambdas {
		if lambda.Cmp(big.NewInt(0)) == 0 {
			t.Errorf("Lambda for signer %d should not be zero", id)
		}
		t.Logf("lambda_%d = %s", id, lambda.String())
	}
}
