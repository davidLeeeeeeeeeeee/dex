package security

import (
	"crypto/rand"
	"math/big"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func generateTestKeyPair(t *testing.T) (*btcec.PrivateKey, []byte) {
	privKey, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	pubKey := privKey.PubKey().SerializeCompressed()
	return privKey, pubKey
}

func randomBytes(t *testing.T, n int) []byte {
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

func TestECIESEncrypt_Basic(t *testing.T) {
	_, recipientPubKey := generateTestKeyPair(t)
	plaintext := randomBytes(t, 32)
	randomness := randomBytes(t, 32)

	ciphertext, err := ECIESEncrypt(recipientPubKey, plaintext, randomness)
	require.NoError(t, err)
	assert.Equal(t, 97, len(ciphertext))
}

func TestECIESEncrypt_Deterministic(t *testing.T) {
	_, recipientPubKey := generateTestKeyPair(t)
	plaintext := randomBytes(t, 32)
	randomness := randomBytes(t, 32)

	ciphertext1, err := ECIESEncrypt(recipientPubKey, plaintext, randomness)
	require.NoError(t, err)
	ciphertext2, err := ECIESEncrypt(recipientPubKey, plaintext, randomness)
	require.NoError(t, err)
	assert.Equal(t, ciphertext1, ciphertext2)
}

func TestECIESVerifyCiphertext_Valid(t *testing.T) {
	_, recipientPubKey := generateTestKeyPair(t)
	plaintext := randomBytes(t, 32)
	randomness := randomBytes(t, 32)

	ciphertext, err := ECIESEncrypt(recipientPubKey, plaintext, randomness)
	require.NoError(t, err)

	valid := ECIESVerifyCiphertext(recipientPubKey, plaintext, randomness, ciphertext)
	assert.True(t, valid)
}

func TestECIESVerifyCiphertext_WrongPlaintext(t *testing.T) {
	_, recipientPubKey := generateTestKeyPair(t)
	plaintext := randomBytes(t, 32)
	randomness := randomBytes(t, 32)

	ciphertext, err := ECIESEncrypt(recipientPubKey, plaintext, randomness)
	require.NoError(t, err)

	wrongPlaintext := randomBytes(t, 32)
	valid := ECIESVerifyCiphertext(recipientPubKey, wrongPlaintext, randomness, ciphertext)
	assert.False(t, valid)
}

func TestECIESVerifyCiphertext_WrongRandomness(t *testing.T) {
	_, recipientPubKey := generateTestKeyPair(t)
	plaintext := randomBytes(t, 32)
	randomness := randomBytes(t, 32)

	ciphertext, err := ECIESEncrypt(recipientPubKey, plaintext, randomness)
	require.NoError(t, err)

	wrongRandomness := randomBytes(t, 32)
	valid := ECIESVerifyCiphertext(recipientPubKey, plaintext, wrongRandomness, ciphertext)
	assert.False(t, valid)
}

func generateFeldmanVSSTestData(t *testing.T, threshold int, receiverIndex int) ([]byte, [][]byte) {
	curve := btcec.S256()
	n := curve.Params().N

	coefficients := make([]*big.Int, threshold)
	commitmentPoints := make([][]byte, threshold)

	for k := 0; k < threshold; k++ {
		// 生成随机系数作为私钥
		coeffBytes := randomBytes(t, 32)
		coefficients[k] = new(big.Int).SetBytes(coeffBytes)
		coefficients[k].Mod(coefficients[k], n)
		// 确保系数不为 0
		if coefficients[k].Sign() == 0 {
			coefficients[k].SetInt64(1)
		}

		// 使用 btcec.PrivKeyFromBytes 获取公钥（避免直接用 NewPublicKey）
		privKey, _ := btcec.PrivKeyFromBytes(coefficients[k].Bytes())
		commitmentPoints[k] = privKey.PubKey().SerializeCompressed()
	}

	x := big.NewInt(int64(receiverIndex))
	share := big.NewInt(0)
	xPower := big.NewInt(1)

	for k := 0; k < threshold; k++ {
		term := new(big.Int).Mul(coefficients[k], xPower)
		share.Add(share, term)
		share.Mod(share, n)
		xPower.Mul(xPower, x)
		xPower.Mod(xPower, n)
	}

	shareBytes := make([]byte, 32)
	shareBytesBig := share.Bytes()
	copy(shareBytes[32-len(shareBytesBig):], shareBytesBig)

	return shareBytes, commitmentPoints
}

func TestVerifyShareAgainstCommitment_Valid(t *testing.T) {
	threshold := 3
	receiverIndex := 5

	share, commitmentPoints := generateFeldmanVSSTestData(t, threshold, receiverIndex)

	valid := VerifyShareAgainstCommitment(share, commitmentPoints, big.NewInt(int64(receiverIndex)))
	assert.True(t, valid)
}

func TestVerifyShareAgainstCommitment_WrongShare(t *testing.T) {
	threshold := 3
	receiverIndex := 5

	_, commitmentPoints := generateFeldmanVSSTestData(t, threshold, receiverIndex)
	wrongShare := randomBytes(t, 32)

	valid := VerifyShareAgainstCommitment(wrongShare, commitmentPoints, big.NewInt(int64(receiverIndex)))
	assert.False(t, valid)
}

func TestVerifyShareAgainstCommitment_WrongIndex(t *testing.T) {
	threshold := 3
	receiverIndex := 5

	share, commitmentPoints := generateFeldmanVSSTestData(t, threshold, receiverIndex)

	wrongIndex := big.NewInt(int64(receiverIndex + 1))
	valid := VerifyShareAgainstCommitment(share, commitmentPoints, wrongIndex)
	assert.False(t, valid)
}
