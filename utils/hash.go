// 文件路径：internal/miner/hash.go
package utils

import (
	"crypto/sha256"
	"dex/logs"

	"github.com/spaolacci/murmur3"
)

// MurmurHash 使用Murmur3哈希算法
func MurmurHash(data []byte) []byte {
	h := murmur3.New64()
	_, err := h.Write(data)
	if err != nil {
		logs.Verbose("hash error: %v", err)
	}
	sum64 := h.Sum64()
	b := make([]byte, 8)
	for i := 0; i < 8; i++ {
		b[i] = byte(sum64 >> (8 * i))
	}
	return b
}
func Sha256Hash(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}
