// vm/frost_vault_dkg_share.go
// FrostVaultDkgShareTx 辅助类型
// Handler 实现在 frost_vault_dkg_share_handler.go 中
package vm

// DkgShareStatus DKG share 状态
type DkgShareStatus string

const (
	// DkgShareStatusPending 待验证
	DkgShareStatusPending DkgShareStatus = "PENDING"
	// DkgShareStatusVerified 已验证
	DkgShareStatusVerified DkgShareStatus = "VERIFIED"
	// DkgShareStatusDisputed 争议中
	DkgShareStatusDisputed DkgShareStatus = "DISPUTED"
)
