package config

import (
	"testing"
)

func TestDefaultFrostConfig(t *testing.T) {
	cfg := DefaultFrostConfig()

	// 测试 Enabled 默认为 false
	if cfg.Enabled {
		t.Error("Enabled should be false by default")
	}

	// 测试 Committee 默认值
	if cfg.Committee.TopN != 10000 {
		t.Errorf("Committee.TopN = %d, want 10000", cfg.Committee.TopN)
	}
	if cfg.Committee.ThresholdRatio != 0.8 {
		t.Errorf("Committee.ThresholdRatio = %f, want 0.8", cfg.Committee.ThresholdRatio)
	}
	if cfg.Committee.EpochBlocks != 200000 {
		t.Errorf("Committee.EpochBlocks = %d, want 200000", cfg.Committee.EpochBlocks)
	}

	// 测试 Vault 默认值
	if cfg.Vault.DefaultK != 100 {
		t.Errorf("Vault.DefaultK = %d, want 100", cfg.Vault.DefaultK)
	}
	if cfg.Vault.MinK != 50 {
		t.Errorf("Vault.MinK = %d, want 50", cfg.Vault.MinK)
	}
	if cfg.Vault.MaxK != 500 {
		t.Errorf("Vault.MaxK = %d, want 500", cfg.Vault.MaxK)
	}
	if cfg.Vault.ThresholdRatio != 0.67 {
		t.Errorf("Vault.ThresholdRatio = %f, want 0.67", cfg.Vault.ThresholdRatio)
	}

	// 测试 Timeouts 默认值
	if cfg.Timeouts.NonceCommitBlocks != 2 {
		t.Errorf("Timeouts.NonceCommitBlocks = %d, want 2", cfg.Timeouts.NonceCommitBlocks)
	}
	if cfg.Timeouts.SigShareBlocks != 3 {
		t.Errorf("Timeouts.SigShareBlocks = %d, want 3", cfg.Timeouts.SigShareBlocks)
	}
	if cfg.Timeouts.AggregatorRotateBlocks != 5 {
		t.Errorf("Timeouts.AggregatorRotateBlocks = %d, want 5", cfg.Timeouts.AggregatorRotateBlocks)
	}
	if cfg.Timeouts.SessionMaxBlocks != 60 {
		t.Errorf("Timeouts.SessionMaxBlocks = %d, want 60", cfg.Timeouts.SessionMaxBlocks)
	}

	// 测试 Withdraw 默认值
	if cfg.Withdraw.MaxInFlightPerChainAsset != 1 {
		t.Errorf("Withdraw.MaxInFlightPerChainAsset = %d, want 1", cfg.Withdraw.MaxInFlightPerChainAsset)
	}
	if cfg.Withdraw.RetryPolicy.MaxRetry != 5 {
		t.Errorf("Withdraw.RetryPolicy.MaxRetry = %d, want 5", cfg.Withdraw.RetryPolicy.MaxRetry)
	}
	if cfg.Withdraw.RetryPolicy.BackoffBlocks != 5 {
		t.Errorf("Withdraw.RetryPolicy.BackoffBlocks = %d, want 5", cfg.Withdraw.RetryPolicy.BackoffBlocks)
	}

	// 测试 Transition 默认值
	if cfg.Transition.TriggerChangeRatio != 0.2 {
		t.Errorf("Transition.TriggerChangeRatio = %f, want 0.2", cfg.Transition.TriggerChangeRatio)
	}
	if !cfg.Transition.PauseWithdrawDuringSwitch {
		t.Error("Transition.PauseWithdrawDuringSwitch should be true")
	}
	if cfg.Transition.DkgCommitWindowBlocks != 2000 {
		t.Errorf("Transition.DkgCommitWindowBlocks = %d, want 2000", cfg.Transition.DkgCommitWindowBlocks)
	}
	if cfg.Transition.DkgDisputeWindowBlocks != 2000 {
		t.Errorf("Transition.DkgDisputeWindowBlocks = %d, want 2000", cfg.Transition.DkgDisputeWindowBlocks)
	}
}

func TestDefaultFrostConfigChains(t *testing.T) {
	cfg := DefaultFrostConfig()

	// 确保包含所有 5 条链
	expectedChains := []string{"btc", "eth", "bnb", "trx", "sol"}
	if len(cfg.Chains) != len(expectedChains) {
		t.Errorf("Chains count = %d, want %d", len(cfg.Chains), len(expectedChains))
	}
	for _, chain := range expectedChains {
		if _, ok := cfg.Chains[chain]; !ok {
			t.Errorf("missing chain config for %s", chain)
		}
	}

	// 测试 BTC 配置
	btc := cfg.Chains["btc"]
	if btc.SignAlgo != "SCHNORR_SECP256K1_BIP340" {
		t.Errorf("btc.SignAlgo = %s, want SCHNORR_SECP256K1_BIP340", btc.SignAlgo)
	}
	if btc.FrostVariant != "frost-secp256k1" {
		t.Errorf("btc.FrostVariant = %s, want frost-secp256k1", btc.FrostVariant)
	}
	if btc.VaultsPerChain != 100 {
		t.Errorf("btc.VaultsPerChain = %d, want 100", btc.VaultsPerChain)
	}
	if btc.FeeSatsPerVByte != 25 {
		t.Errorf("btc.FeeSatsPerVByte = %d, want 25", btc.FeeSatsPerVByte)
	}

	// 测试 ETH 配置
	eth := cfg.Chains["eth"]
	if eth.SignAlgo != "SCHNORR_ALT_BN128" {
		t.Errorf("eth.SignAlgo = %s, want SCHNORR_ALT_BN128", eth.SignAlgo)
	}
	if eth.GasPriceGwei != 30 {
		t.Errorf("eth.GasPriceGwei = %d, want 30", eth.GasPriceGwei)
	}
	if eth.GasLimit != 180000 {
		t.Errorf("eth.GasLimit = %d, want 180000", eth.GasLimit)
	}

	// 测试 SOL 配置
	sol := cfg.Chains["sol"]
	if sol.SignAlgo != "ED25519" {
		t.Errorf("sol.SignAlgo = %s, want ED25519", sol.SignAlgo)
	}
	if sol.PriorityFeeMicroLamports != 2000 {
		t.Errorf("sol.PriorityFeeMicroLamports = %d, want 2000", sol.PriorityFeeMicroLamports)
	}

	// 测试 TRX 配置（非 FROST）
	trx := cfg.Chains["trx"]
	if trx.SignAlgo != "ECDSA_SECP256K1" {
		t.Errorf("trx.SignAlgo = %s, want ECDSA_SECP256K1", trx.SignAlgo)
	}
	if trx.FrostVariant != "" {
		t.Errorf("trx.FrostVariant should be empty, got %s", trx.FrostVariant)
	}
}

func TestDefaultConfigHasFrost(t *testing.T) {
	cfg := DefaultConfig()

	// 确保 DefaultConfig 包含 Frost 配置
	if cfg.Frost.Committee.TopN != 10000 {
		t.Errorf("DefaultConfig().Frost.Committee.TopN = %d, want 10000", cfg.Frost.Committee.TopN)
	}
	if cfg.Frost.Vault.DefaultK != 100 {
		t.Errorf("DefaultConfig().Frost.Vault.DefaultK = %d, want 100", cfg.Frost.Vault.DefaultK)
	}
	if len(cfg.Frost.Chains) != 5 {
		t.Errorf("DefaultConfig().Frost.Chains count = %d, want 5", len(cfg.Frost.Chains))
	}
}
