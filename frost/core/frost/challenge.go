package frost

import (
	"crypto/sha256"
	"math/big"

	"dex/frost/core/curve"

	"golang.org/x/crypto/sha3"
)

// BIP340Challenge 基于 BIP-340 的 tagged hash 实现 ChallengeFunc
func BIP340Challenge(Rx, Px *big.Int, msg []byte, grp curve.Group) *big.Int {
	// 1) 计算 tagHash = SHA256("BIP0340/challenge")
	tagHash := sha256.Sum256([]byte("BIP0340/challenge"))

	// 2) 域分离哈希：SHA256(tagHash || tagHash || Rx || Px || msg)
	h := sha256.New()
	h.Write(tagHash[:])
	h.Write(tagHash[:])

	// pad32 确保 big.Int 始终以 32 字节大端表示
	pad32 := func(b *big.Int) []byte {
		out := make([]byte, 32)
		bb := b.Bytes()
		copy(out[32-len(bb):], bb)
		return out
	}
	h.Write(pad32(Rx))
	h.Write(pad32(Px))
	h.Write(msg) // 这里 msg 应当已经是 32 字节摘要

	// 3) 将哈希结果 mod n，得到 challenge e
	e := new(big.Int).SetBytes(h.Sum(nil))
	e.Mod(e, grp.Order())
	return e
}

// ETHKeccakChallenge 基于 eth_schnorr.go 中的 e = keccak256(Rx||Ry||Px||Py||msg) mod r
// 由于 ThresholdSign 的 ChallengeFunc 只接收 Rx 和 Px，如果需要 Ry/Py
// 也可以扩展签名流程传入 Y 坐标；这里先按最简 Rx||Px||msg 实现。
func ETHKeccakChallenge(Rx, Px *big.Int, msg []byte, grp curve.Group) *big.Int {
	h := sha3.NewLegacyKeccak256()

	// pad32 同 eth_schnorr.go 中的 uintPad / putBigBuf 保持一致
	pad32 := func(n *big.Int) []byte {
		out := make([]byte, 32)
		b := n.Bytes()
		copy(out[32-len(b):], b)
		return out
	}

	h.Write(pad32(Rx)) // R.x
	h.Write(pad32(Px)) // P.x
	h.Write(msg)       // message（需要是 keccak256 之前的原始 bytes）

	eBytes := h.Sum(nil)
	e := new(big.Int).SetBytes(eBytes)
	e.Mod(e, grp.Order())
	return e
}
