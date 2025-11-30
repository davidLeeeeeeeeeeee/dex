// witness/challenge_manager.go
// 挑战管理器 - 管理挑战发起、仲裁和结果处理
package witness

import (
	"dex/pb"
	"fmt"
	"sync"

	"github.com/shopspring/decimal"
)

// ChallengeManager 挑战管理器
type ChallengeManager struct {
	mu     sync.RWMutex
	config *Config

	// 挑战记录缓存
	challenges map[string]*pb.ChallengeRecord // challengeID -> record

	// 请求ID到挑战ID的映射
	requestChallenges map[string]string // requestID -> challengeID

	// 仲裁投票管理
	arbitrationVotes map[string]map[string]*pb.WitnessVote // challengeID -> arbitratorAddr -> vote

	// 事件通道
	eventChan chan *Event
}

// NewChallengeManager 创建挑战管理器
func NewChallengeManager(config *Config) *ChallengeManager {
	if config == nil {
		config = DefaultConfig()
	}
	return &ChallengeManager{
		config:            config,
		challenges:        make(map[string]*pb.ChallengeRecord),
		requestChallenges: make(map[string]string),
		arbitrationVotes:  make(map[string]map[string]*pb.WitnessVote),
		eventChan:         make(chan *Event, 100),
	}
}

// Reset 重置内存状态
func (cm *ChallengeManager) Reset() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.challenges = make(map[string]*pb.ChallengeRecord)
	cm.requestChallenges = make(map[string]string)
	cm.arbitrationVotes = make(map[string]map[string]*pb.WitnessVote)
}

// LoadChallenge 加载挑战记录到内存
func (cm *ChallengeManager) LoadChallenge(record *pb.ChallengeRecord) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.challenges[record.ChallengeId] = record
	cm.requestChallenges[record.RequestId] = record.ChallengeId
	// 恢复仲裁投票
	if cm.arbitrationVotes[record.ChallengeId] == nil {
		cm.arbitrationVotes[record.ChallengeId] = make(map[string]*pb.WitnessVote)
	}
	for _, vote := range record.ArbitrationVotes {
		cm.arbitrationVotes[record.ChallengeId][vote.WitnessAddress] = vote
	}
}

// ValidateChallenge 验证挑战
func (cm *ChallengeManager) ValidateChallenge(
	requestID string,
	challengerAddr string,
	stakeAmount decimal.Decimal,
) error {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	// 检查是否已存在挑战
	if _, exists := cm.requestChallenges[requestID]; exists {
		return ErrChallengeExists
	}

	// 检查挑战质押金额
	minStake, err := decimal.NewFromString(cm.config.ChallengeStakeAmount)
	if err != nil {
		return fmt.Errorf("invalid challenge stake config: %w", err)
	}

	if stakeAmount.LessThan(minStake) {
		return ErrInsufficientChallenge
	}

	return nil
}

// CreateChallenge 创建挑战
func (cm *ChallengeManager) CreateChallenge(
	challengeID string,
	requestID string,
	challengerAddr string,
	stakeAmount string,
	reason string,
	height uint64,
	arbitrators []string,
) (*pb.ChallengeRecord, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// 创建挑战记录
	record := &pb.ChallengeRecord{
		ChallengeId:        challengeID,
		RequestId:          requestID,
		ChallengerAddress:  challengerAddr,
		StakeAmount:        stakeAmount,
		Reason:             reason,
		CreateHeight:       height,
		DeadlineHeight:     height + cm.config.ArbitrationPeriodBlocks,
		ArbitrationRound:   0,
		Arbitrators:        arbitrators,
		ConsensusThreshold: GetConsensusThreshold(0),
		Finalized:          false,
	}

	// 保存到缓存
	cm.challenges[challengeID] = record
	cm.requestChallenges[requestID] = challengeID
	cm.arbitrationVotes[challengeID] = make(map[string]*pb.WitnessVote)

	// 发送事件
	cm.emitEvent(&Event{
		Type:      EventChallengeInitiated,
		RequestID: requestID,
		Data:      record,
		Height:    height,
	})

	return record, nil
}

// GetChallenge 获取挑战记录
func (cm *ChallengeManager) GetChallenge(challengeID string) (*pb.ChallengeRecord, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	record, exists := cm.challenges[challengeID]
	if !exists {
		return nil, ErrChallengeNotFound
	}
	return record, nil
}

// GetChallengeByRequest 根据请求ID获取挑战
func (cm *ChallengeManager) GetChallengeByRequest(requestID string) (*pb.ChallengeRecord, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	challengeID, exists := cm.requestChallenges[requestID]
	if !exists {
		return nil, ErrChallengeNotFound
	}

	record, exists := cm.challenges[challengeID]
	if !exists {
		return nil, ErrChallengeNotFound
	}
	return record, nil
}

