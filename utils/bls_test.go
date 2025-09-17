package utils

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"
)

// TestBLSSignWithCache 专门测试 BLSSignWithCache 函数
func TestBLSSignWithCache(t *testing.T) {
	// 清空全局缓存，确保测试环境干净
	cacheMutex.Lock()
	blsSignCache = make(map[string][]byte)
	cacheKeys = make([]string, 0, maxCacheSize)
	cacheMutex.Unlock()

	// 生成一个有效的 ECDSA 私钥（这里使用 elliptic.P256()）
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey 失败: %v", err)
	}

	// 1. 对同一私钥和消息签名，签名结果不为空
	message := "test message"
	sig1, err := BLSSignWithCache(priv, message)
	if err != nil {
		t.Fatalf("BLSSignWithCache 返回错误: %v", err)
	}
	if len(sig1) == 0 {
		t.Fatal("BLSSignWithCache 返回了空的签名")
	}

	// 2. 再次对相同私钥和消息签名，预期直接命中缓存，结果与第一次签名完全相同
	sig2, err := BLSSignWithCache(priv, message)
	if err != nil {
		t.Fatalf("BLSSignWithCache 第二次调用返回错误: %v", err)
	}
	if !bytes.Equal(sig1, sig2) {
		t.Error("对于相同私钥和消息，多次签名返回的结果不一致，缓存机制可能存在问题")
	}

	// 3. 对不同消息签名，返回的签名应当不同
	anotherMessage := "another message"
	sig3, err := BLSSignWithCache(priv, anotherMessage)
	if err != nil {
		t.Fatalf("BLSSignWithCache 对不同消息签名返回错误: %v", err)
	}
	if bytes.Equal(sig1, sig3) {
		t.Error("不同消息的签名结果不应相同")
	}
}
func TestBLSVerifySignature(t *testing.T) {
	// 1. 生成 ECDSA 私钥
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ECDSA key: %v", err)
	}

	message := "test message"

	// 2. 使用 BLSSignWithCache 签名
	sig, err := BLSSignWithCache(priv, message)
	if err != nil {
		t.Fatalf("BLSSignWithCache returned error: %v", err)
	}
	if len(sig) == 0 {
		t.Fatal("BLSSignWithCache returned empty signature")
	}

	// 3. 获取对应的 BLS 公钥
	pub, err := GetBLSPublicKey(priv)
	if err != nil {
		t.Fatalf("GetBLSPublicKey returned error: %v", err)
	}

	// 正确用例：验证应当成功
	if err := BLSVerifySignature(pub, message, sig); err != nil {
		t.Errorf("valid signature verification failed: %v", err)
	}

	// 错误用例1：消息被篡改，验证应当失败
	wrongMsg := "wrong message"
	if err := BLSVerifySignature(pub, wrongMsg, sig); err == nil {
		t.Error("verification succeeded for wrong message; expected failure")
	}

	// 错误用例2：签名被篡改，验证应当失败
	sigTampered := append([]byte{}, sig...)
	if len(sigTampered) > 0 {
		sigTampered[0] ^= 0xFF
	}
	if err := BLSVerifySignature(pub, message, sigTampered); err == nil {
		t.Error("verification succeeded for tampered signature; expected failure")
	}
}
