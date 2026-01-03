package frost

import (
	"crypto/sha256"
	"math/big"
	"testing"

	"dex/frost/core/curve"
	"dex/frost/core/dkg"

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
