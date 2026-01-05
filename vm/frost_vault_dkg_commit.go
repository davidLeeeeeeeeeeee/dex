// vm/frost_vault_dkg_commit.go
// FrostVaultDkgCommitTx 辅助类型
// Handler 实现在 frost_vault_dkg_commit_handler.go 中
package vm

// DkgCommitmentStatus DKG 承诺状态
type DkgCommitmentStatus string

const (
	// DkgCommitmentStatusCommitted 已提交
	DkgCommitmentStatusCommitted DkgCommitmentStatus = "COMMITTED"
	// DkgCommitmentStatusDisqualified 已失格
	DkgCommitmentStatusDisqualified DkgCommitmentStatus = "DISQUALIFIED"
)
