package types

// 区块定义
type Block struct {
	ID       string
	Height   uint64
	ParentID string
	Data     string
	Proposer string
	Round    int
}
