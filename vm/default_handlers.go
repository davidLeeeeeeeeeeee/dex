package vm

// RegisterDefaultHandlers 注册所有默认的交易处理器
func RegisterDefaultHandlers(reg *HandlerRegistry) error {
	handlers := []TxHandler{
		&IssueTokenTxHandler{}, // 发币交易
		&FreezeTxHandler{},     // 冻结/解冻Token交易
		&TransferTxHandler{},   // 转账交易
		&OrderTxHandler{},      // 订单交易
		&CandidateTxHandler{},  // 委托人投票交易
		&MinerTxHandler{},      // 矿工交易
		// Witness 相关交易处理器
		&WitnessStakeTxHandler{},       // 见证者质押/解质押
		&WitnessRequestTxHandler{},     // 入账见证请求
		&WitnessVoteTxHandler{},        // 见证投票
		&WitnessChallengeTxHandler{},   // 挑战
		&ArbitrationVoteTxHandler{},    // 仲裁投票
		&WitnessClaimRewardTxHandler{}, // 领取奖励
		// Frost 相关交易处理器
		&FrostWithdrawRequestTxHandler{}, // Frost 提现请求
		&FrostWithdrawSignedTxHandler{},  // Frost 提现签名完成
		// Frost DKG/轮换相关交易处理器
		&FrostVaultDkgCommitTxHandler{},           // DKG 承诺点上链
		&FrostVaultDkgShareTxHandler{},            // DKG 加密 share 上链
		&FrostVaultDkgComplaintTxHandler{},        // DKG 投诉
		&FrostVaultDkgRevealTxHandler{},           // DKG reveal
		&FrostVaultDkgValidationSignedTxHandler{}, // DKG 验证签名
		&FrostVaultTransitionSignedTxHandler{},    // Vault 迁移签名
	}

	for _, h := range handlers {
		if err := reg.Register(h); err != nil {
			return err
		}
	}

	return nil
}
