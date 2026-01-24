// witness/stake_manager.go
// 质押管理器 - 管理见证者质押、解质押和罚没逻辑
package witness

import (
	"dex/pb"
	"fmt"
	"sync"

	"github.com/shopspring/decimal"
)

// StakeManager 质押管理器
type StakeManager struct {
	mu     sync.RWMutex
	config *Config

	// 见证者信息缓存（内存中的快照，实际数据在DB中）
	witnesses map[string]*pb.WitnessInfo

	// 事件通道
	eventChan chan *Event
}

// NewStakeManager 创建质押管理器
func NewStakeManager(config *Config) *StakeManager {
	if config == nil {
		config = DefaultConfig()
	}
	return &StakeManager{
		config:    config,
		witnesses: make(map[string]*pb.WitnessInfo),
		eventChan: make(chan *Event, 100),
	}
}

// LoadWitness 加载见证者信息到缓存
func (sm *StakeManager) LoadWitness(info *pb.WitnessInfo) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.witnesses[info.Address] = info
}

// Reset 重置内存状态
func (sm *StakeManager) Reset() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.witnesses = make(map[string]*pb.WitnessInfo)
}

// GetWitness 获取见证者信息
func (sm *StakeManager) GetWitness(address string) (*pb.WitnessInfo, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	info, exists := sm.witnesses[address]
	if !exists {
		return nil, ErrWitnessNotFound
	}
	return info, nil
}

// ValidateStake 验证质押操作
func (sm *StakeManager) ValidateStake(address string, amount decimal.Decimal) error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	// 检查最小质押金额
	minStake, err := decimal.NewFromString(sm.config.MinStakeAmount)
	if err != nil {
		return fmt.Errorf("invalid min stake config: %w", err)
	}

	if amount.LessThan(minStake) {
		return ErrInsufficientStake
	}

	// 检查是否已经是活跃见证者
	if info, exists := sm.witnesses[address]; exists {
		if info.Status == pb.WitnessStatus_WITNESS_ACTIVE {
			return ErrWitnessAlreadyActive
		}
	}

	return nil
}

// ProcessStake 处理质押
// 返回更新后的见证者信息
func (sm *StakeManager) ProcessStake(address string, amount decimal.Decimal, height uint64) (*pb.WitnessInfo, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// 获取或创建见证者信息
	info, exists := sm.witnesses[address]
	if !exists {
		info = &pb.WitnessInfo{
			Address:     address,
			StakeAmount: "0",
			Status:      pb.WitnessStatus_WITNESS_CANDIDATE,
		}
	}

	// 更新质押金额
	currentStake, _ := decimal.NewFromString(info.StakeAmount)
	newStake := currentStake.Add(amount)
	info.StakeAmount = newStake.String()

	// 更新状态为活跃
	info.Status = pb.WitnessStatus_WITNESS_ACTIVE

	// 保存到缓存
	sm.witnesses[address] = info

	// 发送事件
	sm.emitEvent(&Event{
		Type:   EventWitnessStaked,
		Data:   info,
		Height: height,
	})

	return info, nil
}

// ValidateUnstake 验证解质押操作
func (sm *StakeManager) ValidateUnstake(address string) error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	info, exists := sm.witnesses[address]
	if !exists {
		return ErrWitnessNotFound
	}

	// 检查状态
	if info.Status != pb.WitnessStatus_WITNESS_ACTIVE {
		return ErrWitnessNotActive
	}

	// 检查是否有待处理任务
	if len(info.PendingTasks) > 0 {
		return ErrPendingTasks
	}

	// 检查是否有搁置的仲裁
	if len(info.ShelvedArbitrations) > 0 {
		return ErrShelvedArbitrations
	}

	return nil
}

// ProcessUnstake 处理解质押申请
func (sm *StakeManager) ProcessUnstake(address string, height uint64) (*pb.WitnessInfo, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	info, exists := sm.witnesses[address]
	if !exists {
		return nil, ErrWitnessNotFound
	}

	// 更新状态为解质押中
	info.Status = pb.WitnessStatus_WITNESS_UNSTAKING
	info.UnstakeHeight = height

	// 保存到缓存
	sm.witnesses[address] = info

	// 发送事件
	sm.emitEvent(&Event{
		Type:   EventWitnessUnstaking,
		Data:   info,
		Height: height,
	})

	return info, nil
}

// ValidateExit 验证退出操作
func (sm *StakeManager) ValidateExit(address string, currentHeight uint64) error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	info, exists := sm.witnesses[address]
	if !exists {
		return ErrWitnessNotFound
	}

	// 检查状态
	if info.Status != pb.WitnessStatus_WITNESS_UNSTAKING {
		return ErrUnstakingInProgress
	}

	// 检查锁定期是否已过
	lockEndHeight := info.UnstakeHeight + sm.config.UnstakeLockBlocks
	if currentHeight < lockEndHeight {
		return ErrLockPeriodNotExpired
	}

	// 检查是否有待处理任务
	if len(info.PendingTasks) > 0 {
		return ErrPendingTasks
	}

	// 检查是否有搁置的仲裁
	if len(info.ShelvedArbitrations) > 0 {
		return ErrShelvedArbitrations
	}

	return nil
}

