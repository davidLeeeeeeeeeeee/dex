package sender

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"dex/logs"
	"dex/utils"
	"encoding/hex"
	"encoding/pem"
	"errors"
)

var (
	ClientPrivateKey *ecdsa.PrivateKey
	ClientPublicKey  *ecdsa.PublicKey
)

func InitKeys() {
	var err error
	ClientPrivateKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		logs.Error("Failed to generate client key: %v", err)
	}
	ClientPublicKey = &ClientPrivateKey.PublicKey
}

func SignMessage(priv *ecdsa.PrivateKey, message string) (string, error) {
	hash := sha256.Sum256([]byte(message))
	r, s, err := ecdsa.Sign(rand.Reader, priv, hash[:])
	if err != nil {
		return "", err
	}

	// 将 r 和 s 补足至 32 字节
	rBytes := r.Bytes()
	sBytes := s.Bytes()

	rBytesPadded := make([]byte, 32)
	sBytesPadded := make([]byte, 32)
	copy(rBytesPadded[32-len(rBytes):], rBytes)
	copy(sBytesPadded[32-len(sBytes):], sBytes)

	return hex.EncodeToString(rBytesPadded) + hex.EncodeToString(sBytesPadded), nil
}
func SignBlsMessage(message string) ([]byte, error) {
	priv := utils.GetKeyManager().PrivateKeyECDSA
	if priv == nil {
		return nil, errors.New("miner private key not initialized")
	}
	return utils.BLSSignWithCache(priv, message)
}
func PublicKeyToPEM(pub *ecdsa.PublicKey) string {
	derBytes, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		logs.Error("Failed to marshal public key: %v", err)
	}
	block := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: derBytes,
	}
	return string(pem.EncodeToMemory(block))
}
