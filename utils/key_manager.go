package utils

import (
	"crypto/ecdsa"
	"dex/logs"
	"sync"
)

// KeyManager 用于保存单个矿机节点的私钥和地址
type KeyManager struct {
	privateKey      string            // 私钥（hex或WIF等字符串）
	address         string            // 由私钥推导出的地址
	PrivateKeyECDSA *ecdsa.PrivateKey // 新增：ECDSA私钥
	PublicKeyECDSA  *ecdsa.PublicKey  // 新增：ECDSA公钥

}

// 单例相关
var (
	keyManagerInstance *KeyManager
	keyManagerOnce     sync.Once
)

// GetKeyManager 获取全局唯一的 KeyManager 实例
func GetKeyManager() *KeyManager {
	keyManagerOnce.Do(func() {
		keyManagerInstance = &KeyManager{}
	})
	return keyManagerInstance
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
