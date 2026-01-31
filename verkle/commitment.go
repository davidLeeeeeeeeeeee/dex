package verkle

import (
	"crypto/sha256"
	"errors"
	"sync"

	"github.com/crate-crypto/go-ipa/banderwagon"
	"github.com/crate-crypto/go-ipa/ipa"
)

// ============================================
// Pedersen 向量承诺
// 使用 go-ipa 库的 Bandersnatch 曲线
// ============================================

// ErrInvalidIndex 无效索引错误
var ErrInvalidIndex = errors.New("invalid child index: must be 0-255")

// IPAConfig 全局配置（预计算生成元）
var (
	globalIPAConfig     *ipa.IPAConfig
	globalIPAConfigOnce sync.Once
	globalIPAConfigErr  error
)

// GetIPAConfig 获取全局 IPA 配置（单例）
func GetIPAConfig() (*ipa.IPAConfig, error) {
	globalIPAConfigOnce.Do(func() {
		globalIPAConfig, globalIPAConfigErr = ipa.NewIPASettings()
	})
	return globalIPAConfig, globalIPAConfigErr
}

// ============================================
// Pedersen 承诺器
// ============================================

// PedersenCommitter Pedersen 向量承诺器
type PedersenCommitter struct {
	config *ipa.IPAConfig
}

// NewPedersenCommitter 创建新的承诺器
func NewPedersenCommitter() *PedersenCommitter {
	config, err := GetIPAConfig()
	if err != nil {
		// 如果配置初始化失败，使用空配置
		// 实际生产环境应该处理此错误
		return &PedersenCommitter{config: nil}
	}
	return &PedersenCommitter{
		config: config,
	}
}

// CommitToVector 计算 256 个标量的 Pedersen 向量承诺
// C = Σ v_i * G_i
// 输入：256 个 32 字节值（作为标量）
// 输出：承诺点（序列化为 32 字节）
func (p *PedersenCommitter) CommitToVector(values [256][]byte) ([]byte, error) {
	if p.config == nil {
		return nil, errors.New("IPA config not initialized")
	}

	// 将 []byte 值转换为 banderwagon.Fr 标量
	scalars := make([]banderwagon.Fr, 256)
	for i := 0; i < 256; i++ {
		if values[i] == nil || len(values[i]) == 0 {
			scalars[i].SetZero()
		} else {
			// 将字节转换为标量
			scalars[i].SetBytes(values[i])
		}
	}

	// 使用 SRS 生成元计算承诺
	// C = Σ scalars[i] * SRS[i]
	commitment := p.config.Commit(scalars[:])

	// 序列化承诺点为 32 字节切片
	bytes := commitment.Bytes()
	return bytes[:], nil
}

// ============================================
// 并行 Pedersen 承诺计算
// ============================================

// NumWorkers 并行 worker 数量
var NumWorkers = 8

// CommitToVectorParallel 并行计算 256 个标量的 Pedersen 向量承诺
// 使用 goroutine 分块并行计算标量乘法，然后合并结果
func (p *PedersenCommitter) CommitToVectorParallel(values [256][]byte) ([]byte, error) {
	if p.config == nil {
		return nil, errors.New("IPA config not initialized")
	}

	// 分块大小：256 / NumWorkers
	chunkSize := 256 / NumWorkers
	if chunkSize < 1 {
		chunkSize = 1
	}

	// 每个 worker 的部分承诺结果
	partialResults := make([]banderwagon.Element, NumWorkers)
	var wg sync.WaitGroup

	// 并行计算每个分块
	for w := 0; w < NumWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			start := workerID * chunkSize
			end := start + chunkSize
			if workerID == NumWorkers-1 {
				end = 256 // 最后一个 worker 处理剩余部分
			}

			// 计算此分块的部分承诺
			var partial banderwagon.Element
			partial.SetIdentity()

			for i := start; i < end; i++ {
				if values[i] == nil || len(values[i]) == 0 {
					continue // 跳过零值
				}

				var scalar banderwagon.Fr
				scalar.SetBytes(values[i])

				var point banderwagon.Element
				point.ScalarMul(&p.config.SRS[i], &scalar)
				partial.Add(&partial, &point)
			}

			partialResults[workerID] = partial
		}(w)
	}

	wg.Wait()

	// 合并所有部分结果
	var result banderwagon.Element
	result.SetIdentity()
	for i := 0; i < NumWorkers; i++ {
		result.Add(&result, &partialResults[i])
	}

	bytes := result.Bytes()
	return bytes[:], nil
}

