package types

import (
	"encoding/json"
	"fmt"
)

// 区块定义
type Block struct {
	ID       string
	Height   uint64
	ParentID string
	Data     string
	Proposer string
	Round    int
}

func (b *Block) String() string {
	return fmt.Sprintf("Block{ID:%s, Height:%d, Parent:%s, Proposer:%d, Round:%d}",
		b.ID, b.Height, b.ParentID, b.Proposer, b.Round)
}
func (b *Block) ToMsg() ([]byte, error) {
	return json.Marshal(b)
}
