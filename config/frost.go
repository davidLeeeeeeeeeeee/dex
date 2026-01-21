package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// FrostConfig FROST门限签名配置（参考 frost/design.md §10）
type FrostConfig struct {
	Enabled    bool                        `json:"enabled"` // 是否启用 FROST 模块（默认 false）
	Committee  CommitteeConfig             `json:"committee"`
	Vault      VaultConfig                 `json:"vault"`
	Timeouts   TimeoutsConfig              `json:"timeouts"`
	Withdraw   WithdrawConfig              `json:"withdraw"`
	Transition TransitionConfig            `json:"transition"`
	Chains     map[string]ChainFrostConfig `json:"chains"`
}

// CommitteeConfig 委员会配置
type CommitteeConfig struct {
	TopN           int     `json:"topN"`           // Top N 矿工参与签名（默认10000）
	ThresholdRatio float64 `json:"thresholdRatio"` // 门限比例（默认0.8）
	EpochBlocks    int     `json:"epochBlocks"`    // 每个epoch的区块数（默认200000）
}

// VaultConfig Vault 分片配置
type VaultConfig struct {
	DefaultK       int     `json:"defaultK"`       // 每个 Vault 默认委员会规模（默认100）
	MinK           int     `json:"minK"`           // 最小委员会规模（默认50）
	Count          int     `json:"count"`          // 每个链的 Vault 数量（默认100）
	MaxK           int     `json:"maxK"`           // 最大委员会规模（默认500）
	ThresholdRatio float64 `json:"thresholdRatio"` // Vault 门限比例（默认0.67）
}

// TimeoutsConfig 超时配置（以区块数为单位）
type TimeoutsConfig struct {
	NonceCommitBlocks      int `json:"nonceCommitBlocks"`      // Nonce承诺超时（默认2）
	SigShareBlocks         int `json:"sigShareBlocks"`         // 签名份额超时（默认3）
	AggregatorRotateBlocks int `json:"aggregatorRotateBlocks"` // 聚合者轮换超时（默认5）
	SessionMaxBlocks       int `json:"sessionMaxBlocks"`       // 会话最大超时（默认60）
}

// WithdrawConfig 提现配置
type WithdrawConfig struct {
	MaxInFlightPerChainAsset int               `json:"maxInFlightPerChainAsset"` // 每(chain,asset)最大并发job数（默认1）
	RetryPolicy              RetryPolicyConfig `json:"retryPolicy"`
}

// RetryPolicyConfig 重试策略配置
type RetryPolicyConfig struct {
	MaxRetry      int `json:"maxRetry"`      // 最大重试次数（默认5）
	BackoffBlocks int `json:"backoffBlocks"` // 退避区块数（默认5）
}

// TransitionConfig 轮换/DKG 配置
type TransitionConfig struct {
	TriggerChangeRatio        float64 `json:"triggerChangeRatio"`        // 触发轮换的委员会变化比例（默认0.2）
	PauseWithdrawDuringSwitch bool    `json:"pauseWithdrawDuringSwitch"` // 轮换期间暂停提现（默认true）
	DkgCommitWindowBlocks     int     `json:"dkgCommitWindowBlocks"`     // DKG承诺窗口（默认2000）
	DkgSharingWindowBlocks    int     `json:"dkgSharingWindowBlocks"`    // DKG份额分发窗口（默认2000）
	DkgDisputeWindowBlocks    int     `json:"dkgDisputeWindowBlocks"`    // DKG争议窗口（默认2000）
}

// ChainFrostConfig 链级别 FROST 配置
type ChainFrostConfig struct {
	SignAlgo        string `json:"signAlgo"`                  // 签名算法（pb.SignAlgo枚举）
	FrostVariant    string `json:"frostVariant,omitempty"`    // FROST变体名称（null表示不使用FROST）
	ThresholdScheme string `json:"thresholdScheme,omitempty"` // 门限方案（用于非FROST链，如TRX）
	VaultsPerChain  int    `json:"vaultsPerChain"`            // 每条链的Vault数量

	// BTC 特有配置
	FeeSatsPerVByte int `json:"feeSatsPerVByte,omitempty"` // BTC 费率（sat/vByte）

	// EVM 链（ETH/BNB）配置
	GasPriceGwei int `json:"gasPriceGwei,omitempty"` // Gas 价格（Gwei）
	GasLimit     int `json:"gasLimit,omitempty"`     // Gas 限制

	// TRX 配置
	FeeLimitSun int `json:"feeLimitSun,omitempty"` // TRX 费用限制（Sun）

	// SOL 配置
	PriorityFeeMicroLamports int `json:"priorityFeeMicroLamports,omitempty"` // SOL 优先费（微lamports）
}

// DefaultFrostConfig 返回 FrostConfig 默认值
func DefaultFrostConfig() FrostConfig {
	return FrostConfig{
		Enabled: true, // 默认开启
		Committee: CommitteeConfig{
			TopN:           10000,
			ThresholdRatio: 0.8,
			EpochBlocks:    200000,
		},
		Vault: VaultConfig{
			DefaultK:       100,
			MinK:           50,
			MaxK:           500,
			ThresholdRatio: 0.67,
			Count:          100,
		},
		Timeouts: TimeoutsConfig{
			NonceCommitBlocks:      2,
			SigShareBlocks:         3,
			AggregatorRotateBlocks: 5,
			SessionMaxBlocks:       60,
		},
		Withdraw: WithdrawConfig{
			MaxInFlightPerChainAsset: 1,
			RetryPolicy: RetryPolicyConfig{
				MaxRetry:      5,
				BackoffBlocks: 5,
			},
		},
		Transition: TransitionConfig{
			TriggerChangeRatio:        0.2,
			PauseWithdrawDuringSwitch: true,
			DkgCommitWindowBlocks:     20,
			DkgSharingWindowBlocks:    20,
			DkgDisputeWindowBlocks:    20,
		},
		Chains: map[string]ChainFrostConfig{
			"btc": {
				SignAlgo:        "SCHNORR_SECP256K1_BIP340",
				FrostVariant:    "frost-secp256k1",
				VaultsPerChain:  100,
				FeeSatsPerVByte: 25,
			},
			"eth": {
				SignAlgo:       "SCHNORR_SECP256K1_BIP340",
				FrostVariant:   "frost-bn128",
				VaultsPerChain: 100,
				GasPriceGwei:   30,
				GasLimit:       180000,
			},
			"bnb": {
				SignAlgo:       "SCHNORR_SECP256K1_BIP340",
				FrostVariant:   "frost-bn128",
				VaultsPerChain: 100,
				GasPriceGwei:   3,
				GasLimit:       180000,
			},
			"trx": {
				SignAlgo:        "SCHNORR_SECP256K1_BIP340",
				ThresholdScheme: "gg20 or cggmp (v1 not supported)",
				VaultsPerChain:  100,
				FeeLimitSun:     30000000,
			},
			"sol": {
				SignAlgo:                 "SCHNORR_SECP256K1_BIP340",
				FrostVariant:             "frost-ed25519",
				VaultsPerChain:           100,
				PriorityFeeMicroLamports: 2000,
			},
		},
	}
}

// LoadFrostConfig 从文件加载 FrostConfig
func LoadFrostConfig(path string) (FrostConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultFrostConfig(), nil
		}
		return FrostConfig{}, fmt.Errorf("failed to read frost config: %w", err)
	}

	var cfg FrostConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return FrostConfig{}, fmt.Errorf("failed to parse frost config: %w", err)
	}

	return cfg, nil
}