// CommitBatchParallel 批量并行计算多个向量的承诺
// 适用于批量节点更新场景
func (p *PedersenCommitter) CommitBatchParallel(batch [][256][]byte) ([][]byte, error) {
	if p.config == nil {
		return nil, errors.New("IPA config not initialized")
	}

	results := make([][]byte, len(batch))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	// 每个向量并行计算
	for i := range batch {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			commitment, err := p.CommitToVectorParallel(batch[idx])
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			results[idx] = commitment
		}(i)
	}

	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

// CommitSingle 计算单个值的承诺（用于叶子节点值）
func (p *PedersenCommitter) CommitSingle(value []byte) ([]byte, error) {
	if p.config == nil {
		return nil, errors.New("IPA config not initialized")
	}

	var scalar banderwagon.Fr
	if value == nil || len(value) == 0 {
		scalar.SetZero()
	} else {
		scalar.SetBytes(value)
	}

	// 使用第一个生成元
	var point banderwagon.Element
	point.ScalarMul(&p.config.SRS[0], &scalar)

	bytes := point.Bytes()
	return bytes[:], nil
}

// UpdateCommitment 增量更新承诺
// newC = oldC + (newVal - oldVal) * G_i
func (p *PedersenCommitter) UpdateCommitment(oldCommitment []byte, index int, newValue, oldValue []byte) ([]byte, error) {
	if p.config == nil {
		return nil, errors.New("IPA config not initialized")
	}
	if index < 0 || index >= 256 {
		return nil, ErrInvalidIndex
	}

	// 解析旧承诺
	var oldPoint banderwagon.Element
	if err := oldPoint.SetBytes(oldCommitment); err != nil {
		return nil, err
	}

	// 计算差值标量
	var newScalar, oldScalar, diffScalar banderwagon.Fr
	if newValue != nil && len(newValue) > 0 {
		newScalar.SetBytes(newValue)
	}
	if oldValue != nil && len(oldValue) > 0 {
		oldScalar.SetBytes(oldValue)
	}
	diffScalar.Sub(&newScalar, &oldScalar)

	// 计算 diff * G_i
	var diffPoint banderwagon.Element
	diffPoint.ScalarMul(&p.config.SRS[index], &diffScalar)

	// newC = oldC + diffPoint
	var newPoint banderwagon.Element
	newPoint.Add(&oldPoint, &diffPoint)

	bytes := newPoint.Bytes()
	return bytes[:], nil
}

// ZeroCommitment 返回零承诺（全零向量的承诺）
func (p *PedersenCommitter) ZeroCommitment() []byte {
	var zero banderwagon.Element
	zero.SetIdentity()
	bytes := zero.Bytes()
	return bytes[:]
}

// ============================================
// 工具函数
// ============================================

// ToVerkleKey 将任意长度 Key 转换为 32 字节 Verkle Key
// 使用 SHA256 哈希确保均匀分布
func ToVerkleKey(key []byte) [32]byte {
	return sha256.Sum256(key)
}

// GetStemFromKey 获取 Key 的 Stem（前 31 字节）
func GetStemFromKey(key [32]byte) [31]byte {
	var stem [31]byte
	copy(stem[:], key[:31])
	return stem
}

// GetSuffixFromKey 获取 Key 的后缀（最后 1 字节，用于 256 叉索引）
func GetSuffixFromKey(key [32]byte) byte {
	return key[31]
}

// CommitmentToBytes 将承诺点转换为字节
func CommitmentToBytes(point *banderwagon.Element) []byte {
	bytes := point.Bytes()
	return bytes[:]
}

// BytesToCommitment 将字节转换为承诺点
func BytesToCommitment(data []byte) (*banderwagon.Element, error) {
	var point banderwagon.Element
	if err := point.SetBytes(data); err != nil {
		return nil, err
	}
	return &point, nil
}
