package indexdb

import (
	"dex/pb"
)

// extractTxRecord 从 AnyTx 提取交易记录
func extractTxRecord(tx *pb.AnyTx, height uint64, txIndex int) *TxRecord {
	if tx == nil {
		return nil
	}

	record := &TxRecord{
		TxID:    tx.GetTxId(),
		TxType:  extractTxType(tx),
		Height:  height,
		TxIndex: txIndex,
	}

	// 提取基础信息
	if base := tx.GetBase(); base != nil {
		record.FromAddress = base.FromAddress
		record.Status = base.Status.String()
		record.Fee = base.Fee
		record.Nonce = base.Nonce
	}

	// 提取 to 和 value
	record.ToAddress, record.Value = extractToAndValue(tx)

	return record
}

// extractTxType 提取交易类型
func extractTxType(tx *pb.AnyTx) string {
	if tx == nil {
		return ""
	}
	switch tx.GetContent().(type) {
	case *pb.AnyTx_Transaction:
		return "Transaction"
	case *pb.AnyTx_IssueTokenTx:
		return "IssueToken"
	case *pb.AnyTx_FreezeTx:
		return "Freeze"
	case *pb.AnyTx_OrderTx:
		return "Order"
	case *pb.AnyTx_MinerTx:
		return "Miner"
	case *pb.AnyTx_WitnessStakeTx:
		return "WitnessStake"
	case *pb.AnyTx_WitnessRequestTx:
		return "WitnessRequest"
	case *pb.AnyTx_WitnessVoteTx:
		return "WitnessVote"
	case *pb.AnyTx_WitnessChallengeTx:
		return "WitnessChallenge"
	case *pb.AnyTx_ArbitrationVoteTx:
		return "ArbitrationVote"
	case *pb.AnyTx_WitnessClaimRewardTx:
		return "WitnessClaimReward"
	case *pb.AnyTx_FrostWithdrawRequestTx:
		return "FrostWithdrawRequest"
	case *pb.AnyTx_FrostWithdrawSignedTx:
		return "FrostWithdrawSigned"
	case *pb.AnyTx_FrostVaultDkgCommitTx:
		return "FrostVaultDkgCommit"
	case *pb.AnyTx_FrostVaultDkgShareTx:
		return "FrostVaultDkgShare"
	case *pb.AnyTx_FrostVaultDkgComplaintTx:
		return "FrostVaultDkgComplaint"
	case *pb.AnyTx_FrostVaultDkgRevealTx:
		return "FrostVaultDkgReveal"
	case *pb.AnyTx_FrostVaultDkgValidationSignedTx:
		return "FrostVaultDkgValidationSigned"
	case *pb.AnyTx_FrostVaultTransitionSignedTx:
		return "FrostVaultTransitionSigned"
	}
	return "Unknown"
}

// extractToAndValue 提取目标地址和金额
func extractToAndValue(tx *pb.AnyTx) (toAddress, value string) {
	if tx == nil {
		return "", ""
	}
	switch c := tx.GetContent().(type) {
	case *pb.AnyTx_Transaction:
		t := c.Transaction
		return t.To, t.Amount
	case *pb.AnyTx_WitnessStakeTx:
		return "", c.WitnessStakeTx.Amount
	case *pb.AnyTx_MinerTx:
		return "", c.MinerTx.Amount
	case *pb.AnyTx_FreezeTx:
		return c.FreezeTx.TargetAddr, ""
	case *pb.AnyTx_OrderTx:
		o := c.OrderTx
		if o.Price != "" && o.Amount != "" {
			return "", o.Amount + " @ " + o.Price
		}
		return "", o.Amount
	case *pb.AnyTx_FrostWithdrawRequestTx:
		f := c.FrostWithdrawRequestTx
		return f.To, f.Amount
	}
	return "", ""
}
