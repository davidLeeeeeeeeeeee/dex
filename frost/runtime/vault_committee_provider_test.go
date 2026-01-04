// frost/runtime/vault_committee_provider_test.go

package runtime

import (
	"dex/keys"
	"dex/pb"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// mockStateReader 模拟 ChainStateReader
type mockStateReader struct {
	data map[string][]byte
}

func newMockStateReader() *mockStateReader {
	return &mockStateReader{
		data: make(map[string][]byte),
	}
}

func (m *mockStateReader) Get(key string) ([]byte, bool, error) {
	data, exists := m.data[key]
	return data, exists, nil
}

func (m *mockStateReader) Scan(prefix string, fn func(k string, v []byte) bool) error {
	for k, v := range m.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			if !fn(k, v) {
				break
			}
		}
	}
	return nil
}

func (m *mockStateReader) Set(key string, value []byte) {
	m.data[key] = value
}

// TestVaultCommitteeProvider_GetTop10000 测试读取 Top10000
func TestVaultCommitteeProvider_GetTop10000(t *testing.T) {
	reader := newMockStateReader()

	// 设置 Top10000 数据
	top10000 := &pb.FrostTop10000{
		Height:  100,
		Indices: []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		Addresses: []string{"addr1", "addr2", "addr3", "addr4", "addr5",
			"addr6", "addr7", "addr8", "addr9", "addr10"},
	}
	data, err := proto.Marshal(top10000)
	require.NoError(t, err)
	reader.Set(keys.KeyFrostTop10000(), data)

	// 创建 provider
	provider := NewDefaultVaultCommitteeProvider(reader, DefaultVaultCommitteeProviderConfig())

	// 测试读取
	result, err := provider.GetTop10000()
	require.NoError(t, err)
	assert.Equal(t, uint64(100), result.Height)
	assert.Equal(t, 10, len(result.Indices))
	assert.Equal(t, "addr1", result.Addresses[0])
}

// TestVaultCommitteeProvider_GetVaultConfig 测试读取 VaultConfig
func TestVaultCommitteeProvider_GetVaultConfig(t *testing.T) {
	reader := newMockStateReader()

	// 设置 VaultConfig 数据
	cfg := &pb.FrostVaultConfig{
		Chain:          "btc",
		SignAlgo:       pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340,
		VaultCount:     50,
		CommitteeSize:  200,
		ThresholdRatio: 0.67,
	}
	data, err := proto.Marshal(cfg)
	require.NoError(t, err)
	reader.Set(keys.KeyFrostVaultConfig("btc", 0), data)

	// 创建 provider
	provider := NewDefaultVaultCommitteeProvider(reader, DefaultVaultCommitteeProviderConfig())

	// 测试读取
	result, err := provider.GetVaultConfig("btc")
	require.NoError(t, err)
	assert.Equal(t, "btc", result.Chain)
	assert.Equal(t, uint32(50), result.VaultCount)
	assert.Equal(t, uint32(200), result.CommitteeSize)
}

// TestVaultCommitteeProvider_VaultCommittee 测试委员会分配
func TestVaultCommitteeProvider_VaultCommittee(t *testing.T) {
	reader := newMockStateReader()

	// 创建包含 100 个矿工的 Top10000
	indices := make([]uint64, 100)
	addresses := make([]string, 100)
	for i := 0; i < 100; i++ {
		indices[i] = uint64(i)
		addresses[i] = "addr" + string(rune('A'+i%26))
	}

	top10000 := &pb.FrostTop10000{
		Height:    100,
		Indices:   indices,
		Addresses: addresses,
	}
	data, err := proto.Marshal(top10000)
	require.NoError(t, err)
	reader.Set(keys.KeyFrostTop10000(), data)

	// 设置 VaultConfig：2 个 vault，每个 50 人
	cfg := &pb.FrostVaultConfig{
		Chain:          "btc",
		VaultCount:     2,
		CommitteeSize:  50,
		ThresholdRatio: 0.67,
	}
	cfgData, err := proto.Marshal(cfg)
	require.NoError(t, err)
	reader.Set(keys.KeyFrostVaultConfig("btc", 0), cfgData)

	// 创建 provider
	provider := NewDefaultVaultCommitteeProvider(reader, DefaultVaultCommitteeProviderConfig())

	// 测试 vault 0 的委员会
	committee0, err := provider.VaultCommittee("btc", 0, 1)
	require.NoError(t, err)
	assert.Equal(t, 50, len(committee0))

	// 测试 vault 1 的委员会
	committee1, err := provider.VaultCommittee("btc", 1, 1)
	require.NoError(t, err)
	assert.Equal(t, 50, len(committee1))

	// 验证两个委员会不重叠
	vault0Members := make(map[uint32]bool)
	for _, s := range committee0 {
		vault0Members[s.Index] = true
	}
	for _, s := range committee1 {
		assert.False(t, vault0Members[s.Index], "committee member should not overlap")
	}
}

// TestVaultCommitteeProvider_CalculateThreshold 测试门限计算
func TestVaultCommitteeProvider_CalculateThreshold(t *testing.T) {
	reader := newMockStateReader()

	// 设置 VaultConfig
	cfg := &pb.FrostVaultConfig{
		Chain:          "btc",
		VaultCount:     10,
		CommitteeSize:  100,
		ThresholdRatio: 0.67,
	}
	cfgData, _ := proto.Marshal(cfg)
	reader.Set(keys.KeyFrostVaultConfig("btc", 0), cfgData)

	provider := NewDefaultVaultCommitteeProvider(reader, DefaultVaultCommitteeProviderConfig())

	threshold, err := provider.CalculateThreshold("btc", 0)
	require.NoError(t, err)
	assert.Equal(t, 67, threshold) // 100 * 0.67 = 67
}