// ProcessExit 处理退出
func (sm *StakeManager) ProcessExit(address string, height uint64) (*pb.WitnessInfo, decimal.Decimal, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	info, exists := sm.witnesses[address]
	if !exists {
		return nil, decimal.Zero, ErrWitnessNotFound
	}

	// 记录退还金额
	refundAmount, _ := decimal.NewFromString(info.StakeAmount)

	// 更新状态
	info.Status = pb.WitnessStatus_WITNESS_EXITED
	info.StakeAmount = "0"

	// 保存到缓存
	sm.witnesses[address] = info

	// 发送事件
	sm.emitEvent(&Event{
		Type:   EventWitnessExited,
		Data:   info,
		Height: height,
	})

	return info, refundAmount, nil
}

// ProcessSlash 处理罚没
func (sm *StakeManager) ProcessSlash(address string, reason string, height uint64) (*pb.WitnessInfo, decimal.Decimal, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	info, exists := sm.witnesses[address]
	if !exists {
		return nil, decimal.Zero, ErrWitnessNotFound
	}

	// 计算罚没金额
	stakeAmount, _ := decimal.NewFromString(info.StakeAmount)
	slashAmount := stakeAmount.Mul(decimal.NewFromFloat(sm.config.SlashRatio))

	// 更新状态
	info.Status = pb.WitnessStatus_WITNESS_SLASHED
	info.StakeAmount = stakeAmount.Sub(slashAmount).String()
	info.SlashedCount++

	// 保存到缓存
	sm.witnesses[address] = info

	// 发送事件
	sm.emitEvent(&Event{
		Type:   EventWitnessSlashed,
		Data:   map[string]interface{}{"info": info, "reason": reason, "amount": slashAmount},
		Height: height,
	})

	return info, slashAmount, nil
}

// AddPendingTask 添加待处理任务
func (sm *StakeManager) AddPendingTask(address, taskID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	info, exists := sm.witnesses[address]
	if !exists {
		return ErrWitnessNotFound
	}

	info.PendingTasks = append(info.PendingTasks, taskID)
	sm.witnesses[address] = info

	return nil
}

// RemovePendingTask 移除待处理任务
func (sm *StakeManager) RemovePendingTask(address, taskID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	info, exists := sm.witnesses[address]
	if !exists {
		return ErrWitnessNotFound
	}

	// 查找并移除任务
	for i, t := range info.PendingTasks {
		if t == taskID {
			info.PendingTasks = append(info.PendingTasks[:i], info.PendingTasks[i+1:]...)
			break
		}
	}

	sm.witnesses[address] = info
	return nil
}

// GetActiveWitnesses 获取所有活跃见证者
func (sm *StakeManager) GetActiveWitnesses() []*pb.WitnessInfo {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]*pb.WitnessInfo, 0)
	for _, info := range sm.witnesses {
		if info.Status == pb.WitnessStatus_WITNESS_ACTIVE {
			result = append(result, info)
		}
	}
	return result
}

// GetWitnessCandidates 获取见证者候选人列表（用于选择器）
func (sm *StakeManager) GetWitnessCandidates() []*WitnessCandidate {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]*WitnessCandidate, 0)
	for _, info := range sm.witnesses {
		stake, _ := decimal.NewFromString(info.StakeAmount)
		result = append(result, &WitnessCandidate{
			Address:     info.Address,
			StakeAmount: stake,
			IsActive:    info.Status == pb.WitnessStatus_WITNESS_ACTIVE,
		})
	}
	return result
}

// GetAllWitnesses 获取所有见证者列表（包括所有状态）
func (sm *StakeManager) GetAllWitnesses() []*pb.WitnessInfo {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]*pb.WitnessInfo, 0, len(sm.witnesses))
	for _, info := range sm.witnesses {
		result = append(result, info)
	}
	return result
}

// UpdateWitnessStats 更新见证者统计
func (sm *StakeManager) UpdateWitnessStats(address string, voteType pb.WitnessVoteType) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	info, exists := sm.witnesses[address]
	if !exists {
		return ErrWitnessNotFound
	}

	info.TotalWitnessCount++
	switch voteType {
	case pb.WitnessVoteType_VOTE_PASS:
		info.PassCount++
	case pb.WitnessVoteType_VOTE_FAIL:
		info.FailCount++
	case pb.WitnessVoteType_VOTE_ABSTAIN:
		info.AbstainCount++
	}

	sm.witnesses[address] = info
	return nil
}

// DistributeReward 分配奖励
func (sm *StakeManager) DistributeReward(witnesses []string, amount decimal.Decimal) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if len(witnesses) == 0 {
		return nil
	}

	// 计算每人分得的奖励
	perWitnessAmount := amount.Div(decimal.NewFromInt(int64(len(witnesses))))

	for _, addr := range witnesses {
		info, exists := sm.witnesses[addr]
		if !exists {
			continue
		}

		// 更新待领取奖励
		currentPending, _ := decimal.NewFromString(info.PendingReward)
		info.PendingReward = currentPending.Add(perWitnessAmount).String()

		sm.witnesses[addr] = info
	}

	return nil
}

// Events 返回事件通道
func (sm *StakeManager) Events() <-chan *Event {
	return sm.eventChan
}

// emitEvent 发送事件
func (sm *StakeManager) emitEvent(event *Event) {
	select {
	case sm.eventChan <- event:
	default:
	}
}
