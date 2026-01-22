package indexdb

import "fmt"

const (
	// 同步状态键
	KeySyncHeight = "sync_height" // 已同步到的区块高度
	KeySyncNode   = "sync_node"   // 同步的节点地址
)

// KeyAddressTx 地址交易索引
// 格式: addr_tx_<address>_<height(倒序补零)>_<tx_index>
// 使用高度倒序，让最新的交易排在前面
func KeyAddressTx(address string, height uint64, txIndex int) string {
	// 使用倒序高度（max - height），让新交易排在前面
	invertedHeight := ^height // 按位取反，实现倒序
	return fmt.Sprintf("addr_tx_%s_%020d_%04d", address, invertedHeight, txIndex)
}

// KeyAddressTxPrefix 地址交易索引前缀
func KeyAddressTxPrefix(address string) string {
	return fmt.Sprintf("addr_tx_%s_", address)
}

// KeyBlockTx 区块交易索引（用于查询某个区块的所有交易）
// 格式: block_tx_<height>_<tx_index>
func KeyBlockTx(height uint64, txIndex int) string {
	return fmt.Sprintf("block_tx_%020d_%04d", height, txIndex)
}

// KeyBlockTxPrefix 区块交易索引前缀
func KeyBlockTxPrefix(height uint64) string {
	return fmt.Sprintf("block_tx_%020d_", height)
}

// KeyTxDetail 交易详情
// 格式: tx_detail_<tx_id>
func KeyTxDetail(txID string) string {
	return fmt.Sprintf("tx_detail_%s", txID)
}

// KeyAddressCount 地址交易计数
// 格式: addr_count_<address>
func KeyAddressCount(address string) string {
	return fmt.Sprintf("addr_count_%s", address)
}
