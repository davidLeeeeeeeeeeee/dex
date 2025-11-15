// keys/keys.go
// 统一的 Key 定义包，供 VM 和 DB 模块共同使用
package keys

import (
	"fmt"
	"strings"
)

// ===================== 版本控制 =====================
// 设置全局 Key 版本前缀（例如 "v1" → 产出 "v1_<key>"）。
// 如需立刻兼容旧数据，暂时将 KeyVersion 设为 "" 即可不加版本前缀。
const KeyVersion = "v1"

// withVer 把版本号拼到最前面（保持下划线风格：v1_<...>）
func withVer(s string) string {
	if KeyVersion == "" {
		return s
	}
	return KeyVersion + "_" + s
}

// StripVersion 读取回退辅助：把带版本的键去掉版本前缀，便于双读回退。
// 例：newKey := KeyTx(id); oldKey := StripVersion(newKey)
func StripVersion(prefixed string) string {
	if KeyVersion == "" {
		return prefixed
	}
	p := KeyVersion + "_"
	return strings.TrimPrefix(prefixed, p)
}

// ===================== 区块相关 =====================

// KeyBlockData 区块数据
// 例：v1_blockdata_<blockHash>
func KeyBlockData(blockHash string) string {
	return withVer(fmt.Sprintf("blockdata_%s", blockHash))
}

// KeyHeightBlocks 高度到区块的映射
// 例：v1_height_<height>_blocks
func KeyHeightBlocks(height uint64) string {
	return withVer(fmt.Sprintf("height_%d_blocks", height))
}

// KeyBlockIDToHeight 区块哈希到高度的映射
// 例：v1_blockid_<blockHash>
func KeyBlockIDToHeight(blockHash string) string {
	return withVer(fmt.Sprintf("blockid_%s", blockHash))
}

// KeyLatestHeight 最新区块高度
// 例：v1_latest_block_height
func KeyLatestHeight() string {
	return withVer("latest_block_height")
}

// ===================== 交易相关 =====================

// KeyAnyTx 交易通用映射（映射到 tx_<txID> 或 order_<txID> 等）
// 例：v1_anyTx_<txID>
func KeyAnyTx(txID string) string {
	return withVer("anyTx_" + txID)
}

// KeyPendingAnyTx Pending 交易
// 例：v1_pending_anytx_<txID>
func KeyPendingAnyTx(txID string) string {
	return withVer("pending_anytx_" + txID)
}

// KeyTx 普通交易
// 例：v1_tx_<txID>
func KeyTx(txID string) string {
	return withVer("tx_" + txID)
}

// KeyOrderTx 订单交易
// 例：v1_order_<txID>
func KeyOrderTx(txID string) string {
	return withVer("order_" + txID)
}

// KeyMinerTx 矿工交易
// 例：v1_minerTx_<txID>
func KeyMinerTx(txID string) string {
	return withVer("minerTx_" + txID)
}

// ===================== 账户相关 =====================

// KeyAccount 账户数据
// 例：v1_account_<address>
func KeyAccount(addr string) string {
	return withVer("account_" + addr)
}

// KeyIndexToAccount 索引到账户的映射
// 例：v1_indexToAccount_<idx>
func KeyIndexToAccount(idx uint64) string {
	return withVer(fmt.Sprintf("indexToAccount_%d", idx))
}

// NameOfKeyIndexToAccount 索引到账户的前缀
func NameOfKeyIndexToAccount() string {
	return withVer("indexToAccount_")
}

// KeyFreeIdx 空闲索引前缀
// 例：v1_free_idx_
func KeyFreeIdx() string {
	return withVer("free_idx_")
}

// KeyStakeIndex 质押索引
// 例：v1_stakeIndex_<invertedPadded32>_address:<addr>
func KeyStakeIndex(invertedPadded32, addr string) string {
	return withVer(fmt.Sprintf("stakeIndex_%s_address:%s", invertedPadded32, addr))
}

// ===================== 订单相关 =====================

// KeyOrder 订单数据
// 例：v1_order_<orderID>
func KeyOrder(orderID string) string {
	return withVer("order_" + orderID)
}

// KeyOrderPriceIndex 订单价格索引
// 例：v1_pair:<pair>|is_filled:<true|false>|price:<67位十进制>|order_id:<txID>
func KeyOrderPriceIndex(pair string, isFilled bool, priceKey67 string, orderID string) string {
	return withVer(fmt.Sprintf("pair:%s|is_filled:%t|price:%s|order_id:%s", pair, isFilled, priceKey67, orderID))
}

