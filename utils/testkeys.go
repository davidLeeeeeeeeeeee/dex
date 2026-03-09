package utils

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// TestIdentity 包含一个测试用身份的全部信息
type TestIdentity struct {
	PrivateKey    *secp256k1.PrivateKey
	PrivateKeyHex string // 32 字节 hex
	PublicKey     []byte // 33 字节压缩公钥
	Address       string // bc1q... bech32 地址
}

// GenerateTestIdentity 从种子 index 确定性地生成一个 secp256k1 测试身份（私钥 + 地址 + 公钥）
func GenerateTestIdentity(seed int) *TestIdentity {
	// 确定性生成 32 字节私钥种子
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(seed))
	hash := sha256.Sum256(append([]byte("testkey_seed_"), buf[:]...))

	priv := secp256k1.PrivKeyFromBytes(hash[:])
	addr, _ := DeriveBtcBech32Address(priv)

	return &TestIdentity{
		PrivateKey:    priv,
		PrivateKeyHex: hex.EncodeToString(hash[:]),
		PublicKey:     priv.PubKey().SerializeCompressed(),
		Address:       addr,
	}
}

// GenerateTestIdentities 批量生成 n 个测试身份
func GenerateTestIdentities(n int) []*TestIdentity {
	ids := make([]*TestIdentity, n)
	for i := range ids {
		ids[i] = GenerateTestIdentity(i)
	}
	return ids
}

// RandomHex 生成 n 字节的随机 hex 字符串
func RandomHex(n int) string {
	b := make([]byte, n)
	// 使用 crypto/rand 读取随机字节
	// 忽略错误（测试 / mock 专用）
	rand.Read(b)
	return hex.EncodeToString(b)
}

// RandomAddress 生成一个随机的 0x... 地址（20 字节）
func RandomAddress() string { return "0x" + RandomHex(20) }
