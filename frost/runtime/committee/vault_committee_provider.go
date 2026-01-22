// frost/runtime/committee/vault_committee_provider.go
// 实现 VaultCommitteeProvider 接口，从链上读取 Top10000 和 VaultConfig

package committee

import (
	"dex/frost/runtime"
	"dex/keys"
	"dex/pb"
	"fmt"

	"google.golang.org/protobuf/proto"
)

// DefaultVaultCommitteeProvider 默认的 VaultCommitteeProvider 实现
// 从 ChainStateReader 读取链上数据
type DefaultVaultCommitteeProvider struct {
	stateReader runtime.ChainStateReader
	cfg         VaultCommitteeProviderConfig
	assigner    *VaultCommitteeAssigner
}

// VaultCommitteeProviderConfig 配置
type VaultCommitteeProviderConfig struct {
	DefaultVaultCount     int     // 默认 Vault 数量
	DefaultCommitteeSize  int     // 默认委员会规模
	DefaultThresholdRatio float64 // 默认门限比例
}

// DefaultVaultCommitteeProviderConfig 返回默认配置
func DefaultVaultCommitteeProviderConfig() VaultCommitteeProviderConfig {
	return VaultCommitteeProviderConfig{
		DefaultVaultCount:     100,
		DefaultCommitteeSize:  100,
		DefaultThresholdRatio: 0.67,
	}
}

// NewDefaultVaultCommitteeProvider 创建 VaultCommitteeProvider
func NewDefaultVaultCommitteeProvider(
	stateReader runtime.ChainStateReader,
	cfg VaultCommitteeProviderConfig,
) *DefaultVaultCommitteeProvider {
	return &DefaultVaultCommitteeProvider{
		stateReader: stateReader,
		cfg:         cfg,
		assigner:    NewVaultCommitteeAssigner(),
	}
}

// VaultCommittee 获取指定 Vault 的委员会成员
func (p *DefaultVaultCommitteeProvider) VaultCommittee(chain string, vaultID uint32, epoch uint64) ([]runtime.SignerInfo, error) {
	// 1. 读取 Top10000 列表
	top10000, err := p.GetTop10000()
	if err != nil {
		return nil, fmt.Errorf("failed to get top10000: %w", err)
	}

	// 2. 读取 VaultConfig（获取 vault_count 和 committee_size）
	vaultCfg, err := p.GetVaultConfig(chain)
	if err != nil {
		// 使用默认配置
		vaultCfg = &pb.FrostVaultConfig{
			Chain:         chain,
			VaultCount:    uint32(p.cfg.DefaultVaultCount),
			CommitteeSize: uint32(p.cfg.DefaultCommitteeSize),
		}
	}

	// 3. 使用 VaultCommitteeAssigner 分配委员会
	indices := GetVaultCommittee(
		top10000.Indices,
		epoch,
		chain,
		vaultID,
		int(vaultCfg.VaultCount),
		int(vaultCfg.CommitteeSize),
	)

	// 4. 将索引转换为 SignerInfo
	signers := make([]runtime.SignerInfo, 0, len(indices))
	for _, idx := range indices {
		signer := p.indexToSignerInfo(top10000, idx)
		signers = append(signers, signer)
	}

	return signers, nil
}

// VaultCurrentEpoch 获取指定 Vault 的当前 epoch
func (p *DefaultVaultCommitteeProvider) VaultCurrentEpoch(chain string, vaultID uint32) uint64 {
	vaultState, err := p.GetVaultState(chain, vaultID)
	if err != nil || vaultState == nil {
		return 1 // 默认 epoch
	}
	return vaultState.KeyEpoch
}

// VaultGroupPubkey 获取指定 Vault 的 group_pubkey
func (p *DefaultVaultCommitteeProvider) VaultGroupPubkey(chain string, vaultID uint32, epoch uint64) ([]byte, error) {
	vaultState, err := p.GetVaultState(chain, vaultID)
	if err != nil {
		return nil, fmt.Errorf("failed to get vault state: %w", err)
	}

	if vaultState.KeyEpoch != epoch {
		return nil, fmt.Errorf("epoch mismatch: expected %d, got %d", epoch, vaultState.KeyEpoch)
	}

	return vaultState.GroupPubkey, nil
}

