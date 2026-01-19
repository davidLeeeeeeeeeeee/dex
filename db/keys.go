// db/keys.go
// 为了向后兼容，db 包重新导出 keys 包的函数
// 新代码应该直接使用 "dex/keys" 包
package db

import "dex/keys"

// ===================== 版本控制 =====================

// KeyVersion 版本前缀（重新导出）
const KeyVersion = keys.KeyVersion

// StripVersion 去掉版本前缀（重新导出）
func StripVersion(prefixed string) string {
	return keys.StripVersion(prefixed)
}

// ===================== 区块相关 =====================

func KeyBlockData(blockHash string) string {
	return keys.KeyBlockData(blockHash)
}

func KeyHeightBlocks(height uint64) string {
	return keys.KeyHeightBlocks(height)
}

func KeyBlockIDToHeight(blockHash string) string {
	return keys.KeyBlockIDToHeight(blockHash)
}

func KeyLatestHeight() string {
	return keys.KeyLatestHeight()
}

// ===================== 交易相关 =====================

func KeyAnyTx(txID string) string {
	return keys.KeyAnyTx(txID)
}

func KeyPendingAnyTx(txID string) string {
	return keys.KeyPendingAnyTx(txID)
}

func KeyTx(txID string) string {
	return keys.KeyTx(txID)
}

func KeyOrderTx(txID string) string {
	return keys.KeyOrderTx(txID)
}

func KeyMinerTx(txID string) string {
	return keys.KeyMinerTx(txID)
}

func KeyTxRaw(txID string) string {
	return keys.KeyTxRaw(txID)
}

// ===================== 账户相关 =====================

func KeyAccount(addr string) string {
	return keys.KeyAccount(addr)
}

func KeyIndexToAccount(idx uint64) string {
	return keys.KeyIndexToAccount(idx)
}

func NameOfKeyIndexToAccount() string {
	return keys.NameOfKeyIndexToAccount()
}

func KeyFreeIdx() string {
	return keys.KeyFreeIdx()
}

func KeyStakeIndex(invertedPadded32, addr string) string {
	return keys.KeyStakeIndex(invertedPadded32, addr)
}

// ===================== 订单相关 =====================

func KeyOrder(orderID string) string {
	return keys.KeyOrder(orderID)
}

func KeyOrderState(orderID string) string {
	return keys.KeyOrderState(orderID)
}

func KeyOrderStatePrefix() string {
	return keys.KeyOrderStatePrefix()
}

func KeyOrderPriceIndex(pair string, isFilled bool, priceKey67 string, orderID string) string {
	return keys.KeyOrderPriceIndex(pair, isFilled, priceKey67, orderID)
}

// ===================== Token 相关 =====================

func KeyToken(tokenAddress string) string {
	return keys.KeyToken(tokenAddress)
}

func KeyTokenRegistry() string {
	return keys.KeyTokenRegistry()
}

func KeyFreeze(address, tokenAddress string) string {
	return keys.KeyFreeze(address, tokenAddress)
}

// ===================== 历史记录相关 =====================

func KeyTransferHistory(txID string) string {
	return keys.KeyTransferHistory(txID)
}

func KeyMinerHistory(txID string) string {
	return keys.KeyMinerHistory(txID)
}

func KeyRechargeHistory(txID string) string {
	return keys.KeyRechargeHistory(txID)
}

func KeyFreezeHistory(txID string) string {
	return keys.KeyFreezeHistory(txID)
}

// ===================== 索引相关 =====================

func KeyRechargeRecord(address, tweak string) string {
	return keys.KeyRechargeRecord(address, tweak)
}

func KeyRechargeAddress(generatedAddress string) string {
	return keys.KeyRechargeAddress(generatedAddress)
}

// ===================== FROST 相关 =====================
// 重要：资金 lot 必须按 vault_id 分片，避免跨 Vault 混用导致确定性规划错误或多签发

func KeyFrostFundsLotIndex(chain, asset string, vaultID uint32, height, seq uint64) string {
	return keys.KeyFrostFundsLotIndex(chain, asset, vaultID, height, seq)
}

func KeyFrostFundsLotSeq(chain, asset string, vaultID uint32, height uint64) string {
	return keys.KeyFrostFundsLotSeq(chain, asset, vaultID, height)
}

func KeyFrostFundsLotHead(chain, asset string, vaultID uint32) string {
	return keys.KeyFrostFundsLotHead(chain, asset, vaultID)
}

func KeyFrostFundsPendingLotIndex(chain, asset string, vaultID uint32, height, seq uint64) string {
	return keys.KeyFrostFundsPendingLotIndex(chain, asset, vaultID, height, seq)
}

func KeyFrostFundsPendingLotSeq(chain, asset string, vaultID uint32, height uint64) string {
	return keys.KeyFrostFundsPendingLotSeq(chain, asset, vaultID, height)
}

func KeyFrostFundsPendingLotRef(requestID string) string {
	return keys.KeyFrostFundsPendingLotRef(requestID)
}

func KeyFrostVaultFundsLedger(chain, asset string, vaultID uint32) string {
	return keys.KeyFrostVaultFundsLedger(chain, asset, vaultID)
}

func KeyFrostBtcUtxo(vaultID uint32, txid string, vout uint32) string {
	return keys.KeyFrostBtcUtxo(vaultID, txid, vout)
}

func KeyFrostBtcLockedUtxo(vaultID uint32, txid string, vout uint32) string {
	return keys.KeyFrostBtcLockedUtxo(vaultID, txid, vout)
}

// ===================== VM 执行状态相关 =====================

func KeyVMAppliedTx(txID string) string {
	return keys.KeyVMAppliedTx(txID)
}

func KeyVMTxError(txID string) string {
	return keys.KeyVMTxError(txID)
}

func KeyVMTxHeight(txID string) string {
	return keys.KeyVMTxHeight(txID)
}

func KeyVMCommitHeight(height uint64) string {
	return keys.KeyVMCommitHeight(height)
}

func KeyVMBlockHeight(height uint64) string {
	return keys.KeyVMBlockHeight(height)
}

// ===================== 节点/客户端信息 =====================

func KeyNode() string {
	return keys.KeyNode()
}

func KeyClientInfo(ip string) string {
	return keys.KeyClientInfo(ip)
}
