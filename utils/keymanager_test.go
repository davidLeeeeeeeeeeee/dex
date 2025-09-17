package utils

import (
	"math/big"
	"testing"
)

// TestKeyManager_InitKey_ECDSA 测试 InitKey 函数是否正确初始化了 ECDSA 私钥和公钥
func TestKeyManager_InitKey_ECDSA(t *testing.T) {
	// 获取全局唯一的 KeyManager 实例
	km := GetKeyManager()

	// 使用一个合法的 32 字节 hex 私钥（请确保 ParseECDSAPrivateKey、ParseSecp256k1PrivateKey 能正确解析此格式）
	validKey := "a61a8cb51bb55a9bed37b59a94f7f6ee92b04d211179c84a1147fd30bd8c5192"

	// 调用初始化方法
	err := km.InitKey(validKey)
	if err != nil {
		t.Fatalf("InitKey 返回错误: %v", err)
	}

	// 检查 ECDSA 私钥是否被正确赋值
	if km.PrivateKeyECDSA == nil {
		t.Fatal("PrivateKeyECDSA 为空")
	}

	// 检查 ECDSA 公钥是否被正确赋值
	if km.PublicKeyECDSA == nil {
		t.Fatal("PublicKeyECDSA 为空")
	}

	// 验证：PrivateKeyECDSA 内置的公钥应与 PublicKeyECDSA 一致
	privPub := km.PrivateKeyECDSA.PublicKey
	if !bigIntEqual(privPub.X, km.PublicKeyECDSA.X) || !bigIntEqual(privPub.Y, km.PublicKeyECDSA.Y) {
		t.Error("PrivateKeyECDSA 内置的公钥与 PublicKeyECDSA 不匹配")
	}

	// 验证存储的原始私钥字符串是否与传入的一致
	if km.GetPrivateKey() != validKey {
		t.Errorf("GetPrivateKey() 返回 %s，与预期 %s 不符", km.GetPrivateKey(), validKey)
	}

	// 检查推导的地址是否不为空
	if km.GetAddress() == "" {
		t.Error("推导的地址为空")
	}
}

// bigIntEqual 比较两个 *big.Int 是否相等
func bigIntEqual(a, b *big.Int) bool {
	return a.Cmp(b) == 0
}
