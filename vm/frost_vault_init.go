// vm/frost_vault_init.go
// Frost Vault 初始化：VaultConfig 和 VaultState 的链上初始化逻辑
package vm

import (
	"dex/frost/runtime/committee"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"
)

// InitVaultConfig 初始化链级别的 VaultConfig
// 通常在系统启动时或通过治理交易调用
func InitVaultConfig(sv StateView, chain string, cfg *pb.FrostVaultConfig) error {
	if cfg == nil {
		return errors.New("vault config is nil")
	}

	// 验证配置参数
	if cfg.VaultCount == 0 {
		return errors.New("vault_count must be greater than 0")
	}
	if cfg.CommitteeSize == 0 {
		return errors.New("committee_size must be greater than 0")
	}
	if cfg.ThresholdRatio <= 0 || cfg.ThresholdRatio > 1 {
		return errors.New("threshold_ratio must be between 0 and 1")
	}

	// 检查是否已存在
	vaultCfgKey := keys.KeyFrostVaultConfig(chain, 0)
	existingData, exists, err := sv.Get(vaultCfgKey)
	if err != nil {
		return fmt.Errorf("failed to check existing config: %w", err)
	}
	if exists && len(existingData) > 0 {
		// 已存在，幂等返回
		logs.Debug("[InitVaultConfig] config already exists for chain %s", chain)
		return nil
	}

	// 设置 chain
	cfg.Chain = chain

	// 序列化并写入
	cfgData, err := proto.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal vault config: %w", err)
	}

	sv.Set(vaultCfgKey, cfgData)
	logs.Info("[InitVaultConfig] initialized vault config for chain %s: vault_count=%d committee_size=%d",
		chain, cfg.VaultCount, cfg.CommitteeSize)

	return nil
}

// InitVaultStates 初始化所有 Vault 的初始状态
// 从 Top10000 确定性分配到各 Vault，并创建初始 VaultState
func InitVaultStates(sv StateView, chain string, epochID uint64) error {
	// 1. 读取 VaultConfig
	vaultCfgKey := keys.KeyFrostVaultConfig(chain, 0)
	cfgData, exists, err := sv.Get(vaultCfgKey)
	if err != nil || !exists || len(cfgData) == 0 {
		return fmt.Errorf("vault config not found for chain %s, please init vault config first", chain)
	}

	var cfg pb.FrostVaultConfig
	if err := proto.Unmarshal(cfgData, &cfg); err != nil {
		return fmt.Errorf("failed to parse vault config: %w", err)
	}

	// 2. 读取 Top10000
	top10000Key := keys.KeyFrostTop10000()
	top10000Data, exists, err := sv.Get(top10000Key)
	if err != nil || !exists || len(top10000Data) == 0 {
		return fmt.Errorf("top10000 not found, please ensure top10000 is initialized")
	}

	var top10000 pb.FrostTop10000
	if err := proto.Unmarshal(top10000Data, &top10000); err != nil {
		return fmt.Errorf("failed to parse top10000: %w", err)
	}

	// 3. 确定性分配委员会到各 Vault
	seed := committee.ComputeSeed(epochID, chain)

	// 提取矿工索引列表（Top10000 的 indices 已经是按 bit index 排序的）
	minerIndices := top10000.Indices
	if len(minerIndices) == 0 {
		return fmt.Errorf("top10000 has no miner indices")
	}

	// 排序（确保顺序）
	sortedIndices := committee.SortByBitIndex(minerIndices)

	// 分配到各 Vault
	vaultCommittees := committee.AssignToVaults(sortedIndices, seed, int(cfg.VaultCount), int(cfg.CommitteeSize))

	// 4. 为每个 Vault 创建初始 VaultState
	for vaultID := uint32(0); vaultID < cfg.VaultCount; vaultID++ {
		vaultStateKey := keys.KeyFrostVaultState(chain, vaultID)
		existingData, exists, err := sv.Get(vaultStateKey)
		if err != nil {
			logs.Warn("[InitVaultStates] failed to check existing vault state for vault %d: %v", vaultID, err)
			continue
		}
		if exists && len(existingData) > 0 {
			// 已存在，跳过（幂等）
			logs.Debug("[InitVaultStates] vault state already exists for chain=%s vault=%d", chain, vaultID)
			continue
		}

		// 获取该 Vault 的委员会成员
		var committeeMembers []string
		if int(vaultID) < len(vaultCommittees) {
			// 将 bit index 转换为地址（从 Top10000 的对应数组查找）
			for _, bitIndex := range vaultCommittees[vaultID] {
				// 在 indices 中查找对应的地址
				for i, idx := range top10000.Indices {
					if idx == bitIndex && i < len(top10000.Addresses) {
						committeeMembers = append(committeeMembers, top10000.Addresses[i])
						break
					}
				}
			}
		}

		// 创建初始 VaultState
		vaultState := &pb.FrostVaultState{
			VaultId:          vaultID,
			Chain:            chain,
			KeyEpoch:         epochID,
			SignAlgo:         cfg.SignAlgo,
			Status:           "PENDING", // 初始状态为 PENDING，等待 DKG 完成后变为 KEY_READY
			CommitteeMembers: committeeMembers,
		}

		// 如果配置中有 vault_refs，设置对应的 vault_ref
		if int(vaultID) < len(cfg.VaultRefs) {
			// vault_ref 可以是 BTC 地址或合约地址
			// 这里简化处理，实际应该从 group_pubkey 派生
		}

		// 序列化并写入
		stateData, err := proto.Marshal(vaultState)
		if err != nil {
			logs.Warn("[InitVaultStates] failed to marshal vault state for vault %d: %v", vaultID, err)
			continue
		}

		sv.Set(vaultStateKey, stateData)
		logs.Info("[InitVaultStates] initialized vault state for chain=%s vault=%d committee_size=%d",
			chain, vaultID, len(committeeMembers))
	}

	return nil
}

// UpdateVaultStateAfterDKG 在 DKG 完成后更新 VaultState
// 由 FrostVaultDkgValidationSignedTxHandler 调用
func UpdateVaultStateAfterDKG(sv StateView, chain string, vaultID uint32, epochID uint64, groupPubkey []byte, signAlgo pb.SignAlgo, committeeMembers []string) error {
	vaultKey := keys.KeyFrostVaultState(chain, vaultID)
	vaultData, exists, err := sv.Get(vaultKey)
	if err != nil {
		return fmt.Errorf("failed to read vault state: %w", err)
	}

	var vault *pb.FrostVaultState
	if exists && len(vaultData) > 0 {
		vault, _ = unmarshalFrostVaultState(vaultData)
	}
	if vault == nil {
		// 如果不存在，创建新的
		vault = &pb.FrostVaultState{
			VaultId:  vaultID,
			Chain:    chain,
			SignAlgo: signAlgo,
		}
	}

	// 更新状态
	vault.KeyEpoch = epochID
	vault.GroupPubkey = groupPubkey
	vault.Status = "KEY_READY" // DKG 完成后进入 KEY_READY 状态
	if len(committeeMembers) > 0 {
		vault.CommitteeMembers = committeeMembers
	}

	// 序列化并写入
	updatedData, err := proto.Marshal(vault)
	if err != nil {
		return fmt.Errorf("failed to marshal vault state: %w", err)
	}

	sv.Set(vaultKey, updatedData)
	logs.Info("[UpdateVaultStateAfterDKG] updated vault state: chain=%s vault=%d epoch=%d status=KEY_READY",
		chain, vaultID, epochID)

	return nil
}
