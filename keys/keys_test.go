// keys/keys_test.go
package keys

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestFrostKeys 测试 Frost 相关 key 函数
func TestFrostKeys(t *testing.T) {
	// 测试 KeyFrostConfig
	t.Run("KeyFrostConfig", func(t *testing.T) {
		key := KeyFrostConfig()
		assert.Equal(t, "v1_frost_cfg", key)
	})

	// 测试 KeyFrostTop10000
	t.Run("KeyFrostTop10000", func(t *testing.T) {
		key := KeyFrostTop10000()
		assert.Equal(t, "v1_frost_top10000", key)
	})

	// 测试 KeyFrostVaultConfig
	t.Run("KeyFrostVaultConfig", func(t *testing.T) {
		key := KeyFrostVaultConfig("BTC", 5)
		assert.Equal(t, "v1_frost_vault_cfg_BTC_5", key)

		key = KeyFrostVaultConfig("ETH", 100)
		assert.Equal(t, "v1_frost_vault_cfg_ETH_100", key)
	})

	// 测试 KeyFrostVaultState
	t.Run("KeyFrostVaultState", func(t *testing.T) {
		key := KeyFrostVaultState("BTC", 3)
		assert.Equal(t, "v1_frost_vault_state_BTC_3", key)
	})

	// 测试 KeyFrostVaultTransition
	t.Run("KeyFrostVaultTransition", func(t *testing.T) {
		key := KeyFrostVaultTransition("BTC", 1, 12345)
		// epoch_id 用 padUint 格式化为 20 位
		assert.Equal(t, "v1_frost_vault_transition_BTC_1_00000000000000012345", key)
	})

	// 测试 KeyFrostVaultDkgCommit
	t.Run("KeyFrostVaultDkgCommit", func(t *testing.T) {
		key := KeyFrostVaultDkgCommit("ETH", 2, 999, "participant_addr")
		assert.Equal(t, "v1_frost_vault_dkg_commit_ETH_2_00000000000000000999_participant_addr", key)
	})

	// 测试 KeyFrostSignedPackage
	t.Run("KeyFrostSignedPackage", func(t *testing.T) {
		key := KeyFrostSignedPackage("job-abc-123", 0)
		assert.Equal(t, "v1_frost_signed_pkg_job-abc-123_00000000000000000000", key)

		key = KeyFrostSignedPackage("job-xyz", 42)
		assert.Equal(t, "v1_frost_signed_pkg_job-xyz_00000000000000000042", key)
	})

	// 测试已有的 Frost key 函数
	t.Run("KeyFrostWithdraw", func(t *testing.T) {
		key := KeyFrostWithdraw("withdraw-id-123")
		assert.Equal(t, "v1_frost_withdraw_withdraw-id-123", key)
	})

	t.Run("KeyFrostWithdrawFIFOIndex", func(t *testing.T) {
		key := KeyFrostWithdrawFIFOIndex("BTC", "native", 100)
		assert.Equal(t, "v1_frost_withdraw_q_BTC_native_00000000000000000100", key)
	})

	t.Run("KeyFrostWithdrawFIFOSeq", func(t *testing.T) {
		key := KeyFrostWithdrawFIFOSeq("ETH", "USDT")
		assert.Equal(t, "v1_frost_withdraw_seq_ETH_USDT", key)
	})

	t.Run("KeyFrostWithdrawFIFOHead", func(t *testing.T) {
		key := KeyFrostWithdrawFIFOHead("SOL", "native")
		assert.Equal(t, "v1_frost_withdraw_head_SOL_native", key)
	})

	t.Run("KeyFrostWithdrawTxRef", func(t *testing.T) {
		key := KeyFrostWithdrawTxRef("tx-id-abc-123")
		assert.Equal(t, "v1_frost_withdraw_ref_tx-id-abc-123", key)
	})

	t.Run("KeyFrostSignedPackageCount", func(t *testing.T) {
		key := KeyFrostSignedPackageCount("job-id-xyz")
		assert.Equal(t, "v1_frost_signed_pkg_count_job-id-xyz", key)
	})
}
