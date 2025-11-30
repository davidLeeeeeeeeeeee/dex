// witness/vote_manager.go
// 投票管理器 - 管理投票收集和共识判断
// 注意：见证者通过提交交易进行投票，交易本身已有签名验证，无需额外的 BLS 聚合
package witness

import (
	"dex/pb"
	"sync"
)

// VoteManager 投票管理器
type VoteManager struct {
	mu     sync.RWMutex
	config *Config

	// 请求ID -> 投票集合
	votes map[string]map[string]*pb.WitnessVote // requestID -> witnessAddr -> vote

	// 请求ID -> 选中的见证者
	selectedWitnesses map[string]map[string]bool // requestID -> witnessAddr -> true

	// 事件通道
	eventChan chan *Event
}

// NewVoteManager 创建投票管理器
func NewVoteManager(config *Config) *VoteManager {
	if config == nil {
		config = DefaultConfig()
	}
	return &VoteManager{
		config:            config,
		votes:             make(map[string]map[string]*pb.WitnessVote),
		selectedWitnesses: make(map[string]map[string]bool),
		eventChan:         make(chan *Event, 100),
	}
}

// Reset 重置内存状态
func (vm *VoteManager) Reset() {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	vm.votes = make(map[string]map[string]*pb.WitnessVote)
	vm.selectedWitnesses = make(map[string]map[string]bool)
}

// SetSelectedWitnesses 设置请求的选中见证者
func (vm *VoteManager) SetSelectedWitnesses(requestID string, witnesses []string) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	witnessSet := make(map[string]bool)
	for _, w := range witnesses {
		witnessSet[w] = true
	}
	vm.selectedWitnesses[requestID] = witnessSet

	// 初始化投票集合
	if vm.votes[requestID] == nil {
		vm.votes[requestID] = make(map[string]*pb.WitnessVote)
	}
}

// AddVote 添加投票
func (vm *VoteManager) AddVote(vote *pb.WitnessVote) error {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	requestID := vote.RequestId
	witnessAddr := vote.WitnessAddress

	// 检查是否是选中的见证者
	if !vm.selectedWitnesses[requestID][witnessAddr] {
		return ErrNotSelectedWitness
	}

	// 检查是否重复投票
	if vm.votes[requestID] == nil {
		vm.votes[requestID] = make(map[string]*pb.WitnessVote)
	}
	if _, exists := vm.votes[requestID][witnessAddr]; exists {
		return ErrDuplicateVote
	}

	// 添加投票
	vm.votes[requestID][witnessAddr] = vote

	return nil
}

// GetVoteStats 获取投票统计
func (vm *VoteManager) GetVoteStats(requestID string) *VoteStats {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	votes := vm.votes[requestID]
	witnesses := vm.selectedWitnesses[requestID]

	if len(witnesses) == 0 {
		return &VoteStats{}
	}

	var pass, fail, abstain uint32
	for _, vote := range votes {
		switch vote.VoteType {
		case pb.WitnessVoteType_VOTE_PASS:
			pass++
		case pb.WitnessVoteType_VOTE_FAIL:
			fail++
		case pb.WitnessVoteType_VOTE_ABSTAIN:
			abstain++
		}
	}

	total := uint32(len(witnesses))
	return CalculateVoteStats(pass, fail, abstain, total)
}

// CheckConsensus 检查共识状态
func (vm *VoteManager) CheckConsensus(requestID string, isDeadline bool) ConsensusResult {
	stats := vm.GetVoteStats(requestID)
	return DetermineConsensus(stats, vm.config.ConsensusThreshold, isDeadline)
}

// GetVotes 获取所有投票
func (vm *VoteManager) GetVotes(requestID string) []*pb.WitnessVote {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	votes := vm.votes[requestID]
	result := make([]*pb.WitnessVote, 0, len(votes))
	for _, v := range votes {
		result = append(result, v)
	}
	return result
}

// GetPassVotes 获取通过的投票
func (vm *VoteManager) GetPassVotes(requestID string) []*pb.WitnessVote {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	votes := vm.votes[requestID]
	result := make([]*pb.WitnessVote, 0)
	for _, v := range votes {
		if v.VoteType == pb.WitnessVoteType_VOTE_PASS {
			result = append(result, v)
		}
	}
	return result
}

// ClearRequest 清理请求相关数据
func (vm *VoteManager) ClearRequest(requestID string) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	delete(vm.votes, requestID)
	delete(vm.selectedWitnesses, requestID)
}

// ExpandWitnesses 扩大见证者范围
func (vm *VoteManager) ExpandWitnesses(requestID string, newWitnesses []string) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	if vm.selectedWitnesses[requestID] == nil {
		vm.selectedWitnesses[requestID] = make(map[string]bool)
	}

	for _, w := range newWitnesses {
		vm.selectedWitnesses[requestID][w] = true
	}

	// 清空之前的投票（新一轮重新投票）
	vm.votes[requestID] = make(map[string]*pb.WitnessVote)
}

// GetSelectedWitnesses 获取选中的见证者列表
func (vm *VoteManager) GetSelectedWitnesses(requestID string) []string {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	witnesses := vm.selectedWitnesses[requestID]
	result := make([]string, 0, len(witnesses))
	for addr := range witnesses {
		result = append(result, addr)
	}
	return result
}

// HasVoted 检查见证者是否已投票
func (vm *VoteManager) HasVoted(requestID, witnessAddr string) bool {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	if vm.votes[requestID] == nil {
		return false
	}
	_, exists := vm.votes[requestID][witnessAddr]
	return exists
}

// GetVoteCount 获取已投票数量
func (vm *VoteManager) GetVoteCount(requestID string) int {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	return len(vm.votes[requestID])
}

// IsAllVoted 检查是否所有见证者都已投票
func (vm *VoteManager) IsAllVoted(requestID string) bool {
	vm.mu.RLock()
	defer vm.mu.RUnlock()

	witnesses := vm.selectedWitnesses[requestID]
	votes := vm.votes[requestID]

	return len(votes) >= len(witnesses)
}

// CanReachConsensus 检查是否还有可能达成共识
// 如果弃票已经超过阈值，则无法达成共识
func (vm *VoteManager) CanReachConsensus(requestID string) bool {
	stats := vm.GetVoteStats(requestID)

	// 如果弃票率已经超过 (100 - threshold)%，则无法达成共识
	maxAbstainRatio := float64(100 - vm.config.ConsensusThreshold)
	return stats.AbstainRatio <= maxAbstainRatio
}

// Events 返回事件通道
func (vm *VoteManager) Events() <-chan *Event {
	return vm.eventChan
}

// emitEvent 发送事件
func (vm *VoteManager) emitEvent(event *Event) {
	select {
	case vm.eventChan <- event:
	default:
		// 通道满了，丢弃事件
	}
}

