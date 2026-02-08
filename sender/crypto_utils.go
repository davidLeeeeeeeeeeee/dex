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

	rBytes := r.Bytes()
	sBytes := s.Bytes()
	sig := make([]byte, 64)
	copy(sig[32-len(rBytes):32], rBytes)
	copy(sig[64-len(sBytes):64], sBytes)

	return hex.EncodeToString(sig), nil
}

func SignBlsMessage(message string) ([]byte, error) {
	keyMgr := utils.GetKeyManager()
	if keyMgr.PrivateKeyECDSA == nil {
		return nil, errors.New("miner private key not initialized in KeyManager")
	}
	return utils.BLSSignWithCache(keyMgr.PrivateKeyECDSA, message)
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
