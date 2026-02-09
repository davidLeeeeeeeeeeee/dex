// vm/frost_vault_init.go
// Frost Vault 鍒濆鍖栵細VaultConfig 鍜?VaultState 鐨勯摼涓婂垵濮嬪寲閫昏緫
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

// InitVaultConfig 鍒濆鍖栭摼绾у埆鐨?VaultConfig
// 閫氬父鍦ㄧ郴缁熷惎鍔ㄦ椂鎴栭€氳繃娌荤悊浜ゆ槗璋冪敤
func InitVaultConfig(sv StateView, chain string, cfg *pb.FrostVaultConfig) error {
	if cfg == nil {
		return errors.New("vault config is nil")
	}

	// 楠岃瘉閰嶇疆鍙傛暟
	if cfg.VaultCount == 0 {
		return errors.New("vault_count must be greater than 0")
	}
	if cfg.CommitteeSize == 0 {
		return errors.New("committee_size must be greater than 0")
	}
	if cfg.ThresholdRatio <= 0 || cfg.ThresholdRatio > 1 {
		return errors.New("threshold_ratio must be between 0 and 1")
	}

	// 妫€鏌ユ槸鍚﹀凡瀛樺湪
	vaultCfgKey := keys.KeyFrostVaultConfig(chain, 0)
	existingData, exists, err := sv.Get(vaultCfgKey)
	if err != nil {
		return fmt.Errorf("failed to check existing config: %w", err)
	}
	if exists && len(existingData) > 0 {
		// 宸插瓨鍦紝骞傜瓑杩斿洖
		// logs.Debug("[InitVaultConfig] config already exists for chain %s", chain)
		return nil
	}

	// 璁剧疆 chain
	cfg.Chain = chain

	// 搴忓垪鍖栧苟鍐欏叆
	cfgData, err := proto.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal vault config: %w", err)
	}

	sv.Set(vaultCfgKey, cfgData)
	// logs.Info("[InitVaultConfig] initialized vault config for chain %s: vault_count=%d committee_size=%d", chain, cfg.VaultCount, cfg.CommitteeSize)

	return nil
}

