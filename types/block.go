package types

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// BlockHeader contains the metadata that uniquely defines a block
type BlockHeader struct {
	Height       uint64 // Block height
	ParentID     string // Parent block hash
	Timestamp    int64  // Unix timestamp
	StateRoot    string // JMT root hash after executing this block
	TxHash       string // Cumulative hash of all transactions (PoH style)
	Proposer     string // Proposer address/ID
	Window       int    // Time window
	VRFProof     []byte // VRF proof
	VRFOutput    []byte // VRF output
	BLSPublicKey []byte // Proposer's BLS public key
}

// ComputeID generates a deterministic block ID from header fields
// Format: block-<height>-<proposer>-w<window>-<hash8>
func (h *BlockHeader) ComputeID() string {
	// 使用 TxHash 的前 8 个字符作为区块标识的一部分
	hashPart := h.TxHash
	if len(hashPart) > 8 {
		hashPart = hashPart[:8]
	}
	if hashPart == "" {
		// 如果没有交易，使用 StateRoot 或时间戳生成哈希
		data := fmt.Sprintf("%d-%s-%d-%d", h.Height, h.ParentID, h.Timestamp, h.Window)
		hash := sha256.Sum256([]byte(data))
		hashPart = hex.EncodeToString(hash[:4])
	}
	return fmt.Sprintf("block-%d-%s-w%d-%s", h.Height, h.Proposer, h.Window, hashPart)
}

// Block represents a complete block with header and body
type Block struct {
	ID           string      // Computed from header (for backward compatibility)
	Header       BlockHeader // Metadata
	Transactions []string    // Transaction IDs (Body)
	Signatures   [][]byte    // Consensus signatures on Header (future use)
}

// FinalizationChit 单个节点的投票信息
type FinalizationChit struct {
	NodeID      string `json:"node_id"`      // 投票节点 ID
	PreferredID string `json:"preferred_id"` // 节点偏好的区块 ID
	Timestamp   int64  `json:"timestamp"`    // 投票时间戳 (毫秒)
}

// FinalizationChits 区块最终化时的投票汇总
type FinalizationChits struct {
	BlockID     string             `json:"block_id"`     // 最终化的区块 ID
	Height      uint64             `json:"height"`       // 区块高度
	TotalVotes  int                `json:"total_votes"`  // 总投票数
	Chits       []FinalizationChit `json:"chits"`        // 投票详情列表
	FinalizedAt int64              `json:"finalized_at"` // 最终化时间戳 (毫秒)
}

// BlockFinalizedData 区块最终化事件数据包装
type BlockFinalizedData struct {
	Block *Block             `json:"block"`
	Chits *FinalizationChits `json:"chits,omitempty"`
}

// Convenience methods for backward compatibility
func (b *Block) GetHeight() uint64    { return b.Header.Height }
func (b *Block) GetParentID() string  { return b.Header.ParentID }
func (b *Block) GetProposer() string  { return b.Header.Proposer }
func (b *Block) GetWindow() int       { return b.Header.Window }
func (b *Block) GetStateRoot() string { return b.Header.StateRoot }
func (b *Block) GetTxHash() string    { return b.Header.TxHash }
