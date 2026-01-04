package frost

import (
	"crypto/sha256"
	"math/big"
	"testing"

	"dex/frost/core/curve"
	"dex/frost/core/dkg"
	"dex/pb"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

func Test_CompareWithBtcecSchnorr(t *testing.T) {
	grp := curve.NewSecp256k1Group()
	// 随机私钥
	privScalar := dkg.RandomScalar(grp.Order())
	privKey, _ := btcec.PrivKeyFromBytes(privScalar.Bytes())

	// 原始消息
	msg := []byte("consistency check")
	// 先做一次 sha256，得到 32 字节
	digest := sha256.Sum256(msg)

	// —— 用内部实现签名 ——
	Rx, Ry, z, _ := SchnorrSign(grp, privScalar, digest[:])

	// —— 用外部库签名 ——
	sig, err := schnorr.Sign(privKey, digest[:])
	if err != nil {
		t.Fatalf("外部 schnorr.Sign 失败: %v", err)
	}
	sigBytes := sig.Serialize() // 64 字节
	if len(sigBytes) != 64 {
		t.Fatalf("外部签名长度不对: got %d, want 64", len(sigBytes))
	}

	// 拆分为 R.x 和 s
	RxExt := new(big.Int).SetBytes(sigBytes[:32])
	sExt := new(big.Int).SetBytes(sigBytes[32:])

	// 打印对比
	t.Logf(`
=== 内部 SchnorrSign ===
  R = (%s, %s)
  z = %s
=== 外部 schnorr.Sign ===
  R.x = %s
  s   = %s
`, Rx.Text(16), Ry.Text(16), z.Text(16), RxExt.Text(16), sExt.Text(16))

	// 自动断言
	if Rx.Cmp(RxExt) != 0 {
		t.Errorf("R.x 不一致：内部 %s，外部 %s", Rx.Text(16), RxExt.Text(16))
	}
	if z.Cmp(sExt) != 0 {
		t.Errorf("s/z 不一致：内部 %s，外部 %s", z.Text(16), sExt.Text(16))
	}
}

// TestBIP340Verify 测试 BIP-340 验签
func TestBIP340Verify(t *testing.T) {
	grp := curve.NewSecp256k1Group()

	// 生成随机私钥
	privScalar := dkg.RandomScalar(grp.Order())
	privKey, _ := btcec.PrivKeyFromBytes(privScalar.Bytes())

	// 获取公钥（x-only, 32 字节）
	pubKey := privKey.PubKey()
	pubKeyBytes := schnorr.SerializePubKey(pubKey)
	if len(pubKeyBytes) != 32 {
		t.Fatalf("公钥长度不对: got %d, want 32", len(pubKeyBytes))
	}

	// 消息（32 字节哈希）
	msg := sha256.Sum256([]byte("test message for BIP-340"))

	// 使用 btcsuite 签名
	sig, err := schnorr.Sign(privKey, msg[:])
	if err != nil {
		t.Fatalf("签名失败: %v", err)
	}
	sigBytes := sig.Serialize() // 64 字节

	// 测试正确签名通过
	t.Run("ValidSignature", func(t *testing.T) {
		valid, err := VerifyBIP340(pubKeyBytes, msg[:], sigBytes)
		if err != nil {
			t.Fatalf("验签返回错误: %v", err)
		}
		if !valid {
			t.Error("正确签名应该通过验证")
		}
	})

	// 测试修改消息后失败
	t.Run("WrongMessage", func(t *testing.T) {
		wrongMsg := sha256.Sum256([]byte("wrong message"))
		valid, err := VerifyBIP340(pubKeyBytes, wrongMsg[:], sigBytes)
		if err != nil {
			t.Fatalf("验签返回错误: %v", err)
		}
		if valid {
			t.Error("修改消息后应该验证失败")
		}
	})

	// 测试修改签名后失败
	t.Run("WrongSignature", func(t *testing.T) {
		wrongSig := make([]byte, 64)
		copy(wrongSig, sigBytes)
		wrongSig[0] ^= 0x01 // 修改一个字节
		valid, err := VerifyBIP340(pubKeyBytes, msg[:], wrongSig)
		if err != nil {
			// 可能返回 ErrInvalidSignature（x 不在曲线上）
			return
		}
		if valid {
			t.Error("修改签名后应该验证失败")
		}
	})

	// 测试错误的公钥长度
	t.Run("InvalidPubkeyLength", func(t *testing.T) {
		_, err := VerifyBIP340([]byte{1, 2, 3}, msg[:], sigBytes)
		if err != ErrInvalidPublicKey {
			t.Errorf("期望 ErrInvalidPublicKey, 得到 %v", err)
		}
	})

	// 测试错误的消息长度
	t.Run("InvalidMessageLength", func(t *testing.T) {
		_, err := VerifyBIP340(pubKeyBytes, []byte("short"), sigBytes)
		if err != ErrInvalidMessage {
			t.Errorf("期望 ErrInvalidMessage, 得到 %v", err)
		}
	})

	// 测试错误的签名长度
	t.Run("InvalidSignatureLength", func(t *testing.T) {
		_, err := VerifyBIP340(pubKeyBytes, msg[:], []byte{1, 2, 3})
		if err != ErrInvalidSignature {
			t.Errorf("期望 ErrInvalidSignature, 得到 %v", err)
		}
	})
}

// TestVerifyAPI 测试统一的 Verify API
func TestVerifyAPI(t *testing.T) {
	grp := curve.NewSecp256k1Group()

	// 生成随机私钥
	privScalar := dkg.RandomScalar(grp.Order())
	privKey, _ := btcec.PrivKeyFromBytes(privScalar.Bytes())

	// 获取公钥（x-only, 32 字节）
	pubKey := privKey.PubKey()
	pubKeyBytes := schnorr.SerializePubKey(pubKey)

	// 消息（32 字节哈希）
	msg := sha256.Sum256([]byte("test message for Verify API"))

	// 使用 btcsuite 签名
	sig, err := schnorr.Sign(privKey, msg[:])
	if err != nil {
		t.Fatalf("签名失败: %v", err)
	}
	sigBytes := sig.Serialize()

	// 测试 BIP340 算法
	t.Run("BIP340", func(t *testing.T) {
		valid, err := Verify(pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340, pubKeyBytes, msg[:], sigBytes)
		if err != nil {
			t.Fatalf("验签返回错误: %v", err)
		}
		if !valid {
			t.Error("正确签名应该通过验证")
		}
	})

	// 测试不支持的算法
	t.Run("UnsupportedAlgo", func(t *testing.T) {
		_, err := Verify(pb.SignAlgo_SIGN_ALGO_UNSPECIFIED, pubKeyBytes, msg[:], sigBytes)
		if err != ErrUnsupportedSignAlgo {
			t.Errorf("期望 ErrUnsupportedSignAlgo, 得到 %v", err)
		}
	})
}