// KeyOrderPriceIndexPrefix 返回给定交易对和是否已成交状态下的价格索引前缀
// 例：v1_pair:<pair>|is_filled:<true|false>|
func KeyOrderPriceIndexPrefix(pair string, isFilled bool) string {
	return withVer(fmt.Sprintf("pair:%s|is_filled:%t|", pair, isFilled))
}


// ===================== Token 相关 =====================

// KeyToken Token 数据
// 例：v1_token_<tokenAddress>
func KeyToken(tokenAddress string) string {
	return withVer("token_" + tokenAddress)
}

// KeyTokenRegistry Token注册表
// 例：v1_token_registry
func KeyTokenRegistry() string {
	return withVer("token_registry")
}

// KeyFreeze 冻结标记
// 例：v1_freeze_<address>_<tokenAddress>
func KeyFreeze(address, tokenAddress string) string {
	return withVer(fmt.Sprintf("freeze_%s_%s", address, tokenAddress))
}

// ===================== 历史记录相关 =====================

// KeyTransferHistory 转账历史
// 例：v1_transfer_history_<txID>
func KeyTransferHistory(txID string) string {
	return withVer("transfer_history_" + txID)
}

// KeyMinerHistory 矿工历史
// 例：v1_miner_history_<txID>
func KeyMinerHistory(txID string) string {
	return withVer("miner_history_" + txID)
}

// KeyCandidateHistory 候选人历史
// 例：v1_candidate_history_<txID>
func KeyCandidateHistory(txID string) string {
	return withVer("candidate_history_" + txID)
}

// KeyRechargeHistory 充值历史
// 例：v1_recharge_history_<txID>
func KeyRechargeHistory(txID string) string {
	return withVer("recharge_history_" + txID)
}

// KeyFreezeHistory 冻结/解冻历史
// 例：v1_freeze_history_<txID>
func KeyFreezeHistory(txID string) string {
	return withVer("freeze_history_" + txID)
}

// ===================== 索引相关 =====================

// KeyCandidateIndex 候选人索引
// 例：v1_candidate:<candidateAddress>|user:<userAddress>
func KeyCandidateIndex(candidateAddress, userAddress string) string {
	return withVer(fmt.Sprintf("candidate:%s|user:%s", candidateAddress, userAddress))
}

// KeyRechargeRecord 充值记录
// 例：v1_recharge_record_<address>_<tweak>
func KeyRechargeRecord(address, tweak string) string {
	return withVer(fmt.Sprintf("recharge_record_%s_%s", address, tweak))
}

// KeyRechargeAddress 充值地址映射
// 例：v1_recharge_address_<generatedAddress>
func KeyRechargeAddress(generatedAddress string) string {
	return withVer("recharge_address_" + generatedAddress)
}

// ===================== VM 执行状态相关 =====================

// KeyVMAppliedTx VM 已应用的交易状态
// 例：v1_vm_applied_tx_<txID>
func KeyVMAppliedTx(txID string) string {
	return withVer("vm_applied_tx_" + txID)
}

// KeyVMTxError VM 交易错误信息
// 例：v1_vm_tx_error_<txID>
func KeyVMTxError(txID string) string {
	return withVer("vm_tx_error_" + txID)
}

// KeyVMTxHeight VM 交易所在高度
// 例：v1_vm_tx_height_<txID>
func KeyVMTxHeight(txID string) string {
	return withVer("vm_tx_height_" + txID)
}

// KeyVMCommitHeight VM 区块提交标记（用于幂等性检查）
// 例：v1_vm_commit_h_<height>
func KeyVMCommitHeight(height uint64) string {
	return withVer(fmt.Sprintf("vm_commit_h_%d", height))
}

// KeyVMBlockHeight VM 区块高度索引
// 例：v1_vm_block_h_<height>
func KeyVMBlockHeight(height uint64) string {
	return withVer(fmt.Sprintf("vm_block_h_%d", height))
}

// ===================== 节点/客户端信息 =====================

// KeyNode 节点信息前缀
// 例：v1_node_
func KeyNode() string {
	return withVer("node_")
}

// KeyClientInfo 客户端信息
// 例：v1_clientinfo_<ip>
func KeyClientInfo(ip string) string {
	return withVer("clientinfo_" + ip)
}

