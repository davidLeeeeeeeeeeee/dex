// frost/runtime/planning/vault_selector.go
// Vault 选择与余额缓存

package planning

import (
	"fmt"
	"strconv"

	"dex/keys"
	"dex/logs"
	"dex/pb"

	"google.golang.org/protobuf/proto"
)

// selectVault 选择 Vault（按 vault_id 升序，检查余额和 lifecycle）
func (p *JobWindowPlanner) selectVault(chain, asset string, amount uint64) (vaultID uint32, keyEpoch uint64, err error) {
	// 读取 VaultConfig 获取 vault_count
	vaultCfgKey := keys.KeyFrostVaultConfig(chain, 0)
	vaultCfgData, exists, err := p.stateReader.Get(vaultCfgKey)
	if err != nil {
		return 0, 1, err
	}

	var vaultCount uint32 = 1 // 默认值
	if exists && len(vaultCfgData) > 0 {
		var cfg pb.FrostVaultConfig
		if err := proto.Unmarshal(vaultCfgData, &cfg); err == nil {
			vaultCount = cfg.VaultCount
			if vaultCount == 0 {
				vaultCount = 1
			}
		}
	}

	// 按 vault_id 升序遍历，选择第一个 ACTIVE 的 Vault
	activeCount := 0
	var maxAvailable uint64
	var maxAvailableVault uint32
	for id := uint32(0); id < vaultCount; id++ {
		vaultStateKey := keys.KeyFrostVaultState(chain, id)
		vaultStateData, exists, err := p.stateReader.Get(vaultStateKey)
		if err != nil {
			continue
		}
		if !exists || len(vaultStateData) == 0 {
			continue
		}

		var state pb.FrostVaultState
		if err := proto.Unmarshal(vaultStateData, &state); err != nil {
			continue
		}

		// 检查 status
		if state.Status != "ACTIVE" {
			continue
		}
		activeCount++

		// 检查该 Vault 的可用余额是否足够
		available, err := p.calculateVaultAvailableBalance(chain, asset, id)
		if err != nil {
			logs.Warn("[JobWindowPlanner] failed to calculate balance for vault %d: %v", id, err)
			continue
		}
		if available > maxAvailable || activeCount == 1 {
			maxAvailable = available
			maxAvailableVault = id
		}

		if available >= amount {
			// 余额足够，返回该 Vault
			return id, state.KeyEpoch, nil
		}
		// 余额不足，继续下一个 Vault
	}

	if activeCount == 0 {
		return 0, 1, fmt.Errorf("select vault failed: no ACTIVE vault for chain=%s (vault_count=%d)", chain, vaultCount)
	}
	return 0, 1, fmt.Errorf(
		"select vault failed: insufficient balance across ACTIVE vaults for %s/%s need=%d max_available=%d max_vault=%d active_count=%d",
		chain, asset, amount, maxAvailable, maxAvailableVault, activeCount,
	)
}

// calculateVaultAvailableBalance 计算 Vault 的可用余额
func (p *JobWindowPlanner) calculateVaultAvailableBalance(chain, asset string, vaultID uint32) (uint64, error) {
	// 帧级缓存（单次 Tick 有效）
	if p.transientCache != nil {
		if cachedBal, ok := p.transientCache[vaultID]; ok {
			return cachedBal, nil
		}
	}

	// 读取聚合可用余额，避免运行时扫描 lot/utxo
	balanceKey := keys.KeyFrostVaultAvailableBalance(chain, asset, vaultID)
	balanceData, exists, err := p.stateReader.Get(balanceKey)
	if err != nil {
		return 0, err
	}
	if !exists || len(balanceData) == 0 {
		if p.transientCache != nil {
			p.transientCache[vaultID] = 0
		}
		return 0, nil
	}

	balance := parseAmount(string(balanceData))
	if p.transientCache != nil {
		p.transientCache[vaultID] = balance
	}
	return balance, nil
}

func (p *JobWindowPlanner) deductVaultBalance(vaultID uint32, amount uint64) {
	if p.transientCache != nil {
		if bal, ok := p.transientCache[vaultID]; ok {
			if bal >= amount {
				p.transientCache[vaultID] = bal - amount
			} else {
				p.transientCache[vaultID] = 0 // 防御性归零
			}
		}
	}
}

// calculateAccountChainBalance 计算账户链 Vault 的可用余额（FIFO lot）
func (p *JobWindowPlanner) calculateAccountChainBalance(chain, asset string, vaultID uint32) (uint64, error) {
	// 从 FundsLedger lot FIFO 累加金额
	// 简化实现：扫描所有 lot，累加金额
	// 实际应该从 head 开始按 FIFO 顺序累加
	lotPrefix := fmt.Sprintf("v1_frost_funds_lot_%s_%s_%d_", chain, asset, vaultID)
	var total uint64
	rechargeKeys := make([]string, 0)

	err := p.stateReader.Scan(lotPrefix, func(k string, v []byte) bool {
		// v 中存储的是 request_id，需要从 RechargeRequest 获取金额
		requestID := string(v)
		if requestID == "" {
			return true
		}
		rechargeKeys = append(rechargeKeys, keys.KeyRechargeRequest(requestID))
		return true
	})

	if err != nil {
		return 0, err
	}

	rechargeValues, err := getMany(p.stateReader, rechargeKeys)
	if err != nil {
		return 0, err
	}

	amountCache := make(map[string]uint64, len(rechargeValues))
	for _, rechargeKey := range rechargeKeys {
		rechargeData, exists := rechargeValues[rechargeKey]
		if !exists || len(rechargeData) == 0 {
			continue
		}
		amount, cached := amountCache[rechargeKey]
		if !cached {
			var recharge pb.RechargeRequest
			if err := proto.Unmarshal(rechargeData, &recharge); err != nil {
				continue
			}
			parsed, err := strconv.ParseUint(recharge.Amount, 10, 64)
			if err != nil {
				continue
			}
			amount = parsed
			amountCache[rechargeKey] = amount
		}
		total += amount
	}

	return total, nil
}