// AddArbitrationVote 添加仲裁投票
func (cm *ChallengeManager) AddArbitrationVote(challengeID string, vote *pb.WitnessVote) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	record, exists := cm.challenges[challengeID]
	if !exists {
		return ErrChallengeNotFound
	}

	// 检查是否是仲裁者
	isArbitrator := false
	for _, addr := range record.Arbitrators {
		if addr == vote.WitnessAddress {
			isArbitrator = true
			break
		}
	}
	if !isArbitrator {
		return ErrNotArbitrator
	}

	// 检查是否重复投票
	if cm.arbitrationVotes[challengeID] == nil {
		cm.arbitrationVotes[challengeID] = make(map[string]*pb.WitnessVote)
	}
	if _, exists := cm.arbitrationVotes[challengeID][vote.WitnessAddress]; exists {
		return ErrDuplicateVote
	}

	// 添加投票
	cm.arbitrationVotes[challengeID][vote.WitnessAddress] = vote

	// 更新统计
	switch vote.VoteType {
	case pb.WitnessVoteType_VOTE_PASS:
		record.PassCount++
	case pb.WitnessVoteType_VOTE_FAIL:
		record.FailCount++
	}

	// 保存投票到记录
	record.ArbitrationVotes = append(record.ArbitrationVotes, vote)
	cm.challenges[challengeID] = record

	return nil
}

// CheckArbitrationConsensus 检查仲裁共识
func (cm *ChallengeManager) CheckArbitrationConsensus(challengeID string, isDeadline bool) ConsensusResult {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	record, exists := cm.challenges[challengeID]
	if !exists {
		return ConsensusNone
	}

	total := uint32(len(record.Arbitrators))
	if total == 0 {
		return ConsensusNone
	}

	// 计算投票统计
	// 注意：在仲裁中，PASS 表示支持原判定（挑战失败），FAIL 表示反对原判定（挑战成功）
	votes := cm.arbitrationVotes[challengeID]
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

	stats := CalculateVoteStats(pass, fail, abstain, total)
	return DetermineConsensus(stats, record.ConsensusThreshold, isDeadline)
}

// ExpandArbitrationScope 扩大仲裁范围
func (cm *ChallengeManager) ExpandArbitrationScope(
	challengeID string,
	newArbitrators []string,
	height uint64,
) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	record, exists := cm.challenges[challengeID]
	if !exists {
		return ErrChallengeNotFound
	}

	// 增加轮次
	record.ArbitrationRound++

	// 更新仲裁者列表
	record.Arbitrators = newArbitrators

	// 更新共识阈值
	record.ConsensusThreshold = GetConsensusThreshold(record.ArbitrationRound)

	// 更新截止高度
	record.DeadlineHeight = height + cm.config.ArbitrationPeriodBlocks

	// 清空投票（新一轮重新投票）
	record.ArbitrationVotes = nil
	record.PassCount = 0
	record.FailCount = 0
	cm.arbitrationVotes[challengeID] = make(map[string]*pb.WitnessVote)

	cm.challenges[challengeID] = record

	return nil
}

// FinalizeChallenge 完成挑战
func (cm *ChallengeManager) FinalizeChallenge(
	challengeID string,
	success bool,
	height uint64,
) (*pb.ChallengeRecord, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	record, exists := cm.challenges[challengeID]
	if !exists {
		return nil, ErrChallengeNotFound
	}

	record.ChallengeSuccess = success
	record.Finalized = true
	cm.challenges[challengeID] = record

	// 发送事件
	cm.emitEvent(&Event{
		Type:      EventChallengeResolved,
		RequestID: record.RequestId,
		Data:      record,
		Height:    height,
	})

	return record, nil
}

// ShelveChallenge 搁置挑战（全体见证者仍无共识）
func (cm *ChallengeManager) ShelveChallenge(challengeID string, height uint64) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	record, exists := cm.challenges[challengeID]
	if !exists {
		return ErrChallengeNotFound
	}

	// 标记为搁置状态（通过特殊的轮次值表示）
	record.ArbitrationRound = 0xFFFFFFFF // 特殊值表示搁置

	cm.challenges[challengeID] = record

	return nil
}

// GetShelvedChallenges 获取所有搁置的挑战
func (cm *ChallengeManager) GetShelvedChallenges() []*pb.ChallengeRecord {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	result := make([]*pb.ChallengeRecord, 0)
	for _, record := range cm.challenges {
		if record.ArbitrationRound == 0xFFFFFFFF && !record.Finalized {
			result = append(result, record)
		}
	}
	return result
}

// RetryChallenge 重试搁置的挑战
func (cm *ChallengeManager) RetryChallenge(
	challengeID string,
	newArbitrators []string,
	height uint64,
) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	record, exists := cm.challenges[challengeID]
	if !exists {
		return ErrChallengeNotFound
	}

	// 重置轮次
	record.ArbitrationRound = 0
	record.Arbitrators = newArbitrators
	record.ConsensusThreshold = GetConsensusThreshold(0)
	record.DeadlineHeight = height + cm.config.ArbitrationPeriodBlocks
	record.ArbitrationVotes = nil
	record.PassCount = 0
	record.FailCount = 0

	cm.arbitrationVotes[challengeID] = make(map[string]*pb.WitnessVote)
	cm.challenges[challengeID] = record

	return nil
}

// ClearChallenge 清理挑战数据
func (cm *ChallengeManager) ClearChallenge(challengeID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if record, exists := cm.challenges[challengeID]; exists {
		delete(cm.requestChallenges, record.RequestId)
	}
	delete(cm.challenges, challengeID)
	delete(cm.arbitrationVotes, challengeID)
}

// Events 返回事件通道
func (cm *ChallengeManager) Events() <-chan *Event {
	return cm.eventChan
}

// emitEvent 发送事件
func (cm *ChallengeManager) emitEvent(event *Event) {
	select {
	case cm.eventChan <- event:
	default:
	}
}

