// witness/service.go
// 见证者服务 - 整合所有组件，提供统一的服务接口
package witness

import (
	"context"
	"dex/pb"
	"fmt"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

// Service 见证者服务
type Service struct {
	mu     sync.RWMutex
	config *Config

	// 子组件
	selector         *Selector
	voteManager      *VoteManager
	stakeManager     *StakeManager
	challengeManager *ChallengeManager

	// 入账请求缓存
	requests map[string]*pb.RechargeRequest // requestID -> request

	// 数据库接口
	db DBInterface

	// 当前区块高度
	currentHeight uint64

	// 事件通道
	eventChan chan *Event

	// 上下文
	ctx    context.Context
	cancel context.CancelFunc
}

// DBInterface 数据库接口（解耦）
type DBInterface interface {
	GetWitnessInfo(address string) (*pb.WitnessInfo, error)
	SaveWitnessInfo(info *pb.WitnessInfo) error
	GetRechargeRequest(requestID string) (*pb.RechargeRequest, error)
	SaveRechargeRequest(request *pb.RechargeRequest) error
	GetChallengeRecord(challengeID string) (*pb.ChallengeRecord, error)
	SaveChallengeRecord(record *pb.ChallengeRecord) error
	GetActiveWitnesses() ([]*pb.WitnessInfo, error)
}

// NewService 创建见证者服务
func NewService(config *Config, db DBInterface) *Service {
	if config == nil {
		config = DefaultConfig()
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Service{
		config:           config,
		selector:         NewSelector(config),
		voteManager:      NewVoteManager(config),
		stakeManager:     NewStakeManager(config),
		challengeManager: NewChallengeManager(config),
		requests:         make(map[string]*pb.RechargeRequest),
		db:               db,
		eventChan:        make(chan *Event, 100),
		ctx:              ctx,
		cancel:           cancel,
	}
}

// Start 启动服务
func (s *Service) Start() error {
	// 加载活跃见证者
	if s.db != nil {
		witnesses, err := s.db.GetActiveWitnesses()
		if err == nil {
			for _, w := range witnesses {
				s.stakeManager.LoadWitness(w)
			}
		}
	}

	// 启动事件处理
	go s.eventLoop()

	return nil
}

// Stop 停止服务
func (s *Service) Stop() {
	s.cancel()
}

// SetCurrentHeight 设置当前区块高度
func (s *Service) SetCurrentHeight(height uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentHeight = height
}

// GetCurrentHeight 获取当前区块高度
func (s *Service) GetCurrentHeight() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentHeight
}

// ==================== 质押相关 ====================

// ProcessStake 处理质押
func (s *Service) ProcessStake(address string, amount decimal.Decimal) (*pb.WitnessInfo, error) {
	// 验证
	if err := s.stakeManager.ValidateStake(address, amount); err != nil {
		return nil, err
	}

	// 处理质押
	info, err := s.stakeManager.ProcessStake(address, amount, s.GetCurrentHeight())
	if err != nil {
		return nil, err
	}

	// 持久化
	if s.db != nil {
		if err := s.db.SaveWitnessInfo(info); err != nil {
			return nil, fmt.Errorf("failed to save witness info: %w", err)
		}
	}

	return info, nil
}

// ProcessUnstake 处理解质押
func (s *Service) ProcessUnstake(address string) (*pb.WitnessInfo, error) {
	// 验证
	if err := s.stakeManager.ValidateUnstake(address); err != nil {
		return nil, err
	}

	// 处理解质押
	info, err := s.stakeManager.ProcessUnstake(address, s.GetCurrentHeight())
	if err != nil {
		return nil, err
	}

	// 持久化
	if s.db != nil {
		if err := s.db.SaveWitnessInfo(info); err != nil {
			return nil, fmt.Errorf("failed to save witness info: %w", err)
		}
	}

	return info, nil
}

// ==================== 入账请求相关 ====================

// CreateRechargeRequest 创建入账请求
func (s *Service) CreateRechargeRequest(tx *pb.WitnessRequestTx) (*pb.RechargeRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	requestID := tx.Base.TxId

	// 检查是否已存在
	if _, exists := s.requests[requestID]; exists {
		return nil, ErrRequestAlreadyExists
	}

	// 验证原生链
	if !IsSupportedChain(tx.NativeChain) {
		return nil, ErrNativeChainNotSupport
	}

	height := s.currentHeight

	// 选择见证者
	candidates := s.stakeManager.GetWitnessCandidates()
	witnesses, err := s.selector.SelectWitnesses(requestID, 0, candidates, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to select witnesses: %w", err)
	}

	// 创建请求
	request := &pb.RechargeRequest{
		RequestId:         requestID,
		NativeChain:       tx.NativeChain,
		NativeTxHash:      tx.NativeTxHash,
		TokenAddress:      tx.TokenAddress,
		Amount:            tx.Amount,
		ReceiverAddress:   tx.ReceiverAddress,
		RequesterAddress:  tx.Base.FromAddress,
		Status:            pb.RechargeRequestStatus_RECHARGE_VOTING,
		CreateHeight:      height,
		DeadlineHeight:    height + s.config.VotingPeriodBlocks,
		Round:             0,
		SelectedWitnesses: witnesses,
	}

	// 设置投票管理器
	s.voteManager.SetSelectedWitnesses(requestID, witnesses)

	// 为每个见证者添加待处理任务
	for _, w := range witnesses {
		_ = s.stakeManager.AddPendingTask(w, requestID)
	}

	// 保存到缓存
	s.requests[requestID] = request

	// 持久化
	if s.db != nil {
		if err := s.db.SaveRechargeRequest(request); err != nil {
			return nil, fmt.Errorf("failed to save request: %w", err)
		}
	}

	// 发送事件
	s.emitEvent(&Event{
		Type:      EventRechargeRequested,
		RequestID: requestID,
		Data:      request,
		Height:    height,
	})

	return request, nil
}

// ProcessVote 处理投票
func (s *Service) ProcessVote(vote *pb.WitnessVote) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	requestID := vote.RequestId

	// 获取请求
	request, exists := s.requests[requestID]
	if !exists {
		return ErrRequestNotFound
	}

	// 检查状态
	if request.Status != pb.RechargeRequestStatus_RECHARGE_VOTING {
		return fmt.Errorf("request is not in voting status")
	}

	// 检查是否过期
	if s.currentHeight > request.DeadlineHeight {
		return ErrVotingPeriodExpired
	}

	// 添加投票
	if err := s.voteManager.AddVote(vote); err != nil {
		return err
	}

	// 更新统计
	switch vote.VoteType {
	case pb.WitnessVoteType_VOTE_PASS:
		request.PassCount++
	case pb.WitnessVoteType_VOTE_FAIL:
		request.FailCount++
	case pb.WitnessVoteType_VOTE_ABSTAIN:
		request.AbstainCount++
	}

	// 保存投票到请求
	request.Votes = append(request.Votes, vote)

	// 更新见证者统计
	_ = s.stakeManager.UpdateWitnessStats(vote.WitnessAddress, vote.VoteType)

	// 检查共识
	s.checkAndProcessConsensus(request)

	// 持久化
	if s.db != nil {
		_ = s.db.SaveRechargeRequest(request)
	}

	return nil
}

