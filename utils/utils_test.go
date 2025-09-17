package utils_test

import (
	"dex/utils"
	"encoding/hex"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// TestParseSecp256k1PrivateKey 测试 ParseSecp256k1PrivateKey
func TestParseSecp256k1PrivateKey(t *testing.T) {
	t.Run("WIF compressed", func(t *testing.T) {
		// 这是一个随机示例 WIF，可以换成你自己的
		// 以下WIF只做示例，不一定可用
		wifStr := "L4bgJzsnrN8ygWdG3rCFWe1iw46Jpudbzh982po71EB61DXXkzNM"
		priv, err := utils.ParseSecp256k1PrivateKey(wifStr)
		if err != nil {
			t.Fatalf("Failed to parse valid WIF: %v", err)
		}
		if priv == nil {
			t.Fatalf("Expected non-nil private key")
		}
	})

	t.Run("Hex 32 bytes", func(t *testing.T) {
		// 这里随意举例一个32字节的Hex，记得必须是64个hex字符
		hexStr := "af981abb208cf43ddc03afb57cdd92613677528794c94185236df76d77ad860f"
		priv, err := utils.ParseSecp256k1PrivateKey(hexStr)
		if err != nil {
			t.Fatalf("Failed to parse valid hex: %v", err)
		}
		// 校验一下长度是否为32字节
		if len(priv.Serialize()) != 32 {
			t.Errorf("Private key length mismatch, want=32 got=%d", len(priv.Serialize()))
		}
	})

	t.Run("Invalid input", func(t *testing.T) {
		// 无效字符串，不是WIF也不是Hex
		invalid := "thisIsNotWIFNorHex"
		priv, err := utils.ParseSecp256k1PrivateKey(invalid)
		if err == nil {
			t.Fatalf("Expected error for invalid key, got nil")
		}
		if priv != nil {
			t.Fatalf("Expected nil privKey on error, got non-nil")
		}
	})
}

// TestDeriveEthereumAddress 测试 DeriveEthereumAddress
func TestDeriveEthereumAddress(t *testing.T) {
	// 准备一个确定的私钥，验证衍生结果稳定
	// 下面用Hex举例
	hexPriv := "af981abb208cf43ddc03afb57cdd92613677528794c94185236df76d77ad860f"

	raw, _ := hex.DecodeString(hexPriv)
	privKey := secp256k1.PrivKeyFromBytes(raw)

	ethAddr := utils.DeriveEthereumAddress(privKey)
	t.Logf("Derived Ethereum address: %s", ethAddr)

	// 基础检查：是否以 0x 开头 & 长度（0x + 40 hex = 42 chars）
	if len(ethAddr) != 42 || ethAddr[:2] != "0x" {
		t.Errorf("Ethereum address format error, got=%s", ethAddr)
	}
}

// TestDeriveBtcBech32Address 测试 DeriveBtcBech32Address
func TestDeriveBtcBech32Address(t *testing.T) {
	// 生成随机私钥测试一下
	privKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}

	btcAddr, err := utils.DeriveBtcBech32Address(privKey)
	if err != nil {
		t.Fatalf("DeriveBtcBech32Address error: %v", err)
	}

	t.Logf("Derived BTC bech32 address: %s", btcAddr)

	// 简单校验：是否以 bc1 开头
	if len(btcAddr) < 3 || btcAddr[:3] != "bc1" {
		t.Errorf("Expected bc1 prefix, got=%s", btcAddr)
	}
}
func TestParseWIFAndGenerateBtcBech32Address(t *testing.T) {
	// 这是一个有效的WIF示例（主网，压缩公钥），仅供测试演示
	// 你可替换成自己项目里使用的WIF
	wifStr := "L14j65C9VW5g2PEmisQyoHkhXHoAJCnjVY1LkNrK3PHbDwqK62Es"

	// 1) 先用 ParseSecp256k1PrivateKey 解析 WIF
	privKey, err := utils.ParseSecp256k1PrivateKey(wifStr)
	if err != nil {
		t.Fatalf("ParseSecp256k1PrivateKey(WIF) failed: %v", err)
	}
	if privKey == nil {
		t.Fatalf("Expected non-nil private key, got nil")
	}

	// 2) 生成 bc1 地址
	bc1Addr, err := utils.DeriveBtcTaprootAddress(privKey)
	if err != nil {
		t.Fatalf("DeriveBtcBech32Address error: %v", err)
	}

	t.Logf("Derived bc1 address from WIF: %s", bc1Addr)

	// 3) 基本检查: 是否以 bc1 开头
	if len(bc1Addr) < 3 || bc1Addr[:3] != "bc1" {
		t.Errorf("Expected bc1 address, got=%s", bc1Addr)
	}
}
