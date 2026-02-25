package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"dex/pb"
	"dex/utils"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// TestValidator 简单的交易验证器
type TestValidator struct{}

func (v *TestValidator) CheckAnyTx(tx *pb.AnyTx) error {
	if tx == nil {
		return fmt.Errorf("nil transaction")
	}
	base := tx.GetBase()
	if base == nil {
		return fmt.Errorf("missing base message")
	}
	if base.TxId == "" {
		return fmt.Errorf("empty tx id")
	}
	if tx.GetContent() == nil {
		return fmt.Errorf("transaction content is empty (no oneof set)")
	}
	return nil
}

// generatePrivateKeys 生成指定数量的私钥 (确定性生成)，并将私钥+地址保存到 miner_keys.txt
func generatePrivateKeys(count int) []string {
	keys := make([]string, count)
	for i := 0; i < count; i++ {
		hash := sha256.Sum256([]byte(fmt.Sprintf("node_seed_%d", i)))
		keys[i] = hex.EncodeToString(hash[:])
	}
	savePrivateKeysToFile(keys)
	return keys
}

// savePrivateKeysToFile 将私钥和对应 bc1 地址写入 miner_keys.txt
func savePrivateKeysToFile(keys []string) {
	var sb strings.Builder
	for i, k := range keys {
		addr := ""
		if privK, err := utils.ParseSecp256k1PrivateKey(k); err == nil {
			if a, err := utils.DeriveBtcBech32Address(privK); err == nil {
				addr = a
			}
		}
		sb.WriteString(fmt.Sprintf("# index: %d  address: %s\n%s\n", i, addr, k))
	}
	if err := os.WriteFile("miner_keys.txt", []byte(sb.String()), 0600); err != nil {
		fmt.Printf("⚠️  Failed to save miner_keys.txt: %v\n", err)
	} else {
		fmt.Printf("🔑 Saved %d miner keys to miner_keys.txt\n", len(keys))
	}
}

// 生成自签名证书
func generateSelfSignedCert(certFile, keyFile string) error {
	if _, err := os.Stat(certFile); err == nil {
		if _, err := os.Stat(keyFile); err == nil {
			return nil
		}
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(365 * 24 * time.Hour)

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Dex Project"},
		},
		NotBefore: notBefore,
		NotAfter:  notAfter,

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return err
	}

	certOut, err := os.Create(certFile)
	if err != nil {
		return err
	}
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	certOut.Close()

	keyOut, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	privBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return err
	}
	pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})
	keyOut.Close()

	return nil
}

func normalizeSecpPrivKey(key string) (string, error) {
	key = strings.TrimPrefix(key, "0x")
	if !isHexString(key) {
		return "", fmt.Errorf("invalid hex string")
	}
	if len(key) < 64 {
		key = strings.Repeat("0", 64-len(key)) + key
	}
	return key, nil
}

func isHexString(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

func isNotFoundError(err error) bool {
	return err != nil && (err == pebble.ErrNotFound || strings.Contains(err.Error(), "not found"))
}