// checkAndProcessConsensus 检查并处理共识
func (s *Service) checkAndProcessConsensus(request *pb.RechargeRequest) {
	isDeadline := s.currentHeight >= request.DeadlineHeight
	isAllVoted := s.voteManager.IsAllVoted(request.RequestId)

	// 提前检查是否无法达成共识
	if !s.voteManager.CanReachConsensus(request.RequestId) {
		s.expandScope(request)
		return
	}

	result := s.voteManager.CheckConsensus(request.RequestId, isDeadline || isAllVoted)

	switch result {
	case ConsensusPass:
		s.handleConsensusPass(request)
	case ConsensusFail:
		s.handleConsensusFail(request)
	case ConsensusExpand:
		s.expandScope(request)
	case ConsensusConflict:
		s.handleConflict(request)
	}
}

// handleConsensusPass 处理共识通过
func (s *Service) handleConsensusPass(request *pb.RechargeRequest) {
	// 更新状态为公示期
	// 注意：见证者通过提交交易投票，交易本身已有签名验证，无需额外聚合签名
	request.Status = pb.RechargeRequestStatus_RECHARGE_CHALLENGE_PERIOD
	request.DeadlineHeight = s.currentHeight + s.config.ChallengePeriodBlocks

	// 移除见证者的待处理任务
	for _, w := range request.SelectedWitnesses {
		_ = s.stakeManager.RemovePendingTask(w, request.RequestId)
	}

	s.emitEvent(&Event{
		Type:      EventChallengePeriodStart,
		RequestID: request.RequestId,
		Data:      request,
		Height:    s.currentHeight,
	})
}

