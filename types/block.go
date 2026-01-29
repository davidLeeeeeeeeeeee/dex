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

// Convenience methods for backward compatibility
func (b *Block) GetHeight() uint64    { return b.Header.Height }
func (b *Block) GetParentID() string  { return b.Header.ParentID }
func (b *Block) GetProposer() string  { return b.Header.Proposer }
func (b *Block) GetWindow() int       { return b.Header.Window }
func (b *Block) GetStateRoot() string { return b.Header.StateRoot }
func (b *Block) GetTxHash() string    { return b.Header.TxHash }
