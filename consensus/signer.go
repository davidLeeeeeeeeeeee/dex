package consensus

import (
	"crypto/sha256"
	"dex/interfaces"
	"dex/logs"
	"dex/types"
	"dex/utils"
	"encoding/binary"
	"sync"
)

// ============================================
// 全局公钥注册表（用于验签）
// ============================================

var (
	nodePublicKeys   = make(map[string][]byte) // nodeID -> 压缩公钥字节
	nodePublicKeysMu sync.RWMutex
)

// RegisterNodePublicKey 注册节点公钥（节点启动时调用）
func RegisterNodePublicKey(nodeID types.NodeID, pubKeyBytes []byte) {
	nodePublicKeysMu.Lock()
	nodePublicKeys[string(nodeID)] = pubKeyBytes
	nodePublicKeysMu.Unlock()
}

// GetNodePublicKey 获取节点公钥
func GetNodePublicKey(nodeID string) ([]byte, bool) {
	nodePublicKeysMu.RLock()
	pub, ok := nodePublicKeys[nodeID]
	nodePublicKeysMu.RUnlock()
	return pub, ok
}

// ClearNodePublicKeys 清空公钥注册表（仅用于测试）
func ClearNodePublicKeys() {
	nodePublicKeysMu.Lock()
	nodePublicKeys = make(map[string][]byte)
	nodePublicKeysMu.Unlock()
}

// ============================================
// 签名和验签工具函数
// ============================================

// ComputeChitDigest 计算投票签名的待签数据: SHA256(preferred || height || vrf_seed || seq_id)
func ComputeChitDigest(preferred string, height uint64, vrfSeed []byte, seqID uint32) []byte {
	h := sha256.New()
	h.Write([]byte(preferred))
	heightBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(heightBuf, height)
	h.Write(heightBuf)
	h.Write(vrfSeed)
	seqBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(seqBuf, seqID)
	h.Write(seqBuf)
	return h.Sum(nil)
}

// VerifyChitSignature 验证节点的投票签名
// 从公钥注册表中查找 nodeID 对应的公钥，然后调用 utils.VerifyECDSASignature 验证
func VerifyChitSignature(nodeID string, digest []byte, sig []byte) bool {
	pubBytes, ok := GetNodePublicKey(nodeID)
	if !ok {
		logs.Debug("[VerifyChitSignature] No public key registered for node %s", nodeID)
		return false
	}
	return utils.VerifyECDSASignature(pubBytes, digest, sig)
}

// RegisterSignerPublicKey 从 NodeSigner 中提取公钥并注册到公钥注册表
func RegisterSignerPublicKey(nodeID types.NodeID, signer interfaces.NodeSigner) {
	if signer == nil {
		return
	}
	pubBytes := signer.PublicKeyBytes()
	if len(pubBytes) > 0 {
		RegisterNodePublicKey(nodeID, pubBytes)
	}
}
