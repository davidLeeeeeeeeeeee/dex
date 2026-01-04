// frost/runtime/vault_committee.go
// Vault 委员会分配：确定性将 Top10000 分配到 M 个 Vault

package runtime

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
)

// ========== 委员会分配 ==========

// VaultCommitteeAssigner 负责将 Top10000 确定性分配到各 Vault
type VaultCommitteeAssigner struct{}

// NewVaultCommitteeAssigner 创建分配器
func NewVaultCommitteeAssigner() *VaultCommitteeAssigner {
	return &VaultCommitteeAssigner{}
}

// ComputeSeed 计算委员会分配种子
// seed = H(epoch_id || chain)
func ComputeSeed(epochID uint64, chain string) []byte {
	h := sha256.New()
	epochBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(epochBytes, epochID)
	h.Write(epochBytes)
	h.Write([]byte(chain))
	return h.Sum(nil)
}

// Permute 确定性洗牌（Fisher-Yates with seed-based RNG）
// 输入：列表（按 bit index 排序）+ 种子
// 输出：洗牌后的列表
func Permute(list []uint64, seed []byte) []uint64 {
	if len(list) == 0 {
		return list
	}

	// 复制列表避免修改原始数据
	result := make([]uint64, len(list))
	copy(result, list)

	// 使用种子初始化确定性 RNG
	rng := newSeededRNG(seed)

	// Fisher-Yates 洗牌
	for i := len(result) - 1; i > 0; i-- {
		j := rng.Intn(i + 1)
		result[i], result[j] = result[j], result[i]
	}

	return result
}

// AssignToVaults 将 Top10000 分配到 M 个 Vault
// 参数：
//   - top10000: 按 bit index 排序的矿工索引列表
//   - seed: 分配种子（H(epoch_id || chain)）
//   - vaultCount (M): Vault 数量
//   - committeeSize (K): 每个 Vault 的委员会规模
//
// 返回：各 Vault 的委员会成员（vault[j].committee = permuted_list[j*K : (j+1)*K]）
func AssignToVaults(top10000 []uint64, seed []byte, vaultCount, committeeSize int) [][]uint64 {
	// 确定性洗牌
	permuted := Permute(top10000, seed)

	// 按顺序切分
	vaults := make([][]uint64, vaultCount)
	for j := 0; j < vaultCount; j++ {
		start := j * committeeSize
		end := start + committeeSize
		if start >= len(permuted) {
			vaults[j] = []uint64{} // 没有更多成员
			continue
		}
		if end > len(permuted) {
			end = len(permuted)
		}
		vaults[j] = make([]uint64, end-start)
		copy(vaults[j], permuted[start:end])
	}

	return vaults
}

// GetVaultCommittee 获取指定 Vault 的委员会成员
func GetVaultCommittee(top10000 []uint64, epochID uint64, chain string, vaultID uint32, vaultCount, committeeSize int) []uint64 {
	seed := ComputeSeed(epochID, chain)
	vaults := AssignToVaults(top10000, seed, vaultCount, committeeSize)

	if int(vaultID) >= len(vaults) {
		return nil
	}
	return vaults[vaultID]
}

// SortByBitIndex 按 bit index 排序矿工列表
func SortByBitIndex(indices []uint64) []uint64 {
	sorted := make([]uint64, len(indices))
	copy(sorted, indices)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i] < sorted[j]
	})
	return sorted
}

// ========== 确定性 RNG ==========

// seededRNG 基于种子的确定性随机数生成器
type seededRNG struct {
	state []byte
	pos   int
}

func newSeededRNG(seed []byte) *seededRNG {
	return &seededRNG{
		state: seed,
		pos:   0,
	}
}

func (r *seededRNG) nextBytes(n int) []byte {
	result := make([]byte, n)
	for i := 0; i < n; i++ {
		if r.pos >= len(r.state) {
			// 重新哈希生成更多随机字节
			h := sha256.Sum256(r.state)
			r.state = h[:]
			r.pos = 0
		}
		result[i] = r.state[r.pos]
		r.pos++
	}
	return result
}

func (r *seededRNG) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	// 使用 4 字节生成随机数
	bytes := r.nextBytes(4)
	val := binary.BigEndian.Uint32(bytes)
	return int(val % uint32(n))
}