// handleConsensusFail 处理共识失败
func (s *Service) handleConsensusFail(request *pb.RechargeRequest) {
	request.Status = pb.RechargeRequestStatus_RECHARGE_REJECTED

	// 移除见证者的待处理任务
	for _, w := range request.SelectedWitnesses {
		_ = s.stakeManager.RemovePendingTask(w, request.RequestId)
	}

	s.emitEvent(&Event{
		Type:      EventRechargeRejected,
		RequestID: request.RequestId,
		Data:      request,
		Height:    s.currentHeight,
	})
}

// expandScope 扩大范围
func (s *Service) expandScope(request *pb.RechargeRequest) {
	// 检查是否达到最大轮次
	if request.Round >= s.config.MaxRounds {
		// 搁置请求
		request.Status = pb.RechargeRequestStatus_RECHARGE_SHELVED
		s.emitEvent(&Event{
			Type:      EventRechargeShelved,
			RequestID: request.RequestId,
			Data:      request,
			Height:    s.currentHeight,
		})
		return
	}

	// 增加轮次
	request.Round++

	// 构建排除列表
	excludeAddresses := make(map[string]bool)
	for _, w := range request.SelectedWitnesses {
		excludeAddresses[w] = true
	}

	// 选择新的见证者
	candidates := s.stakeManager.GetWitnessCandidates()
	newWitnesses, err := s.selector.SelectWitnesses(
		request.RequestId,
		request.Round,
		candidates,
		excludeAddresses,
	)
	if err != nil {
		return
	}

	// 更新请求
	request.SelectedWitnesses = newWitnesses
	request.DeadlineHeight = s.currentHeight + s.config.VotingPeriodBlocks
	request.PassCount = 0
	request.FailCount = 0
	request.AbstainCount = 0
	request.Votes = nil

	// 更新投票管理器
	s.voteManager.ExpandWitnesses(request.RequestId, newWitnesses)

	// 为新见证者添加待处理任务
	for _, w := range newWitnesses {
		_ = s.stakeManager.AddPendingTask(w, request.RequestId)
	}

	s.emitEvent(&Event{
		Type:      EventScopeExpanded,
		RequestID: request.RequestId,
		Data:      request,
		Height:    s.currentHeight,
	})
}

// handleConflict 处理矛盾结果（自动进入挑战）
func (s *Service) handleConflict(request *pb.RechargeRequest) {
	request.Status = pb.RechargeRequestStatus_RECHARGE_CHALLENGED

	// 自动创建挑战（系统发起）
	challengeID := fmt.Sprintf("auto_%s", request.RequestId)

	// 选择仲裁者
	candidates := s.stakeManager.GetWitnessCandidates()
	arbitrators, _ := s.selector.SelectArbitrators(
		challengeID,
		0,
		candidates,
		request.SelectedWitnesses,
	)

	_, _ = s.challengeManager.CreateChallenge(
		challengeID,
		request.RequestId,
		"system",
		"0",
		"auto challenge due to conflict",
		s.currentHeight,
		arbitrators,
	)

	request.ChallengeId = challengeID
}

// Events 返回事件通道
func (s *Service) Events() <-chan *Event {
	return s.eventChan
}

// eventLoop 事件处理循环
func (s *Service) eventLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case event := <-s.stakeManager.Events():
			s.eventChan <- event
		case event := <-s.voteManager.Events():
			s.eventChan <- event
		case event := <-s.challengeManager.Events():
			s.eventChan <- event
		}
	}
}

// emitEvent 发送事件
func (s *Service) emitEvent(event *Event) {
	event.Timestamp = time.Now().Unix()
	select {
	case s.eventChan <- event:
	default:
	}
}

// GetRequest 获取入账请求
func (s *Service) GetRequest(requestID string) (*pb.RechargeRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	request, exists := s.requests[requestID]
	if !exists {
		return nil, ErrRequestNotFound
	}
	return request, nil
}

// GetWitnessInfo 获取见证者信息
func (s *Service) GetWitnessInfo(address string) (*pb.WitnessInfo, error) {
	return s.stakeManager.GetWitness(address)
}

// GetActiveWitnesses 获取活跃见证者列表
func (s *Service) GetActiveWitnesses() []*pb.WitnessInfo {
	return s.stakeManager.GetActiveWitnesses()
}

