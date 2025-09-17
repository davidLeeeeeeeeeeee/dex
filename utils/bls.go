package utils

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"dex/logs"
	"encoding/hex"
	"go.dedis.ch/kyber/v3"
	"sync"

	"go.dedis.ch/kyber/v3/pairing/bn256"
	"go.dedis.ch/kyber/v3/sign/bls"
)

const maxCacheSize = 100

// 定义全局缓存：
// blsSignCache 存储签名数据，cacheKeys 记录键的插入顺序，用于实现简单的 FIFO 驱逐策略。
var (
	blsSignCache = make(map[string][]byte)
	cacheKeys    = make([]string, 0, maxCacheSize)
	cacheMutex   sync.Mutex
)

// BLSSignWithCache 用于对给定消息进行 BLS 签名，并缓存结果。
// 如果同一私钥对同一消息已经签名，则直接返回缓存的签名结果，避免重复计算。
// 入参：
//   - priv: *ecdsa.PrivateKey，用于签名的私钥。
//   - msg: 需要签名的消息字符串。
//
// 返回签名结果（字节数组）或错误。
func BLSSignWithCache(priv *ecdsa.PrivateKey, msg string) ([]byte, error) {
	// 生成唯一的缓存 key：使用私钥的 D 值（转换为十六进制字符串）和消息拼接
	keyID := hex.EncodeToString(priv.D.Bytes())
	cacheKey := keyID + "_" + msg

	// 尝试从缓存中查找签名结果
	cacheMutex.Lock()
	if sig, exists := blsSignCache[cacheKey]; exists {
		cacheMutex.Unlock()
		return sig, nil
	}
	cacheMutex.Unlock()

	// 使用 bn256 曲线的 BLS suite 进行签名
	suite := bn256.NewSuite()

	// 将 ecdsa.PrivateKey 转换为 kyber.Scalar
	// 注意：这里假设私钥的 D 值能直接作为 BLS 签名所用的私钥，
	// 实际使用时需确保不同算法之间的兼容性。
	privScalar, err := GetBLSPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	// 执行 BLS 签名，bls.Sign 内部会使用 hash-to-scalar 方法
	signature, err := bls.Sign(suite, privScalar, []byte(msg))
	if err != nil {
		return nil, err
	}

	// 将签名结果缓存
	cacheMutex.Lock()
	// 如果缓存已满，则删除最早的缓存项（FIFO 驱逐）
	if len(blsSignCache) >= maxCacheSize {
		oldestKey := cacheKeys[0]
		cacheKeys = cacheKeys[1:]
		delete(blsSignCache, oldestKey)
	}
	// 缓存新签名，并记录 key 的插入顺序
	blsSignCache[cacheKey] = signature
	cacheKeys = append(cacheKeys, cacheKey)
	cacheMutex.Unlock()

	return signature, nil
}

// GetBLSPrivateKey 通过ECDSA私钥生成BLS私钥
func GetBLSPrivateKey(priv *ecdsa.PrivateKey) (kyber.Scalar, error) {

	// 对ECDSA私钥的D值进行哈希，生成BLS私钥
	hash := sha256.Sum256(priv.D.Bytes())
	suite := bn256.NewSuite()
	blsPrivateKey := suite.G2().Scalar().SetBytes(hash[:])

	return blsPrivateKey, nil
}

// BLSVerifySignature 验证给定消息和签名是否与提供的 BLS 公钥匹配。
// 入参：
//   - pub: BLS 公钥（通过 GetBLSPublicKey 获得）
//   - msg: 被签名的消息字符串
//   - signature: 签名数据
//
// 返回值：
//   - error: 校验成功时返回 nil，否则返回错误信息
func BLSVerifySignature(pub kyber.Point, msg string, signature []byte) error {
	suite := bn256.NewSuite()
	// bls.Verify 内部会使用 hash-to-scalar 方法进行签名验证
	return bls.Verify(suite, pub, []byte(msg), signature)
}

// GetBLSPublicKey 获取与BLS私钥对应的BLS公钥
func GetBLSPublicKey(priv *ecdsa.PrivateKey) (kyber.Point, error) {
	blsPrivateKey, err := GetBLSPrivateKey(priv)
	if err != nil {
		return nil, err
	}

	suite := bn256.NewSuite()
	blsPublicKey := suite.G2().Point().Mul(blsPrivateKey, nil)

	return blsPublicKey, nil
}

// utils/bls_aggregate.go
func AggregateBLS(sigs [][]byte) ([]byte, error) {
	// 1. 使用 bn256 曲线的 BLS suite
	suite := bn256.NewSuite()
	aggSig, err := bls.AggregateSignatures(suite, sigs...)
	if err != nil {
		logs.Error("failed to aggregate signatures: %v", err)
	}
	return aggSig, nil // 示例返回值
}
