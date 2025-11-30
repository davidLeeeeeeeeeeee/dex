package vm

// RegisterDefaultHandlers 注册所有默认的交易处理器
func RegisterDefaultHandlers(reg *HandlerRegistry) error {
	handlers := []TxHandler{
		&IssueTokenTxHandler{},  // 发币交易
		&FreezeTxHandler{},      // 冻结/解冻Token交易
		&TransferTxHandler{},    // 转账交易
		&OrderTxHandler{},       // 订单交易
		&CandidateTxHandler{},   // 委托人投票交易
		&MinerTxHandler{},       // 矿工交易
		// Witness 相关交易处理器
		&WitnessStakeTxHandler{},      // 见证者质押/解质押
		&WitnessRequestTxHandler{},    // 入账见证请求
		&WitnessVoteTxHandler{},       // 见证投票
		&WitnessChallengeTxHandler{},  // 挑战
		&ArbitrationVoteTxHandler{},   // 仲裁投票
		&WitnessClaimRewardTxHandler{}, // 领取奖励
	}

	for _, h := range handlers {
		if err := reg.Register(h); err != nil {
			return err
		}
	}

	return nil
}
