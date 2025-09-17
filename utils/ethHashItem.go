package utils

import (
	"dex/logs"
	"encoding/hex"
	"github.com/dchest/siphash"
)

// EthHashItem表示一个32字节的哈希值，对应以太坊txhash(0x+64 hex)
type EthHashItem [32]byte

// XOR 实现Symbol接口的异或操作，对应group运算
func (t EthHashItem) XOR(t2 EthHashItem) EthHashItem {
	var res EthHashItem
	for i := 0; i < 32; i++ {
		res[i] = t[i] ^ t2[i]
	}
	return res
}

// Hash 使用SipHash对32字节的数据进行哈希
func (t EthHashItem) Hash() uint64 {
	return siphash.Hash(0x12345678, 0x87654321, t[:])
}

// 将EthHashItem转换为以太坊txhash格式字符串 (0x+64 hex)
func (t EthHashItem) String() string {
	return "0x" + hex.EncodeToString(t[:])
}

// convertTxIdToEthItem: 把字符串形式的txId转换为 EthHashItem(32字节)
func ConvertTxIdToEthItem(txID string) (EthHashItem, bool) {
	var res EthHashItem
	decoded, err := hex.DecodeString(stripHexPrefix(txID)) // 根据你实际TxId是否含 0x 前缀处理
	if err != nil {
		logs.Error("hex.DecodeString failed")
		return res, false
	}
	if len(decoded) != 32 {
		// txId 可能不是固定32字节，这里只是示例
		return res, false
	}
	copy(res[:], decoded)
	return res, true
}
func stripHexPrefix(s string) string {
	if len(s) > 2 && (s[:2] == "0x" || s[:2] == "0X") {
		return s[2:]
	}
	return s
}
