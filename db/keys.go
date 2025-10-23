// db/keys.go
package db

import (
	"fmt"
	"strings"
)

// ===================== 版本控制 =====================
// 设置全局 Key 版本前缀（例如 "v1" → 产出 "v1_<key>"）。
// 如需立刻兼容旧数据，暂时将 KeyVersion 设为 "" 即可不加版本前缀。
const KeyVersion = "v1"

// 把版本号拼到最前面（保持你下划线风格：v1_<...>）
func withVer(s string) string {
	if KeyVersion == "" {
		return s
	}
	return KeyVersion + "_" + s
}

// 读取回退辅助：把带版本的键去掉版本前缀，便于双读回退。
// 例：newKey := KeyTx(id); oldKey := StripVersion(newKey)
func StripVersion(prefixed string) string {
	if KeyVersion == "" {
		return prefixed
	}
	p := KeyVersion + "_"
	return strings.TrimPrefix(prefixed, p)
}

// —— 区块 ——
// 例：blockdata_<blockHash>
func KeyBlockData(blockHash string) string {
	return withVer(fmt.Sprintf("blockdata_%s", blockHash))
}

// 例：height_<height>_blocks
func KeyHeightBlocks(height uint64) string {
	return withVer(fmt.Sprintf("height_%d_blocks", height))
}

// 例：blockid_<blockHash>
func KeyBlockIDToHeight(blockHash string) string {
	return withVer(fmt.Sprintf("blockid_%s", blockHash))
}

// 例：latest_block_height
func KeyLatestHeight() string { return withVer("latest_block_height") }

// —— 交易通用映射 ——
// 例：anyTx_<txID>（映射到 tx_<txID> 或 order_<txID> 等）
func KeyAnyTx(txID string) string { return withVer("anyTx_" + txID) }

// —— Pending AnyTx ——
// 例：pending_anytx_<txID>
func KeyPendingAnyTx(txID string) string { return withVer("pending_anytx_" + txID) }

// —— 具体交易 ——
// 例：tx_<txID>
func KeyTx(txID string) string { return withVer("tx_" + txID) }

// 例：order_<txID>
func KeyOrderTx(txID string) string { return withVer("order_" + txID) }

// 例：minerTx_<txID>
func KeyMinerTx(txID string) string { return withVer("minerTx_" + txID) }

// —— 订单价格索引（保持你原来的管道与冒号结构不变）——
// 例：pair:<pair>|is_filled:<true|false>|price:<67位十进制>|order_id:<txID>
func KeyOrderPriceIndex(pair string, isFilled bool, priceKey67 string, orderID string) string {
	return withVer(fmt.Sprintf("pair:%s|is_filled:%t|price:%s|order_id:%s", pair, isFilled, priceKey67, orderID))
}

// —— 账户与索引 ——
// 例：account_<address>
func KeyAccount(addr string) string { return withVer("account_" + addr) }

// 例：indexToAccount_<idx>
func KeyIndexToAccount(idx uint64) string { return withVer(fmt.Sprintf("indexToAccount_%d", idx)) }
func NameOfKeyIndexToAccount() string     { return withVer("indexToAccount_") }

// 例：free_idx_%020d
// 你原来这个函数返回的是“前缀”，我保持不变：
func KeyFreeIdx() string { return withVer("free_idx_") }

// 投票/候选人等你已有的模式（如 stakeIndex_ 前缀里带 address:）保持原状：
// 例：stakeIndex_<invertedPadded32>__address:<addr>
func KeyStakeIndex(invertedPadded32, addr string) string {
	return withVer(fmt.Sprintf("stakeIndex_%s_address:%s", invertedPadded32, addr))
}

// —— 节点/客户端信息 ——
// 例：node_<pubKey>
// 你原函数名没有参数且返回前缀，我保持不变：
func KeyNode() string { return withVer("node_") }

// 例：clientinfo_<ip>
func KeyClientInfo(ip string) string { return withVer("clientinfo_" + ip) }