// InitVaultStates 鍒濆鍖栨墍鏈?Vault 鐨勫垵濮嬬姸鎬?
// 浠?Top10000 纭畾鎬у垎閰嶅埌鍚?Vault锛屽苟鍒涘缓鍒濆 VaultState
func InitVaultStates(sv StateView, chain string, epochID uint64) error {
	// 1. 璇诲彇 VaultConfig
	vaultCfgKey := keys.KeyFrostVaultConfig(chain, 0)
	cfgData, exists, err := sv.Get(vaultCfgKey)
	if err != nil || !exists || len(cfgData) == 0 {
		return fmt.Errorf("vault config not found for chain %s, please init vault config first", chain)
	}

	var cfg pb.FrostVaultConfig
	if err := unmarshalProtoCompat(cfgData, &cfg); err != nil {
		return fmt.Errorf("failed to parse vault config: %w", err)
	}

	// 2. 璇诲彇 Top10000
	top10000Key := keys.KeyFrostTop10000()
	top10000Data, exists, err := sv.Get(top10000Key)
	if err != nil || !exists || len(top10000Data) == 0 {
		return fmt.Errorf("top10000 not found, please ensure top10000 is initialized")
	}

	var top10000 pb.FrostTop10000
	if err := unmarshalProtoCompat(top10000Data, &top10000); err != nil {
		return fmt.Errorf("failed to parse top10000: %w", err)
	}

	// 3. 纭畾鎬у垎閰嶅鍛樹細鍒板悇 Vault
	seed := committee.ComputeSeed(epochID, chain)

	// 鎻愬彇鐭垮伐绱㈠紩鍒楄〃锛圱op10000 鐨?indices 宸茬粡鏄寜 bit index 鎺掑簭鐨勶級
	minerIndices := top10000.Indices
	if len(minerIndices) == 0 {
		return fmt.Errorf("top10000 has no miner indices")
	}

	// 鎺掑簭锛堢‘淇濋『搴忥級
	sortedIndices := committee.SortByBitIndex(minerIndices)

	// 鍒嗛厤鍒板悇 Vault
	vaultCommittees := committee.AssignToVaults(sortedIndices, seed, int(cfg.VaultCount), int(cfg.CommitteeSize))

	// 4. 涓烘瘡涓?Vault 鍒涘缓鍒濆 VaultState
	for vaultID := uint32(0); vaultID < cfg.VaultCount; vaultID++ {
		vaultStateKey := keys.KeyFrostVaultState(chain, vaultID)
		existingData, exists, err := sv.Get(vaultStateKey)
		if err != nil {
			logs.Warn("[InitVaultStates] failed to check existing vault state for vault %d: %v", vaultID, err)
			continue
		}
		if exists && len(existingData) > 0 {
			// 宸插瓨鍦紝璺宠繃锛堝箓绛夛級
			logs.Debug("[InitVaultStates] vault state already exists for chain=%s vault=%d", chain, vaultID)
			continue
		}

		// 鑾峰彇璇?Vault 鐨勫鍛樹細鎴愬憳
		var committeeMembers []string
		if int(vaultID) < len(vaultCommittees) {
			// 灏?bit index 杞崲涓哄湴鍧€锛堜粠 Top10000 鐨勫搴旀暟缁勬煡鎵撅級
			for _, bitIndex := range vaultCommittees[vaultID] {
				// 鍦?indices 涓煡鎵惧搴旂殑鍦板潃
				for i, idx := range top10000.Indices {
					if idx == bitIndex && i < len(top10000.Addresses) {
						committeeMembers = append(committeeMembers, top10000.Addresses[i])
						break
					}
				}
			}
		}

		// 鍒涘缓鍒濆 VaultState
		vaultState := &pb.FrostVaultState{
			VaultId:          vaultID,
			Chain:            chain,
			KeyEpoch:         epochID,
			SignAlgo:         cfg.SignAlgo,
			Status:           "PENDING", // 鍒濆鐘舵€佷负 PENDING锛岀瓑寰?DKG 瀹屾垚鍚庡彉涓?KEY_READY
			CommitteeMembers: committeeMembers,
		}

		// 濡傛灉閰嶇疆涓湁 vault_refs锛岃缃搴旂殑 vault_ref
		if int(vaultID) < len(cfg.VaultRefs) {
			// vault_ref 鍙互鏄?BTC 鍦板潃鎴栧悎绾﹀湴鍧€
			// 杩欓噷绠€鍖栧鐞嗭紝瀹為檯搴旇浠?group_pubkey 娲剧敓
		}

		// 搴忓垪鍖栧苟鍐欏叆
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

// InitVaultTransitions 鍒濆鍖?VaultTransitionState锛堢敤浜庡惎鍔ㄥ垵濮?DKG锛?
// 渚濊禆 VaultConfig/VaultState/Top10000 宸插氨缁紝epochID 涓虹洰鏍?epoch
func InitVaultTransitions(sv StateView, chain string, epochID uint64, triggerHeight uint64, commitWindow uint64, sharingWindow uint64, disputeWindow uint64) error {
	// 1. 璇诲彇 VaultConfig
	vaultCfgKey := keys.KeyFrostVaultConfig(chain, 0)
	cfgData, exists, err := sv.Get(vaultCfgKey)
	if err != nil || !exists || len(cfgData) == 0 {
		return fmt.Errorf("vault config not found for chain %s, please init vault config first", chain)
	}

	var cfg pb.FrostVaultConfig
	if err := unmarshalProtoCompat(cfgData, &cfg); err != nil {
		return fmt.Errorf("failed to parse vault config: %w", err)
	}

	// 2. 涓烘瘡涓?Vault 鍒涘缓鍒濆 transition锛堝箓绛夛級
	for vaultID := uint32(0); vaultID < cfg.VaultCount; vaultID++ {
		vaultKey := keys.KeyFrostVaultState(chain, vaultID)
		vaultData, vaultExists, err := sv.Get(vaultKey)
		if err != nil {
			logs.Warn("[InitVaultTransitions] failed to read vault state for chain=%s vault=%d: %v", chain, vaultID, err)
			continue
		}

		var vaultState pb.FrostVaultState
		if vaultExists && len(vaultData) > 0 {
			if err := unmarshalProtoCompat(vaultData, &vaultState); err != nil {
				logs.Warn("[InitVaultTransitions] failed to parse vault state for chain=%s vault=%d: %v", chain, vaultID, err)
			}
		}

		effectiveEpoch := epochID
		if effectiveEpoch == 0 {
			if vaultState.KeyEpoch > 0 {
				effectiveEpoch = vaultState.KeyEpoch
			} else {
				effectiveEpoch = 1
			}
		}

		transitionKey := keys.KeyFrostVaultTransition(chain, vaultID, effectiveEpoch)
		existingData, exists, err := sv.Get(transitionKey)
		if err != nil {
			logs.Warn("[InitVaultTransitions] failed to check transition for chain=%s vault=%d: %v", chain, vaultID, err)
			continue
		}
		if exists && len(existingData) > 0 {
			continue // already exists
		}

		committeeMembers := vaultState.CommitteeMembers
		if len(committeeMembers) == 0 {
			logs.Warn("[InitVaultTransitions] empty committee for chain=%s vault=%d, skip transition", chain, vaultID)
			continue
		}

		threshold := int(float64(len(committeeMembers))*float64(cfg.ThresholdRatio) + 0.5)
		if threshold < 1 {
			threshold = 1
		}

		commitDeadline := triggerHeight + commitWindow - 1
		sharingDeadline := commitDeadline + sharingWindow
		disputeDeadline := sharingDeadline + disputeWindow

		logs.Info("[InitVaultTransitions] logic: trigger(%d) + window(%d) - 1 = deadline(%d)", triggerHeight, commitWindow, commitDeadline)

		transition := &pb.VaultTransitionState{
			Chain:               chain,
			VaultId:             vaultID,
			EpochId:             effectiveEpoch,
			SignAlgo:            cfg.SignAlgo,
			TriggerHeight:       triggerHeight,
			OldCommitteeMembers: committeeMembers,
			NewCommitteeMembers: committeeMembers,
			DkgStatus:           DKGStatusCommitting,
			DkgSessionId:        fmt.Sprintf("%s_%d_%d", chain, vaultID, effectiveEpoch),
			DkgThresholdT:       uint32(threshold),
			DkgN:                uint32(len(committeeMembers)),
			DkgCommitDeadline:   commitDeadline,
			DkgSharingDeadline:  sharingDeadline,
			DkgDisputeDeadline:  disputeDeadline,
			OldGroupPubkey:      vaultState.GroupPubkey,
			ValidationStatus:    "NOT_STARTED",
			Lifecycle:           VaultLifecycleActive,
		}

		transitionData, err := proto.Marshal(transition)
		if err != nil {
			logs.Warn("[InitVaultTransitions] failed to marshal transition for chain=%s vault=%d: %v", chain, vaultID, err)
			continue
		}

		sv.Set(transitionKey, transitionData)
		logs.Info("[InitVaultTransitions] initialized transition for chain=%s vault=%d epoch=%d committee=%d",
			chain, vaultID, effectiveEpoch, len(committeeMembers))
	}

	return nil
}

// UpdateVaultStateAfterDKG 鍦?DKG 瀹屾垚鍚庢洿鏂?VaultState
// 鐢?FrostVaultDkgValidationSignedTxHandler 璋冪敤
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
		// 濡傛灉涓嶅瓨鍦紝鍒涘缓鏂扮殑
		vault = &pb.FrostVaultState{
			VaultId:  vaultID,
			Chain:    chain,
			SignAlgo: signAlgo,
		}
	}

	// 鏇存柊鐘舵€?
	vault.KeyEpoch = epochID
	vault.GroupPubkey = groupPubkey
	vault.Status = "KEY_READY" // DKG 瀹屾垚鍚庤繘鍏?KEY_READY 鐘舵€?
	if len(committeeMembers) > 0 {
		vault.CommitteeMembers = committeeMembers
	}

	// 搴忓垪鍖栧苟鍐欏叆
	updatedData, err := proto.Marshal(vault)
	if err != nil {
		return fmt.Errorf("failed to marshal vault state: %w", err)
	}

	sv.Set(vaultKey, updatedData)
	logs.Info("[UpdateVaultStateAfterDKG] updated vault state: chain=%s vault=%d epoch=%d status=KEY_READY",
		chain, vaultID, epochID)

	return nil
}
