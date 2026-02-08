package utils

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"dex/logs"
	"encoding/hex" // 使用标准库
	"math/big"
	"sync"
)

// KeyManager 用于保存单个矿机节点的私钥和地址
type KeyManager struct {
	privateKey      string            // 私钥（hex或WIF等字符串）
	address         string            // 由私钥推导出的地址
	PrivateKeyECDSA *ecdsa.PrivateKey // ECDSA私钥
	PublicKeyECDSA  *ecdsa.PublicKey  // ECDSA公钥
}

// 单例相关（向后兼容）
var (
	keyManagerInstance *KeyManager
	keyManagerOnce     sync.Once
)

// GetKeyManager 获取全局唯一的 KeyManager 实例（向后兼容）
func GetKeyManager() *KeyManager {
	keyManagerOnce.Do(func() {
		keyManagerInstance = &KeyManager{}
	})
	return keyManagerInstance
}

// NewKeyManager 创建一个新的 KeyManager 实例（非单例，每个节点独立持有）
func NewKeyManager() *KeyManager {
	return &KeyManager{}
}

func (km *KeyManager) InitKey(priKey string) error {
	// 尝试解析私钥（支持WIF或32字节hex），返回类型为 *ecdsa.privateKey
	priv, err := ParseECDSAPrivateKey(priKey)
	if err != nil {
		return err
	}

	// 同时初始化ECDSA私钥和公钥成员变量
	km.PrivateKeyECDSA = priv
	km.PublicKeyECDSA = &priv.PublicKey

	// 保存原始私钥字符串
	km.privateKey = priKey

	// 推导地址，这里以比特币Bech32地址为例
	privb, err := ParseSecp256k1PrivateKey(priKey)
	if err != nil {
		return err
	}
	addr, err := DeriveBtcBech32Address(privb)
	if err != nil {
		return err
	}
	km.address = addr

	logs.Debug("[KeyManager] InitKey success. Address=%s\n", km.address)
	return nil
}

// InitKeyRandom 随机生成 ECDSA 密钥对（用于模拟节点）
func (km *KeyManager) InitKeyRandom() error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	km.PrivateKeyECDSA = priv
	km.PublicKeyECDSA = &priv.PublicKey
	return nil
}

// GetPrivateKey 返回当前节点的私钥字符串
func (km *KeyManager) GetPrivateKey() string {
	return km.privateKey
}
func (km *KeyManager) GetPublicKey() string {
	return PublicKeyToPEM(km.PublicKeyECDSA)
}

// GetAddress 返回当前节点的推导地址
func (km *KeyManager) GetAddress() string {
	return km.address
}

// ============================================
// NodeSigner 接口实现（ECDSA 签名 / 验签）
// ============================================

// Sign 对 digest 进行 ECDSA 签名，返回 r||s（各 32 字节，共 64 字节）
func (km *KeyManager) Sign(digest []byte) ([]byte, error) {
	r, s, err := ecdsa.Sign(rand.Reader, km.PrivateKeyECDSA, digest)
	if err != nil {
		return nil, err
	}
	// 固定长度编码: r(32 bytes) || s(32 bytes)
	rBytes := r.Bytes()
	sBytes := s.Bytes()
	sig := make([]byte, 64)
	copy(sig[32-len(rBytes):32], rBytes)
	copy(sig[64-len(sBytes):64], sBytes)
	return sig, nil
}

// PublicKeyBytes 返回压缩公钥字节（33 字节）
func (km *KeyManager) PublicKeyBytes() []byte {
	if km.PublicKeyECDSA == nil {
		return nil
	}
	return elliptic.MarshalCompressed(km.PublicKeyECDSA.Curve, km.PublicKeyECDSA.X, km.PublicKeyECDSA.Y)
}

// VerifyECDSASignature 验证 ECDSA 签名（r||s 各 32 字节，共 64 字节）
// pubKeyBytes 为压缩公钥字节
func VerifyECDSASignature(pubKeyBytes []byte, digest []byte, sig []byte) bool {
	if len(sig) != 64 {
		return false
	}

	// 解析压缩公钥
	x, y := elliptic.UnmarshalCompressed(elliptic.P256(), pubKeyBytes)
	if x == nil {
		// 尝试非压缩格式
		x, y = elliptic.Unmarshal(elliptic.P256(), pubKeyBytes)
		if x == nil {
			return false
		}
	}

	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])

	return ecdsa.Verify(&ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     x,
		Y:     y,
	}, digest, r, s)
}

// SignMessage 对消息进行 ECDSA 签名（SHA256 哈希后签名）
// 返回 hex 编码的签名字符串（r||s 各 32 字节 = 128 hex 字符）
func (km *KeyManager) SignMessage(message string) (string, error) {
	hash := sha256.Sum256([]byte(message))
	sig, err := km.Sign(hash[:])
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sig), nil
}
