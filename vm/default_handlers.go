package vm

import (
	"dex/config"
	"dex/witness"
)

// RegisterDefaultHandlers 注册所有默认的交易处理器
// 为了兼容旧的测试代码，使用变长参数
// 推荐用法: RegisterDefaultHandlers(reg, cfg, witnessSvc)
func RegisterDefaultHandlers(reg *HandlerRegistry, args ...interface{}) error {
	var cfg *config.Config
	var witnessSvc *witness.Service

	for _, arg := range args {
		switch v := arg.(type) {
		case *config.Config:
			cfg = v
		case *witness.Service:
			witnessSvc = v
		}
	}

	handlers := []TxHandler{
		&IssueTokenTxHandler{}, // 发币交易
		&FreezeTxHandler{},     // 冻结/解冻Token交易
		&TransferTxHandler{},   // 转账交易
		&OrderTxHandler{},      // 订单交易
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
		// 如果 handler 需要 WitnessService，设置它
		if awareness, ok := h.(WitnessServiceAware); ok && witnessSvc != nil {
			awareness.SetWitnessService(witnessSvc)
		}

		// 如果是 WitnessRequestTxHandler，设置 VaultCount
		if wh, ok := h.(*WitnessRequestTxHandler); ok && cfg != nil {
			wh.VaultCount = uint32(cfg.Frost.Vault.Count)
		} else if wh, ok := h.(*WitnessRequestTxHandler); ok {
			// 默认值保证兼容性
			wh.VaultCount = 100
		}

		if err := reg.Register(h); err != nil {
			return err
		}
	}

	return nil
}
