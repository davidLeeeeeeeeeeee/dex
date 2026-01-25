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

// KeyBlockReward 区块奖励发放记录
// 例：v1_block_reward_<height>
func KeyBlockReward(height uint64) string {
	return withVer(fmt.Sprintf("block_reward_%020d", height))
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

// KeyTxRaw 交易原文（不可变）
// 用于区块存储层保存交易原始数据，独立于订单簿状态
// 例：v1_txraw_<txID>
func KeyTxRaw(txID string) string {
	return withVer("txraw_" + txID)
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

// KeyOrder 订单数据（已废弃，兼容旧数据）
// 例：v1_order_<orderID>
// Deprecated: 新代码应使用 KeyOrderState
func KeyOrder(orderID string) string {
	return withVer("order_" + orderID)
}

// KeyOrderState 订单状态（可变）
// 用于存储 OrderState，由 VM Diff 控制
// 例：v1_orderstate_<orderID>
func KeyOrderState(orderID string) string {
	return withVer("orderstate_" + orderID)
}

// KeyOrderStatePrefix 订单状态前缀
// 例：v1_orderstate_
func KeyOrderStatePrefix() string {
	return withVer("orderstate_")
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

// ===================== 成交记录相关 =====================

// KeyTradeRecord 成交记录
// 例：v1_trade_<pair>_<timestamp>_<tradeID>
func KeyTradeRecord(pair string, timestamp int64, tradeID string) string {
	// 使用倒序时间戳，让最新的成交记录排在前面
	invertedTimestamp := ^uint64(timestamp)
	return withVer(fmt.Sprintf("trade_%s_%020d_%s", pair, invertedTimestamp, tradeID))
}

// KeyTradeRecordPrefix 成交记录前缀（按交易对）
// 例：v1_trade_<pair>_
func KeyTradeRecordPrefix(pair string) string {
	return withVer(fmt.Sprintf("trade_%s_", pair))
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

// ===================== FROST 相关 =====================
// 重要：资金 lot 必须按 vault_id 分片，避免跨 Vault 混用导致确定性规划错误或多签发

// KeyFrostFundsLotIndex 入账 lot 索引（按 vault_id + finalize_height + seq）
// 例：v1_frost_funds_lot_<chain>_<asset>_<vault_id>_<height>_<seq>
func KeyFrostFundsLotIndex(chain, asset string, vaultID uint32, height, seq uint64) string {
	return withVer(fmt.Sprintf("frost_funds_lot_%s_%s_%d_%s_%s", chain, asset, vaultID, padUint(height), padUint(seq)))
}

// KeyFrostFundsLotSeq 入账 lot 高度内序号（按 vault_id 分片）
// 例：v1_frost_funds_lot_seq_<chain>_<asset>_<vault_id>_<height>
func KeyFrostFundsLotSeq(chain, asset string, vaultID uint32, height uint64) string {
	return withVer(fmt.Sprintf("frost_funds_lot_seq_%s_%s_%d_%s", chain, asset, vaultID, padUint(height)))
}

// KeyFrostFundsLotHead 每个 Vault 的 FIFO 头指针
// 例：v1_frost_funds_lot_head_<chain>_<asset>_<vault_id>
func KeyFrostFundsLotHead(chain, asset string, vaultID uint32) string {
	return withVer(fmt.Sprintf("frost_funds_lot_head_%s_%s_%d", chain, asset, vaultID))
}

// KeyFrostFundsPendingLotIndex 待入账 lot 索引（按 vault_id + request_height + seq）
// 例：v1_frost_funds_pending_lot_<chain>_<asset>_<vault_id>_<height>_<seq>
func KeyFrostFundsPendingLotIndex(chain, asset string, vaultID uint32, height, seq uint64) string {
	return withVer(fmt.Sprintf("frost_funds_pending_lot_%s_%s_%d_%s_%s", chain, asset, vaultID, padUint(height), padUint(seq)))
}

// KeyFrostFundsPendingLotSeq 待入账 lot 高度内序号（按 vault_id 分片）
// 例：v1_frost_funds_pending_lot_seq_<chain>_<asset>_<vault_id>_<height>
func KeyFrostFundsPendingLotSeq(chain, asset string, vaultID uint32, height uint64) string {
	return withVer(fmt.Sprintf("frost_funds_pending_lot_seq_%s_%s_%d_%s", chain, asset, vaultID, padUint(height)))
}

// KeyFrostFundsPendingLotRef request_id -> (vault_id, pending lot key)
// 例：v1_frost_funds_pending_ref_<request_id>
func KeyFrostFundsPendingLotRef(requestID string) string {
	return withVer("frost_funds_pending_ref_" + requestID)
}

// KeyFrostVaultFundsLedger Vault 的资金账本聚合状态
// 例：v1_frost_funds_<chain>_<asset>_<vault_id>
func KeyFrostVaultFundsLedger(chain, asset string, vaultID uint32) string {
	return withVer(fmt.Sprintf("frost_funds_%s_%s_%d", chain, asset, vaultID))
}

// KeyFrostBtcUtxo Vault 的 UTXO（按 vault_id 隔离，避免跨 Vault 误签）
// 例：v1_frost_btc_utxo_<vault_id>_<txid>_<vout>
func KeyFrostBtcUtxo(vaultID uint32, txid string, vout uint32) string {
	return withVer(fmt.Sprintf("frost_btc_utxo_%d_%s_%d", vaultID, txid, vout))
}

// KeyFrostBtcLockedUtxo Vault 的已锁定 UTXO -> job_id
// 例：v1_frost_btc_locked_utxo_<vault_id>_<txid>_<vout>
func KeyFrostBtcLockedUtxo(vaultID uint32, txid string, vout uint32) string {
	return withVer(fmt.Sprintf("frost_btc_locked_utxo_%d_%s_%d", vaultID, txid, vout))
}

// KeyFrostWithdraw 提现请求状态
// 例：v1_frost_withdraw_<withdraw_id>
func KeyFrostWithdraw(withdrawID string) string {
	return withVer("frost_withdraw_" + withdrawID)
}

// KeyFrostWithdrawFIFOIndex 按 (chain, asset) 分队列的 FIFO 索引
// 例：v1_frost_withdraw_q_<chain>_<asset>_<seq>
func KeyFrostWithdrawFIFOIndex(chain, asset string, seq uint64) string {
	return withVer(fmt.Sprintf("frost_withdraw_q_%s_%s_%s", chain, asset, padUint(seq)))
}

// KeyFrostWithdrawFIFOSeq 每个 (chain, asset) 队列的 seq 计数器
// 例：v1_frost_withdraw_seq_<chain>_<asset>
func KeyFrostWithdrawFIFOSeq(chain, asset string) string {
	return withVer(fmt.Sprintf("frost_withdraw_seq_%s_%s", chain, asset))
}

// KeyFrostWithdrawFIFOHead 每个队列的 FIFO 头指针（下一个待处理 seq）
// 例：v1_frost_withdraw_head_<chain>_<asset>
func KeyFrostWithdrawFIFOHead(chain, asset string) string {
	return withVer(fmt.Sprintf("frost_withdraw_head_%s_%s", chain, asset))
}

// KeyFrostWithdrawTxRef tx_id -> withdraw_id 引用（用于幂等检查）
// 例：v1_frost_withdraw_ref_<tx_id>
func KeyFrostWithdrawTxRef(txID string) string {
	return withVer("frost_withdraw_ref_" + txID)
}

// KeyFrostSignedPackageCount 签名产物数量（用于追加 receipt/history）
// 例：v1_frost_signed_pkg_count_<job_id>
func KeyFrostSignedPackageCount(jobID string) string {
	return withVer("frost_signed_pkg_count_" + jobID)
}

// KeyFrostConfig Frost 全局配置
// 例：v1_frost_cfg
func KeyFrostConfig() string {
	return withVer("frost_cfg")
}

// KeyFrostTop10000 Frost Top 10000 矿工列表
// 例：v1_frost_top10000
func KeyFrostTop10000() string {
	return withVer("frost_top10000")
}

// KeyFrostVaultConfig Vault 配置
// 例：v1_frost_vault_cfg_<chain>_<vault_id>
func KeyFrostVaultConfig(chain string, vaultID uint32) string {
	return withVer(fmt.Sprintf("frost_vault_cfg_%s_%d", chain, vaultID))
}

// KeyFrostVaultState Vault 状态（包含 key_epoch, group_pubkey 等）
// 例：v1_frost_vault_state_<chain>_<vault_id>
func KeyFrostVaultState(chain string, vaultID uint32) string {
	return withVer(fmt.Sprintf("frost_vault_state_%s_%d", chain, vaultID))
}

// KeyFrostVaultTransition Vault 轮换状态
// 例：v1_frost_vault_transition_<chain>_<vault_id>_<epoch_id>
func KeyFrostVaultTransition(chain string, vaultID uint32, epochID uint64) string {
	return withVer(fmt.Sprintf("frost_vault_transition_%s_%d_%s", chain, vaultID, padUint(epochID)))
}

// KeyFrostVaultDkgCommit Vault DKG 承诺点
// 例：v1_frost_vault_dkg_commit_<chain>_<vault_id>_<epoch_id>_<participant>
func KeyFrostVaultDkgCommit(chain string, vaultID uint32, epochID uint64, participant string) string {
	return withVer(fmt.Sprintf("frost_vault_dkg_commit_%s_%d_%s_%s", chain, vaultID, padUint(epochID), participant))
}

// KeyFrostVaultDkgShare Vault DKG share（加密）
// 例：v1_frost_vault_dkg_share_<chain>_<vault_id>_<epoch_id>_<dealer>_<receiver>
func KeyFrostVaultDkgShare(chain string, vaultID uint32, epochID uint64, dealer, receiver string) string {
	return withVer(fmt.Sprintf("frost_vault_dkg_share_%s_%d_%s_%s_%s", chain, vaultID, padUint(epochID), dealer, receiver))
}

// KeyFrostVaultDkgComplaint Vault DKG 投诉
// 例：v1_frost_vault_dkg_complaint_<chain>_<vault_id>_<epoch_id>_<dealer>_<receiver>
func KeyFrostVaultDkgComplaint(chain string, vaultID uint32, epochID uint64, dealer, receiver string) string {
	return withVer(fmt.Sprintf("frost_vault_dkg_complaint_%s_%d_%s_%s_%s", chain, vaultID, padUint(epochID), dealer, receiver))
}

// KeyFrostSignedPackage 签名产物记录
// 例：v1_frost_signed_pkg_<job_id>_<idx>
func KeyFrostSignedPackage(jobID string, idx uint64) string {
	return withVer(fmt.Sprintf("frost_signed_pkg_%s_%s", jobID, padUint(idx)))
}

func padUint(v uint64) string {
	return fmt.Sprintf("%020d", v)
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

// ===================== Witness 见证者相关 =====================

// KeyWitnessInfo 见证者信息
// 例：v1_winfo_<address>
func KeyWitnessInfo(address string) string {
	return withVer("winfo_" + address)
}

// KeyWitnessInfoPrefix 见证者信息前缀
// 例：v1_winfo_
func KeyWitnessInfoPrefix() string {
	return withVer("winfo_")
}

// KeyRechargeRequest 入账请求
// 例：v1_recharge_request_<requestID>
func KeyRechargeRequest(requestID string) string {
	return withVer("recharge_request_" + requestID)
}

// KeyRechargeRequestPrefix 入账请求前缀
// 例：v1_recharge_request_
func KeyRechargeRequestPrefix() string {
	return withVer("recharge_request_")
}

// KeyRechargeRequestByNativeTx 原生链交易到入账请求的映射
// 例：v1_recharge_native_<chain>_<txHash>
func KeyRechargeRequestByNativeTx(chain, txHash string) string {
	return withVer(fmt.Sprintf("recharge_native_%s_%s", chain, txHash))
}

// KeyChallengeRecord 挑战记录
// 例：v1_challenge_<challengeID>
func KeyChallengeRecord(challengeID string) string {
	return withVer("challenge_" + challengeID)
}

// KeyChallengeRecordPrefix 挑战记录前缀
// 例：v1_challenge_
func KeyChallengeRecordPrefix() string {
	return withVer("challenge_")
}

// KeyWitnessVote 见证投票
// 例：v1_wvote_<requestID>_<witnessAddress>
func KeyWitnessVote(requestID, witnessAddress string) string {
	return withVer(fmt.Sprintf("wvote_%s_%s", requestID, witnessAddress))
}

// KeyWitnessVotePrefix 见证投票前缀（按请求ID）
// 例：v1_wvote_<requestID>_
func KeyWitnessVotePrefix(requestID string) string {
	return withVer(fmt.Sprintf("wvote_%s_", requestID))
}

// KeyArbitrationVote 仲裁投票
// 例：v1_arbitration_vote_<challengeID>_<arbitratorAddress>
func KeyArbitrationVote(challengeID, arbitratorAddress string) string {
	return withVer(fmt.Sprintf("arbitration_vote_%s_%s", challengeID, arbitratorAddress))
}

// KeyArbitrationVotePrefix 仲裁投票前缀（按挑战ID）
// 例：v1_arbitration_vote_<challengeID>_
func KeyArbitrationVotePrefix(challengeID string) string {
	return withVer(fmt.Sprintf("arbitration_vote_%s_", challengeID))
}

// KeyWitnessStakeIndex 见证者质押索引（按质押金额倒序）
// 例：v1_wstake_<invertedPadded32>_<address>
func KeyWitnessStakeIndex(invertedPadded32, address string) string {
	return withVer(fmt.Sprintf("wstake_%s_%s", invertedPadded32, address))
}

// KeyWitnessStakeIndexPrefix 见证者质押索引前缀
// 例：v1_wstake_
func KeyWitnessStakeIndexPrefix() string {
	return withVer("wstake_")
}

// KeyWitnessConfig 见证者配置
// 例：v1_witness_config
func KeyWitnessConfig() string {
	return withVer("witness_config")
}

// KeyWitnessRewardPool 见证者奖励池
// 例：v1_witness_reward_pool
func KeyWitnessRewardPool() string {
	return withVer("witness_reward_pool")
}

// KeyWitnessHistory 见证历史记录
// 例：v1_whist_<txID>
func KeyWitnessHistory(txID string) string {
	return withVer("whist_" + txID)
}

// KeyShelvedRequest 搁置的请求
// 例：v1_shelved_request_<requestID>
func KeyShelvedRequest(requestID string) string {
	return withVer("shelved_request_" + requestID)
}

// KeyShelvedRequestPrefix 搁置请求前缀
// 例：v1_shelved_request_
func KeyShelvedRequestPrefix() string {
	return withVer("shelved_request_")
}
