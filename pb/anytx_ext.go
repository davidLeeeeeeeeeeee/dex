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
	case *AnyTx_AddressTx:
		return tx.AddressTx.GetBase()
	case *AnyTx_CandidateTx:
		return tx.CandidateTx.GetBase()
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
