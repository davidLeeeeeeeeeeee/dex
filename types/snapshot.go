package types

import (
	"time"
)

type Snapshot struct {
	Height             uint64            `json:"height"`
	Timestamp          time.Time         `json:"timestamp"`
	FinalizedBlocks    map[uint64]*Block `json:"finalized_blocks"`
	LastAcceptedID     string            `json:"last_accepted_id"`
	LastAcceptedHeight uint64            `json:"last_accepted_height"`
	BlockHashes        map[string]bool   `json:"block_hashes"` // 用于快速去重
}
