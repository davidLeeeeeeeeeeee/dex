package utils

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
)

func DecodePublicKey(pemData string) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, errors.New("failed to decode PEM block")
	}
	pubKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ecdsaPub, ok := pubKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("public key is not ECDSA type")
	}
	return ecdsaPub, nil
}

func VerifySignature(pub *ecdsa.PublicKey, message, signatureHex string) bool {
	if len(signatureHex) != 128 {
		return false
	}

	rBytes, err := hexDecode(signatureHex[:64])
	if err != nil {
		return false
	}
	sBytes, err := hexDecode(signatureHex[64:])
	if err != nil {
		return false
	}

	r := new(big.Int).SetBytes(rBytes)
	s := new(big.Int).SetBytes(sBytes)

	hash := sha256.Sum256([]byte(message))
	return ecdsa.Verify(pub, hash[:], r, s)
}

func hexDecode(s string) ([]byte, error) {
	return hex.DecodeString(s)
}

func hexToBigInt(hexStr string) *big.Int {
	data := make([]byte, len(hexStr)/2)
	// 忽略错误处理以简化示例
	fmt.Sscanf(hexStr, "%x", &data)
	return new(big.Int).SetBytes(data)
}

func PublicKeyToPEM(pub *ecdsa.PublicKey) string {
	derBytes, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		// 根据实际情况处理错误
		return ""
	}
	block := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: derBytes,
	}
	return string(pem.EncodeToMemory(block))
}
func ExtractPublicKeyString(pub *ecdsa.PublicKey) string {
	// 示例：对公钥X、Y序列化再hash
	pubBytes := elliptic.Marshal(pub.Curve, pub.X, pub.Y)
	hash := sha256.Sum256(pubBytes)
	return hex.EncodeToString(hash[:])
}
