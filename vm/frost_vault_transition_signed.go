// vm/frost_vault_transition_signed.go
// FrostVaultTransitionSignedTx 辅助类型和常量
// Handler 实现在 frost_vault_transition_signed_handler.go 中
package vm

// Vault 生命周期常量
const (
	VaultLifecycleActive   = "ACTIVE"   // 正常运行
	VaultLifecycleDraining = "DRAINING" // 停止入账，等待资金迁移
	VaultLifecycleRetired  = "RETIRED"  // 已退役
)

// Vault 状态常量
const (
	VaultStatusKeyReady   = "KEY_READY"  // 密钥已就绪
	VaultStatusActive     = "ACTIVE"     // 激活中
	VaultStatusDeprecated = "DEPRECATED" // 已弃用
)
