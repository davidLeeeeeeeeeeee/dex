// vm/frost_funds_ledger.go
// Frost 资金账本 helper（lot FIFO head/seq 操作）
package vm

import (
	"dex/keys"
	"strconv"
)

// FundsLotHead Vault 的 FIFO 头指针
type FundsLotHead struct {
	Chain   string
	Asset   string
	VaultID uint32
	Height  uint64
	Seq     uint64
}

// GetFundsLotHead 获取指定 Vault 的 FIFO 头指针
// 返回头指针位置（height, seq），如果不存在则返回 (0, 0)
func GetFundsLotHead(sv StateView, chain, asset string, vaultID uint32) *FundsLotHead {
	headKey := keys.KeyFrostFundsLotHead(chain, asset, vaultID)
	data, exists, err := sv.Get(headKey)
	if err != nil || !exists || len(data) == 0 {
		// 如果头指针不存在，返回默认值
		return &FundsLotHead{
			Chain:   chain,
			Asset:   asset,
			VaultID: vaultID,
			Height:  0,
			Seq:     0,
		}
	}

	// 头指针格式：height|seq
	height, seq := parseFundsLotHeadValue(string(data))
	return &FundsLotHead{
		Chain:   chain,
		Asset:   asset,
		VaultID: vaultID,
		Height:  height,
		Seq:     seq,
	}
}

// SetFundsLotHead 设置指定 Vault 的 FIFO 头指针
func SetFundsLotHead(sv StateView, head *FundsLotHead) {
	headKey := keys.KeyFrostFundsLotHead(head.Chain, head.Asset, head.VaultID)
	value := formatFundsLotHeadValue(head.Height, head.Seq)
	sv.Set(headKey, []byte(value))
}

// AdvanceFundsLotHead 推进 FIFO 头指针到下一个位置
// 消费当前 lot 后调用，返回新的头指针
func AdvanceFundsLotHead(sv StateView, chain, asset string, vaultID uint32) *FundsLotHead {
	head := GetFundsLotHead(sv, chain, asset, vaultID)

	// 获取当前高度的最大 seq
	maxSeq := GetFundsLotSeq(sv, chain, asset, vaultID, head.Height)

	if head.Seq < maxSeq {
		// 同高度还有下一个 lot
		head.Seq++
	} else {
		// 需要移动到下一个高度
		// 查找下一个有 lot 的高度
		nextHeight := head.Height + 1
		for i := 0; i < 1000; i++ { // 最多查找 1000 个高度
			nextSeq := GetFundsLotSeq(sv, chain, asset, vaultID, nextHeight)
			if nextSeq > 0 {
				head.Height = nextHeight
				head.Seq = 1 // seq 从 1 开始
				break
			}
			nextHeight++
		}
	}

	SetFundsLotHead(sv, head)
	return head
}

// GetFundsLotSeq 获取指定高度的 lot 序号（最大已使用的 seq）
func GetFundsLotSeq(sv StateView, chain, asset string, vaultID uint32, height uint64) uint64 {
	seqKey := keys.KeyFrostFundsLotSeq(chain, asset, vaultID, height)
	data, exists, err := sv.Get(seqKey)
	if err != nil || !exists || len(data) == 0 {
		return 0
	}
	n, err := strconv.ParseUint(string(data), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// IncrFundsLotSeq 递增并返回新的 lot 序号
func IncrFundsLotSeq(sv StateView, chain, asset string, vaultID uint32, height uint64) uint64 {
	seqKey := keys.KeyFrostFundsLotSeq(chain, asset, vaultID, height)
	currentSeq := GetFundsLotSeq(sv, chain, asset, vaultID, height)
	newSeq := currentSeq + 1
	sv.Set(seqKey, []byte(strconv.FormatUint(newSeq, 10)))
	return newSeq
}

// GetFundsLotAtHead 获取 FIFO 头部的 lot 信息（request_id）
func GetFundsLotAtHead(sv StateView, chain, asset string, vaultID uint32) (string, bool) {
	head := GetFundsLotHead(sv, chain, asset, vaultID)
	if head.Height == 0 && head.Seq == 0 {
		// 头指针未初始化，尝试从 (0, 1) 或 (1, 1) 开始
		// 先检查是否有任何 lot
		for h := uint64(1); h <= 100; h++ {
			if GetFundsLotSeq(sv, chain, asset, vaultID, h) > 0 {
				head.Height = h
				head.Seq = 1
				SetFundsLotHead(sv, head)
				break
			}
		}
	}

	if head.Height == 0 {
		return "", false
	}

	indexKey := keys.KeyFrostFundsLotIndex(chain, asset, vaultID, head.Height, head.Seq)
	data, exists, err := sv.Get(indexKey)
	if err != nil || !exists || len(data) == 0 {
		return "", false
	}

	return string(data), true
}

// ConsumeFundsLot 消费 FIFO 头部的 lot
// 返回被消费的 request_id 和是否成功
func ConsumeFundsLot(sv StateView, chain, asset string, vaultID uint32) (string, bool) {
	requestID, ok := GetFundsLotAtHead(sv, chain, asset, vaultID)
	if !ok {
		return "", false
	}

	// 推进头指针
	AdvanceFundsLotHead(sv, chain, asset, vaultID)
	return requestID, true
}

// formatFundsLotHeadValue 格式化头指针值
func formatFundsLotHeadValue(height, seq uint64) string {
	return strconv.FormatUint(height, 10) + "|" + strconv.FormatUint(seq, 10)
}

// parseFundsLotHeadValue 解析头指针值
func parseFundsLotHeadValue(value string) (height, seq uint64) {
	for i := 0; i < len(value); i++ {
		if value[i] == '|' {
			height, _ = strconv.ParseUint(value[:i], 10, 64)
			seq, _ = strconv.ParseUint(value[i+1:], 10, 64)
			return
		}
	}
	height, _ = strconv.ParseUint(value, 10, 64)
	return height, 0
}
