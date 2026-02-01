// keys/category.go
// Key 分类模块：判断数据应该存储到 KV 还是 StateDB
package keys

import "strings"

// KeyCategory 定义 Key 的存储归属
type KeyCategory int

const (
	CategoryKV    KeyCategory = iota // 存 KV（不可变流水/索引）
	CategoryState                    // 存 StateDB（可变状态）
)

// ========== 可变状态数据前缀（存 StateDB）==========
// 这些数据需要版本管理、参与 Merkle 树、支持历史回溯
var statePrefixes = []string{
	"v1_account_",            // 账户状态（余额、Nonce、矿工标记）
	"v1_acc_order_",          // 账户关联的订单（离散存储）
	"v1_order_",              // 订单实体数据
	"v1_orderstate_",         // 订单实时状态（成交量、是否完成）
	"v1_token_",              // Token 信息（包括 token_registry）
	"v1_freeze_",             // 冻结标记（但排除 freeze_history）
	"v1_recharge_record_",    // 充值记录状态
	"v1_recharge_request_",   // 入账请求状态
	"v1_winfo_",              // 见证者信息
	"v1_wvote_",              // 见证投票
	"v1_wstake_",             // 见证者质押索引
	"v1_witness_config",      // 见证者配置
	"v1_witness_reward_pool", // 见证者奖励池
	"v1_challenge_",          // 挑战记录
	"v1_shelved_request_",    // 搁置请求
	"v1_arbitration_vote_",   // 仲裁投票
}

// ========== 需要排除的历史记录前缀（这些存 KV）==========
// 虽然以上述前缀开头，但属于历史记录，不可变
var excludeFromState = []string{
	"v1_freeze_history_", // 冻结历史（后缀匹配优先级高于 v1_freeze_）
}

// CategorizeKey 判断 key 应该存到哪里
// 返回 CategoryState 表示存 StateDB（可变状态）
// 返回 CategoryKV 表示存 KV（不可变流水/索引）
func CategorizeKey(key string) KeyCategory {
	// 1. 先检查排除列表（历史记录等）
	for _, ex := range excludeFromState {
		if strings.HasPrefix(key, ex) {
			return CategoryKV
		}
	}

	// 2. 检查是否属于状态数据
	for _, prefix := range statePrefixes {
		if strings.HasPrefix(key, prefix) {
			return CategoryState
		}
	}

	// 3. 其他都存 KV
	return CategoryKV
}

// IsStatefulKey 判断 key 是否属于可变状态（便捷方法）
func IsStatefulKey(key string) bool {
	return CategorizeKey(key) == CategoryState
}

// IsFlowKey 判断 key 是否属于不可变流水/索引（便捷方法）
func IsFlowKey(key string) bool {
	return CategorizeKey(key) == CategoryKV
}

// ========== 按数据类型分组的判断（用于调试和统计）==========

// IsAccountKey 判断是否为账户数据
func IsAccountKey(key string) bool {
	return strings.HasPrefix(key, "v1_account_")
}

// IsOrderStateKey 判断是否为订单状态数据
func IsOrderStateKey(key string) bool {
	return strings.HasPrefix(key, "v1_orderstate_")
}

// IsBlockKey 判断是否为区块数据
func IsBlockKey(key string) bool {
	return strings.HasPrefix(key, "v1_blockdata_") ||
		strings.HasPrefix(key, "v1_height_") ||
		strings.HasPrefix(key, "v1_blockid_") ||
		key == "v1_latest_block_height"
}

// IsTxKey 判断是否为交易流水数据
func IsTxKey(key string) bool {
	return strings.HasPrefix(key, "v1_txraw_") ||
		strings.HasPrefix(key, "v1_anyTx_") ||
		strings.HasPrefix(key, "v1_pending_anytx_") ||
		strings.HasPrefix(key, "v1_tx_") ||
		strings.HasPrefix(key, "v1_order_") ||
		strings.HasPrefix(key, "v1_minerTx_")
}

// IsIndexKey 判断是否为索引数据
func IsIndexKey(key string) bool {
	return strings.HasPrefix(key, "v1_pair:") ||
		strings.HasPrefix(key, "v1_stakeIndex_") ||
		strings.HasPrefix(key, "v1_indexToAccount_") ||
		strings.HasPrefix(key, "v1_trade_")
}

// IsHistoryKey 判断是否为历史记录
func IsHistoryKey(key string) bool {
	return strings.HasPrefix(key, "v1_transfer_history_") ||
		strings.HasPrefix(key, "v1_miner_history_") ||
		strings.HasPrefix(key, "v1_recharge_history_") ||
		strings.HasPrefix(key, "v1_freeze_history_") ||
		strings.HasPrefix(key, "v1_whist_")
}

// IsVMKey 判断是否为 VM 执行状态
func IsVMKey(key string) bool {
	return strings.HasPrefix(key, "v1_vm_")
}

// IsFrostKey 判断是否为 FROST 相关数据
func IsFrostKey(key string) bool {
	return strings.HasPrefix(key, "v1_frost_")
}

// IsWitnessKey 判断是否为见证者相关数据
func IsWitnessKey(key string) bool {
	return strings.HasPrefix(key, "v1_winfo_") ||
		strings.HasPrefix(key, "v1_wvote_") ||
		strings.HasPrefix(key, "v1_wstake_") ||
		strings.HasPrefix(key, "v1_witness_") ||
		strings.HasPrefix(key, "v1_challenge_") ||
		strings.HasPrefix(key, "v1_arbitration_vote_") ||
		strings.HasPrefix(key, "v1_shelved_request_") ||
		strings.HasPrefix(key, "v1_recharge_request_")
}
