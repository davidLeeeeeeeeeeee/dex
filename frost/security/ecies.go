// frost/security/ecies.go
// ECIES 加解密工具（用于 DKG share 加密）
// 密文格式：ephemeralPubKey (33 bytes) || ciphertext (32 bytes) || mac (32 bytes)

package security

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"math/big"

	"github.com/btcsuite/btcd/btcec/v2"
)

var (
	ErrInvalidCiphertext     = errors.New("invalid ciphertext format")
	ErrMacVerificationFailed = errors.New("mac verification failed")
	ErrInvalidPublicKey      = errors.New("invalid public key")
	ErrInvalidPrivateKey     = errors.New("invalid private key")
)

// ECIESCiphertext ECIES 密文结构
// 格式：ephemeralPubKey (33 bytes compressed) || encrypted (len=plaintext len) || mac (32 bytes)
type ECIESCiphertext struct {
	EphemeralPubKey []byte // 33 bytes compressed public key
	Encrypted       []byte // 加密的数据
	Mac             []byte // HMAC-SHA256
}

// ECIESEncrypt 使用 secp256k1 ECIES 加密
// recipientPubKey: 接收者公钥（33 字节压缩格式）
// plaintext: 明文（通常是 32 字节 share）
// randomness: 加密随机数（32 字节，用于确定性重放）
func ECIESEncrypt(recipientPubKey, plaintext, randomness []byte) ([]byte, error) {
	if len(recipientPubKey) != 33 {
		return nil, ErrInvalidPublicKey
	}
	if len(randomness) != 32 {
		return nil, errors.New("randomness must be 32 bytes")
	}

	// 解析接收者公钥
	pubKey, err := btcec.ParsePubKey(recipientPubKey)
	if err != nil {
		return nil, ErrInvalidPublicKey
	}

	// 使用 randomness 作为临时私钥
	var privKeyBytes [32]byte
	copy(privKeyBytes[:], randomness)
	ephemeralPriv := secp256k1PrivKeyFromBytes(privKeyBytes[:])

	// 计算临时公钥
	ephemeralPub := ephemeralPriv.PubKey()
	ephemeralPubBytes := ephemeralPub.SerializeCompressed()

	// 计算共享密钥：ECDH
	sharedX, _ := btcec.S256().ScalarMult(pubKey.X(), pubKey.Y(), ephemeralPriv.Serialize())
	sharedSecret := sha256.Sum256(sharedX.Bytes())

	// 派生加密密钥和 MAC 密钥
	encKey := sharedSecret[:16]
	macKey := sharedSecret[16:]

	// AES-CTR 加密
	encrypted, err := aesCTREncrypt(encKey, plaintext)
	if err != nil {
		return nil, err
	}

	// 计算 HMAC
	mac := computeHMAC(macKey, encrypted)

	// 组装密文：ephemeralPub || encrypted || mac
	result := make([]byte, 0, 33+len(encrypted)+32)
	result = append(result, ephemeralPubBytes...)
	result = append(result, encrypted...)
	result = append(result, mac...)

	return result, nil
}

// ECIESVerifyCiphertext 验证密文是否由给定的明文和随机数生成
// recipientPubKey: 接收者公钥（33 字节压缩格式）
// plaintext: 明文 share
// randomness: 加密随机数
// ciphertext: 链上存储的密文
func ECIESVerifyCiphertext(recipientPubKey, plaintext, randomness, ciphertext []byte) bool {
	// 重新加密
	recomputed, err := ECIESEncrypt(recipientPubKey, plaintext, randomness)
	if err != nil {
		return false
	}

	// 比较密文
	return bytes.Equal(recomputed, ciphertext)
}

// secp256k1PrivKeyFromBytes 从字节创建 secp256k1 私钥
func secp256k1PrivKeyFromBytes(privKeyBytes []byte) *btcec.PrivateKey {
	privKey, _ := btcec.PrivKeyFromBytes(privKeyBytes)
	return privKey
}

// aesCTREncrypt AES-CTR 加密
func aesCTREncrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	// 使用全零 IV（因为每个密钥只用一次）
	iv := make([]byte, aes.BlockSize)

	ciphertext := make([]byte, len(plaintext))
	stream := cipher.NewCTR(block, iv)
	stream.XORKeyStream(ciphertext, plaintext)

	return ciphertext, nil
}

// computeHMAC 计算 HMAC-SHA256
func computeHMAC(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// ParseECIESCiphertext 解析 ECIES 密文
func ParseECIESCiphertext(ciphertext []byte, plaintextLen int) (*ECIESCiphertext, error) {
	// 最小长度：33 (pubkey) + plaintextLen + 32 (mac)
	expectedLen := 33 + plaintextLen + 32
	if len(ciphertext) != expectedLen {
		return nil, ErrInvalidCiphertext
	}

	return &ECIESCiphertext{
		EphemeralPubKey: ciphertext[:33],
		Encrypted:       ciphertext[33 : 33+plaintextLen],
		Mac:             ciphertext[33+plaintextLen:],
	}, nil
}

// GetReceiverPubKeyFromAddress 从地址获取接收者公钥
// 注意：这需要从链上状态获取，这里只是接口定义
// 实际实现需要查询 NodeInfo 或 PublicKeys
func GetReceiverPubKeyFromAddress(receiverID string, signAlgo int32) ([]byte, error) {
	// TODO: 从链上状态查询 receiver 的公钥
	// 需要根据 signAlgo 返回对应曲线的公钥
	return nil, errors.New("not implemented: need to query chain state")
}

// VerifyShareAgainstCommitment 验证 share 与 commitment 的一致性
// share: 32 字节标量
// commitmentPoints: dealer 的承诺点（Feldman VSS A_ik）
// receiverIndex: receiver 的索引（1-based）
func VerifyShareAgainstCommitment(share []byte, commitmentPoints [][]byte, receiverIndex *big.Int) bool {
	if len(share) == 0 || len(commitmentPoints) == 0 {
		return false
	}

	// g^share 计算
	shareInt := new(big.Int).SetBytes(share)
	gShareX, gShareY := btcec.S256().ScalarBaseMult(shareInt.Bytes())

	// 计算 Π A_ik * x^k（其中 x = receiverIndex）
	// expected = A_i0 * A_i1^x * A_i2^x^2 * ... * A_i(t-1)^x^(t-1)
	var expectedX, expectedY *big.Int

	xPower := big.NewInt(1) // x^0 = 1
	curve := btcec.S256()
	n := curve.Params().N

	for k, pointBytes := range commitmentPoints {
		// 解析承诺点
		pubKey, err := btcec.ParsePubKey(pointBytes)
		if err != nil {
			return false
		}

		// A_ik^(x^k)
		termX, termY := curve.ScalarMult(pubKey.X(), pubKey.Y(), xPower.Bytes())

		if k == 0 {
			expectedX, expectedY = termX, termY
		} else {
			expectedX, expectedY = curve.Add(expectedX, expectedY, termX, termY)
		}

		// x^(k+1) = x^k * x
		xPower.Mul(xPower, receiverIndex)
		xPower.Mod(xPower, n)
	}

	// 比较 g^share == expected
	return gShareX.Cmp(expectedX) == 0 && gShareY.Cmp(expectedY) == 0
}
