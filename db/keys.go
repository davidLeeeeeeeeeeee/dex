package db

import "fmt"

// Keys 定义所有数据库key前缀（集中管理）
var Keys = struct {
	// 交易相关
	AnyTx        string
	PendingAnyTx string
	Transaction  string
	PendingTx    string
	OrderTx      string
	MinerTx      string

	// 账户相关
	Account        string
	StakeIndex     string
	IndexToAccount string
	FreeIndex      string

	// 区块相关
	BlockData    string
	HeightBlocks string
	BlockID      string
	LatestHeight string

	// 节点相关
	Node       string
	ClientInfo string

	// 元数据
	MaxIndex string
}{
	// 交易相关
	AnyTx:        "anyTx",
	PendingAnyTx: "pending_anytx",
	Transaction:  "tx",
	PendingTx:    "pending_tx",
	OrderTx:      "order",
	MinerTx:      "minerTx",

	// 账户相关
	Account:        "account",
	StakeIndex:     "stakeIndex",
	IndexToAccount: "indexToAccount",
	FreeIndex:      "free_idx",

	// 区块相关
	BlockData:    "blockdata",
	HeightBlocks: "height",
	BlockID:      "blockid",
	LatestHeight: "latest_block_height",

	// 节点相关
	Node:       "node",
	ClientInfo: "clientinfo",

	// 元数据
	MaxIndex: "meta:max_index",
}

// 构建带分隔符的key
func BuildKey(prefix string, parts ...interface{}) string {
	if len(parts) == 0 {
		return prefix
	}
	key := prefix
	for _, part := range parts {
		key = fmt.Sprintf("%s_%v", key, part)
	}
	return key
}