// GetTop10000 从链上读取 Top10000 矿工列表
func (p *DefaultVaultCommitteeProvider) GetTop10000() (*pb.FrostTop10000, error) {
	key := keys.KeyFrostTop10000()
	data, exists, err := p.stateReader.Get(key)
	if err != nil {
		return nil, fmt.Errorf("failed to read top10000: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("top10000 not found")
	}

	var top10000 pb.FrostTop10000
	if err := proto.Unmarshal(data, &top10000); err != nil {
		return nil, fmt.Errorf("failed to unmarshal top10000: %w", err)
	}

	return &top10000, nil
}

// GetVaultConfig 从链上读取链级别的 VaultConfig
func (p *DefaultVaultCommitteeProvider) GetVaultConfig(chain string) (*pb.FrostVaultConfig, error) {
	// 使用 vaultID=0 读取链级别配置（实际上是第一个 Vault 的配置）
	key := keys.KeyFrostVaultConfig(chain, 0)
	data, exists, err := p.stateReader.Get(key)
	if err != nil {
		return nil, fmt.Errorf("failed to read vault config: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("vault config not found for chain %s", chain)
	}

	var cfg pb.FrostVaultConfig
	if err := proto.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal vault config: %w", err)
	}

	return &cfg, nil
}

// GetVaultState 从链上读取 VaultState
func (p *DefaultVaultCommitteeProvider) GetVaultState(chain string, vaultID uint32) (*pb.FrostVaultState, error) {
	key := keys.KeyFrostVaultState(chain, vaultID)
	data, exists, err := p.stateReader.Get(key)
	if err != nil {
		return nil, fmt.Errorf("failed to read vault state: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("vault state not found for %s/%d", chain, vaultID)
	}

	var state pb.FrostVaultState
	if err := proto.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal vault state: %w", err)
	}

	return &state, nil
}

// indexToSignerInfo 将矿工索引转换为 SignerInfo
func (p *DefaultVaultCommitteeProvider) indexToSignerInfo(top10000 *pb.FrostTop10000, idx uint64) runtime.SignerInfo {
	// 在 Top10000 中查找对应索引的信息
	for i, minerIdx := range top10000.Indices {
		if minerIdx == idx {
			signer := runtime.SignerInfo{
				Index: uint32(idx),
			}
			if i < len(top10000.Addresses) {
				signer.ID = runtime.NodeID(top10000.Addresses[i])
			}
			if i < len(top10000.PublicKeys) {
				signer.PublicKey = top10000.PublicKeys[i]
			}
			return signer
		}
	}

	// 未找到时返回仅包含索引的信息
	return runtime.SignerInfo{
		Index: uint32(idx),
	}
}

// CalculateThreshold 计算门限值
func (p *DefaultVaultCommitteeProvider) CalculateThreshold(chain string, vaultID uint32) (int, error) {
	vaultCfg, err := p.GetVaultConfig(chain)
	if err != nil {
		// 使用默认配置
		return int(float64(p.cfg.DefaultCommitteeSize) * p.cfg.DefaultThresholdRatio), nil
	}

	committeeSize := int(vaultCfg.CommitteeSize)
	thresholdRatio := float64(vaultCfg.ThresholdRatio)
	if thresholdRatio == 0 {
		thresholdRatio = p.cfg.DefaultThresholdRatio
	}

	threshold := int(float64(committeeSize)*thresholdRatio + 0.5) // 向上取整
	return threshold, nil
}

// Ensure DefaultVaultCommitteeProvider implements VaultCommitteeProvider
var _ runtime.VaultCommitteeProvider = (*DefaultVaultCommitteeProvider)(nil)
