package crt

import (
	"awesomeProject1/logs"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"os"
	"time"

	"github.com/btcsuite/btcd/btcutil/bech32"
	"golang.org/x/crypto/ripemd160"
)

// convertBits 用于将数据从 fromBits 位组转换为 toBits 位组
func convertBits(data []byte, fromBits, toBits uint, pad bool) ([]byte, error) {
	acc := 0
	bits := uint(0)
	maxv := (1 << toBits) - 1
	output := []byte{}

	for _, value := range data {
		acc = (acc << fromBits) | int(value)
		bits += fromBits
		for bits >= toBits {
			bits -= toBits
			output = append(output, byte((acc>>bits)&maxv))
		}
	}

	if pad && bits > 0 {
		output = append(output, byte((acc<<(toBits-bits))&maxv))
	} else if !pad && bits >= fromBits {
		return nil, fmt.Errorf("excess padding")
	} else if !pad && ((acc<<(toBits-bits))&maxv) != 0 {
		return nil, fmt.Errorf("non-zero padding")
	}

	return output, nil
}

// GenerateBitcoinAddress 根据公钥生成一个 P2WPKH SegWit 主网地址 (bc1 开头)
func GenerateBitcoinAddress(pubKey *ecdsa.PublicKey) (string, error) {
	// 公钥序列化（非压缩）
	pubKeyBytes := elliptic.Marshal(pubKey.Curve, pubKey.X, pubKey.Y)

	// SHA-256 哈希
	sha256Hash := sha256.Sum256(pubKeyBytes)

	// RIPEMD-160 哈希
	ripemdHasher := ripemd160.New()
	_, err := ripemdHasher.Write(sha256Hash[:])
	if err != nil {
		return "", err
	}
	ripemdHash := ripemdHasher.Sum(nil)

	if len(ripemdHash) != 20 {
		return "", fmt.Errorf("invalid RIPEMD-160 hash length: %d", len(ripemdHash))
	}

	// 比特币 P2WPKH 地址：version=0，data=hash160(pubkey)
	// version 用一个 5 位组表示（0x00）
	version := byte(0x00) // 0 表示 witness version 0

	// 将 20 字节 hash 转换为 5 位一组的数据
	converted, err := convertBits(ripemdHash, 8, 5, false)
	if err != nil {
		return "", err
	}

	// 将 version（0） 和 转换后的哈希数据拼接
	// data 的第一个元素是 5-bit 的版本号0，其余的是转换后的哈希
	data := append([]byte{version}, converted...)

	// 使用 Bech32 编码生成 bc1 开头的地址
	address, err := bech32.Encode("bc", data)
	if err != nil {
		return "", err
	}

	return address, nil
}

func generateSelfSignedCert(certPath, keyPath string) error {
	// 生成 ECDSA 私钥
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	// 根据公钥生成比特币地址
	bitcoinAddress, err := GenerateBitcoinAddress(&privateKey.PublicKey)
	if err != nil {
		return err
	}

	// 创建证书模板
	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			Organization: []string{bitcoinAddress}, // 将 bc1 地址写入组织字段
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:  x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}

	// 自签名证书
	certBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return err
	}

	// 保存证书
	certFile, err := os.Create(certPath)
	if err != nil {
		return err
	}
	defer certFile.Close()
	if err := pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certBytes}); err != nil {
		return err
	}

	// 保存私钥
	keyFile, err := os.Create(keyPath)
	if err != nil {
		return err
	}
	defer keyFile.Close()
	privBytes, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return err
	}
	if err := pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes}); err != nil {
		return err
	}

	logs.Debug("Certificate and key generated:\nCertificate: %s\nPrivate Key: %s\n", certPath, keyPath)
	logs.Debug("Bitcoin address used in certificate: %s\n", bitcoinAddress)
	return nil
}

func Test() {
	certPath := "server.crt"
	keyPath := "server.key"

	if err := generateSelfSignedCert(certPath, keyPath); err != nil {
		logs.Error("Failed to generate certificate: %v", err)
	}

	log.Println("Certificate generation completed!")
}
