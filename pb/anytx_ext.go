// pb/anytx_ext.go
package pb

func (m *AnyTx) GetBase() *BaseMessage {
	switch tx := m.GetContent().(type) {
	case *AnyTx_IssueTokenTx:
		return tx.IssueTokenTx.GetBase()
	case *AnyTx_FreezeTx:
		return tx.FreezeTx.GetBase()
	case *AnyTx_Transaction:
		return tx.Transaction.GetBase()
	case *AnyTx_OrderTx:
		return tx.OrderTx.GetBase()
	case *AnyTx_MinerTx:
		return tx.MinerTx.GetBase()
	case *AnyTx_WitnessStakeTx:
		return tx.WitnessStakeTx.GetBase()
	case *AnyTx_WitnessRequestTx:
		return tx.WitnessRequestTx.GetBase()
	case *AnyTx_WitnessVoteTx:
		return tx.WitnessVoteTx.GetBase()
	case *AnyTx_WitnessChallengeTx:
		return tx.WitnessChallengeTx.GetBase()
	case *AnyTx_ArbitrationVoteTx:
		return tx.ArbitrationVoteTx.GetBase()
	case *AnyTx_WitnessClaimRewardTx:
		return tx.WitnessClaimRewardTx.GetBase()
	// Frost 相关交易
	case *AnyTx_FrostWithdrawRequestTx:
		return tx.FrostWithdrawRequestTx.GetBase()
	case *AnyTx_FrostWithdrawSignedTx:
		return tx.FrostWithdrawSignedTx.GetBase()
	case *AnyTx_FrostVaultDkgCommitTx:
		return tx.FrostVaultDkgCommitTx.GetBase()
	case *AnyTx_FrostVaultDkgShareTx:
		return tx.FrostVaultDkgShareTx.GetBase()
	case *AnyTx_FrostVaultDkgComplaintTx:
		return tx.FrostVaultDkgComplaintTx.GetBase()
	case *AnyTx_FrostVaultDkgRevealTx:
		return tx.FrostVaultDkgRevealTx.GetBase()
	case *AnyTx_FrostVaultDkgValidationSignedTx:
		return tx.FrostVaultDkgValidationSignedTx.GetBase()
	case *AnyTx_FrostVaultTransitionSignedTx:
		return tx.FrostVaultTransitionSignedTx.GetBase()
	case *AnyTx_FrostWithdrawPlanningLogTx:
		return tx.FrostWithdrawPlanningLogTx.GetBase()
	default:
		return nil
	}
}

func (m *AnyTx) GetTxId() string {
	if b := m.GetBase(); b != nil {
		return b.TxId
	}
	return ""
}
