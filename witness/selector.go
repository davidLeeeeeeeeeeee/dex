// witness/selector.go
// 见证者选择器 - 基于 VRF/Hash 的确定性见证者选择算法
package witness

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

// WitnessCandidate 见证者候选人信息
type WitnessCandidate struct {
	Address     string          // 地址
	StakeAmount decimal.Decimal // 质押金额
	IsActive    bool            // 是否活跃
}

// Selector 见证者选择器
type Selector struct {
	config *Config
}

// NewSelector 创建新的选择器
func NewSelector(config *Config) *Selector {
	if config == nil {
		config = DefaultConfig()
	}
	return &Selector{config: config}
}

// SelectWitnesses 选择见证者
// 参数:
//   - requestID: 入账请求ID（用于确定性选择）
//   - round: 当前轮次（0开始）
//   - candidates: 所有活跃的见证者候选人
//   - excludeAddresses: 需要排除的地址（如之前轮次已选中的）
//
// 返回: 选中的见证者地址列表
func (s *Selector) SelectWitnesses(
	requestID string,
	round uint32,
	candidates []*WitnessCandidate,
	excludeAddresses map[string]bool,
) ([]string, error) {
	// 过滤活跃且未被排除的候选人
	activeCandidates := make([]*WitnessCandidate, 0)
	for _, c := range candidates {
		if c.IsActive && !excludeAddresses[c.Address] {
			activeCandidates = append(activeCandidates, c)
		}
	}

	if len(activeCandidates) == 0 {
		return nil, fmt.Errorf("no active candidates available")
	}

	// 计算本轮需要选择的见证者数量
	count := s.calculateWitnessCount(round)
	if uint32(len(activeCandidates)) < count {
		count = uint32(len(activeCandidates))
	}

	// 使用加权随机选择
	selected := s.weightedRandomSelect(requestID, round, activeCandidates, count)

	return selected, nil
}

// calculateWitnessCount 计算本轮需要的见证者数量
func (s *Selector) calculateWitnessCount(round uint32) uint32 {
	count := s.config.InitialWitnessCount
	for i := uint32(0); i < round; i++ {
		count *= s.config.ExpandMultiplier
	}
	return count
}

// weightedRandomSelect 加权随机选择
// 使用确定性算法，相同输入产生相同输出
func (s *Selector) weightedRandomSelect(
	requestID string,
	round uint32,
	candidates []*WitnessCandidate,
	count uint32,
) []string {
	// 计算每个候选人的选择概率（基于质押权重）
	type weightedCandidate struct {
		address string
		weight  decimal.Decimal
		score   uint64 // 用于排序的分数
	}

	totalStake := decimal.Zero
	for _, c := range candidates {
		totalStake = totalStake.Add(c.StakeAmount)
	}

	// 如果总质押为0，使用均等权重
	if totalStake.IsZero() {
		totalStake = decimal.NewFromInt(int64(len(candidates)))
		for i := range candidates {
			candidates[i].StakeAmount = decimal.NewFromInt(1)
		}
	}

	// 为每个候选人计算确定性分数
	weighted := make([]weightedCandidate, len(candidates))
	for i, c := range candidates {
		// 计算确定性哈希分数
		score := s.calculateScore(requestID, round, c.Address)
		// 根据质押权重调整分数
		weight := c.StakeAmount.Div(totalStake)
		weighted[i] = weightedCandidate{
			address: c.Address,
			weight:  weight,
			score:   score,
		}
	}

	// 按分数排序（分数越高越容易被选中）
	sort.Slice(weighted, func(i, j int) bool {
		// 使用权重调整后的分数进行比较
		// 权重越高，分数越容易排在前面
		scoreI := float64(weighted[i].score) * weighted[i].weight.InexactFloat64()
		scoreJ := float64(weighted[j].score) * weighted[j].weight.InexactFloat64()
		return scoreI > scoreJ
	})

	// 选择前 count 个
	selected := make([]string, 0, count)
	for i := uint32(0); i < count && i < uint32(len(weighted)); i++ {
		selected = append(selected, weighted[i].address)
	}

	return selected
}

// calculateScore 计算确定性分数
// 使用 SHA256(requestID || round || address) 的前8字节作为分数
func (s *Selector) calculateScore(requestID string, round uint32, address string) uint64 {
	hasher := sha256.New()
	hasher.Write([]byte(requestID))

	roundBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(roundBytes, round)
	hasher.Write(roundBytes)

	hasher.Write([]byte(address))

	hash := hasher.Sum(nil)
	return binary.BigEndian.Uint64(hash[:8])
}

// SelectArbitrators 选择仲裁者
// 仲裁者选择与见证者选择类似，但需要排除原见证者
func (s *Selector) SelectArbitrators(
	challengeID string,
	round uint32,
	candidates []*WitnessCandidate,
	originalWitnesses []string,
) ([]string, error) {
	// 构建排除列表
	excludeAddresses := make(map[string]bool)
	for _, addr := range originalWitnesses {
		excludeAddresses[addr] = true
	}

	// 仲裁者数量随轮次增加
	count := s.calculateArbitratorCount(round)

	// 过滤活跃且未被排除的候选人
	activeCandidates := make([]*WitnessCandidate, 0)
	for _, c := range candidates {
		if c.IsActive && !excludeAddresses[c.Address] {
			activeCandidates = append(activeCandidates, c)
		}
	}

	if len(activeCandidates) == 0 {
		return nil, fmt.Errorf("no arbitrators available")
	}

	if uint32(len(activeCandidates)) < count {
		count = uint32(len(activeCandidates))
	}

	// 使用加权随机选择
	selected := s.weightedRandomSelect(challengeID, round, activeCandidates, count)

	return selected, nil
}

// calculateArbitratorCount 计算仲裁者数量
func (s *Selector) calculateArbitratorCount(round uint32) uint32 {
	// 仲裁者数量 = 初始见证者数量 * 2^(round+1)
	count := s.config.InitialWitnessCount * 2
	for i := uint32(0); i < round; i++ {
		count *= s.config.ExpandMultiplier
	}
	return count
}

// VerifyWitnessSelection 验证见证者选择是否正确
// 用于其他节点验证选择结果的确定性
func (s *Selector) VerifyWitnessSelection(
	requestID string,
	round uint32,
	candidates []*WitnessCandidate,
	selectedWitnesses []string,
) bool {
	// 重新执行选择算法
	expected, err := s.SelectWitnesses(requestID, round, candidates, nil)
	if err != nil {
		return false
	}

	// 比较结果
	if len(expected) != len(selectedWitnesses) {
		return false
	}

	expectedSet := make(map[string]bool)
	for _, addr := range expected {
		expectedSet[addr] = true
	}

	for _, addr := range selectedWitnesses {
		if !expectedSet[addr] {
			return false
		}
	}

	return true
}

// GetWitnessCountForRound 获取指定轮次的见证者数量
func (s *Selector) GetWitnessCountForRound(round uint32) uint32 {
	return s.calculateWitnessCount(round)
}
