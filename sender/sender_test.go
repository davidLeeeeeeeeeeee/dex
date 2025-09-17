package sender

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"log"
	mrand "math/rand"
	"os"
	"testing"
	"time"
)

// TxData 用于存储单条交易信息
type TxData struct {
	Message      string `json:"message"`
	PublicKeyPEM string `json:"public_key_pem"`
	SignatureHex string `json:"signature_hex"`
}

// TestGenerateTxData 用于生成 10,000 条随机交易数据并写入文件
func TestGenerateTxData(t *testing.T) {
	t.Parallel()

	const total = 10000
	outFile := "tx_data.jsonl"

	file, err := os.Create(outFile)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}
	defer file.Close()

	// 使用固定随机种子, 实际可变动
	mrand.Seed(time.Now().UnixNano())

	for i := 0; i < total; i++ {
		// 生成随机消息
		msg := randomMessage(64) // 长度64的随机字符串

		// 生成新的公私钥对
		privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("Failed to generate key: %v", err)
		}
		pubKey := &privKey.PublicKey

		// 对消息进行签名
		sig, err := SignMessage(privKey, msg)
		if err != nil {
			t.Fatalf("Failed to sign message: %v", err)
		}

		// 公钥转 PEM
		pubPEM := PublicKeyToPEM(pubKey)

		data := TxData{
			Message:      msg,
			PublicKeyPEM: pubPEM,
			SignatureHex: sig,
		}

		line, err := json.Marshal(data)
		if err != nil {
			t.Fatalf("Failed to marshal JSON: %v", err)
		}
		_, err = file.Write(append(line, '\n'))
		if err != nil {
			t.Fatalf("Failed to write line: %v", err)
		}
	}
	log.Printf("Successfully wrote %d tx data lines to %s\n", total, outFile)
}

func randomMessage(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[mrand.Intn(len(charset))]
	}
	return string(b)
}
