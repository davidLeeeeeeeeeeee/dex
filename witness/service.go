// witness/service.go
// 见证者服务 - 纯内存计算模块，用于辅助 VM Handler 进行见证者选择、共识计算等
// 注意：所有状态持久化都通过 VM Handler 的 WriteOp 机制完成，本模块不直接操作数据库
package witness

import (
	"context"
	"dex/pb"
	"fmt"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

// Service 见证者服务（纯内存计算模块）
// 职责：
// 1. 见证者选择算法
// 2. 共识计算
// 3. 公示期/仲裁期检查
// 4. 事件通知
// 注意：不负责状态持久化，所有写操作由 VM Handler 通过 WriteOp 完成
type Service struct {
	mu     sync.RWMutex
	config *Config

	// 子组件
	selector         *Selector
	voteManager      *VoteManager
	stakeManager     *StakeManager
	challengeManager *ChallengeManager

	// 内存缓存（用于辅助计算，不作为持久化来源）
	requests map[string]*pb.RechargeRequest // requestID -> request

	// 当前区块高度
	currentHeight uint64

	// 事件通道
	eventChan chan *Event

	// 上下文
	ctx    context.Context
	cancel context.CancelFunc
}

// NewService 创建见证者服务（纯内存计算模块）
// 注意：不再接受 db 参数，所有持久化由 VM Handler 负责
func NewService(config *Config) *Service {
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
		eventChan:        make(chan *Event, 100),
		ctx:              ctx,
		cancel:           cancel,
	}
}

// Start 启动服务
func (s *Service) Start() error {
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
	// 注意：这里不能加锁，因为 checkChallengePeriod 会加锁
	// 但 s.currentHeight 的更新需要保护，或者 checkChallengePeriod 内部处理
	// 更好的方式是将 SetCurrentHeight 仅更新高度，然后异步触发检查，或者在外部调用检查
	// 为了简单起见，我们先更新高度，然后在一个独立的 goroutine 或不加锁的情况下调用检查
	// 但由于 checkChallengePeriod 需要访问 s.requests (受 mu 保护)，所以它必须加锁

	s.mu.Lock()
	s.currentHeight = height
	s.mu.Unlock()

	// 触发周期性检查
	s.checkChallengePeriod()
}

// GetCurrentHeight 获取当前区块高度
func (s *Service) GetCurrentHeight() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentHeight
}

// ==================== 质押相关 ====================

// ProcessStake 处理质押（内存计算，不持久化）
// 注意：实际持久化由 VM WitnessStakeTxHandler 通过 WriteOp 完成
func (s *Service) ProcessStake(address string, amount decimal.Decimal) (*pb.WitnessInfo, error) {
	// 验证
	if err := s.stakeManager.ValidateStake(address, amount); err != nil {
		return nil, err
	}

	// 处理质押（仅更新内存状态）
	info, err := s.stakeManager.ProcessStake(address, amount, s.GetCurrentHeight())
	if err != nil {
		return nil, err
	}

	return info, nil
}

// ProcessUnstake 处理解质押（内存计算，不持久化）
// 注意：实际持久化由 VM WitnessStakeTxHandler 通过 WriteOp 完成
func (s *Service) ProcessUnstake(address string) (*pb.WitnessInfo, error) {
	// 验证
	if err := s.stakeManager.ValidateUnstake(address); err != nil {
		return nil, err
	}

	// 处理解质押（仅更新内存状态）
	info, err := s.stakeManager.ProcessUnstake(address, s.GetCurrentHeight())
	if err != nil {
		return nil, err
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
		RechargeFee:       tx.RechargeFee,
	}

	// 设置投票管理器
	s.voteManager.SetSelectedWitnesses(requestID, witnesses)

	// 为每个见证者添加待处理任务
	for _, w := range witnesses {
		_ = s.stakeManager.AddPendingTask(w, requestID)
	}

	// 保存到内存缓存（用于后续共识计算）
	s.requests[requestID] = request

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

	// 保存投票到请求（内存缓存）
	request.Votes = append(request.Votes, vote)

	// 更新见证者统计（内存）
	_ = s.stakeManager.UpdateWitnessStats(vote.WitnessAddress, vote.VoteType)

	// 检查共识
	s.checkAndProcessConsensus(request)

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

// checkChallengePeriod 检查公示期是否结束
func (s *Service) checkChallengePeriod() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, request := range s.requests {
		if request.Status != pb.RechargeRequestStatus_RECHARGE_CHALLENGE_PERIOD {
			continue
		}

		// 检查是否过期
		if s.currentHeight > request.DeadlineHeight {
			// 公示期结束且无挑战，自动完成
			request.Status = pb.RechargeRequestStatus_RECHARGE_FINALIZED
			request.FinalizeHeight = s.currentHeight

			// 分配奖励给投 PASS 的见证者
			honestWitnesses := s.voteManager.GetPassVotes(request.RequestId)
			witnessAddrs := make([]string, 0, len(honestWitnesses))
			for _, v := range honestWitnesses {
				witnessAddrs = append(witnessAddrs, v.WitnessAddress)
			}

			fee, _ := decimal.NewFromString(request.RechargeFee)
			if fee.IsPositive() {
				_ = s.stakeManager.DistributeReward(witnessAddrs, fee)
			}

			s.emitEvent(&Event{
				Type:      EventRechargeFinalized,
				RequestID: request.RequestId,
				Data:      request,
				Height:    s.currentHeight,
			})
		}
	}
}

// handleChallengeResolved 处理挑战解决
func (s *Service) handleChallengeResolved(record *pb.ChallengeRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	request, exists := s.requests[record.RequestId]
	if !exists {
		return
	}

	if record.ChallengeSuccess {
		// 挑战成功 -> 入账失败
		request.Status = pb.RechargeRequestStatus_RECHARGE_REJECTED

		// 诚实者是投 FAIL 的人
		// 注意：这里需要从原始投票中获取投 FAIL 的人，而不是仲裁投票
		// 但原始投票数据可能在 VoteManager 中，或者直接从 Request 中获取
		honestWitnesses := make([]string, 0)
		for _, v := range request.Votes {
			if v.VoteType == pb.WitnessVoteType_VOTE_FAIL {
				honestWitnesses = append(honestWitnesses, v.WitnessAddress)
			}
		}

		fee, _ := decimal.NewFromString(request.RechargeFee)
		if fee.IsPositive() {
			_ = s.stakeManager.DistributeReward(honestWitnesses, fee)
		}

		s.emitEvent(&Event{
			Type:      EventRechargeRejected,
			RequestID: request.RequestId,
			Data:      request,
			Height:    s.currentHeight,
		})
	} else {
		// 挑战失败 -> 入账成功
		request.Status = pb.RechargeRequestStatus_RECHARGE_FINALIZED
		request.FinalizeHeight = s.currentHeight

		// 诚实者是投 PASS 的人
		honestWitnesses := make([]string, 0)
		for _, v := range request.Votes {
			if v.VoteType == pb.WitnessVoteType_VOTE_PASS {
				honestWitnesses = append(honestWitnesses, v.WitnessAddress)
			}
		}

		fee, _ := decimal.NewFromString(request.RechargeFee)
		if fee.IsPositive() {
			_ = s.stakeManager.DistributeReward(honestWitnesses, fee)
		}

		s.emitEvent(&Event{
			Type:      EventRechargeFinalized,
			RequestID: request.RequestId,
			Data:      request,
			Height:    s.currentHeight,
		})
	}
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
			if event.Type == EventChallengeResolved {
				if record, ok := event.Data.(*pb.ChallengeRecord); ok {
					s.handleChallengeResolved(record)
				}
			}
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

// Reset 重置内存状态（用于回滚场景）
// 注意：由于不再持有 db 引用，需要外部传入数据来重置状态
func (s *Service) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 清空内存缓存
	s.requests = make(map[string]*pb.RechargeRequest)
	s.stakeManager.Reset()
	s.voteManager.Reset()
	s.challengeManager.Reset()
}

// LoadWitness 加载见证者信息到内存（供 VM 初始化时调用）
func (s *Service) LoadWitness(info *pb.WitnessInfo) {
	s.stakeManager.LoadWitness(info)
}

// LoadRequest 加载入账请求到内存（供 VM 初始化时调用）
func (s *Service) LoadRequest(request *pb.RechargeRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.requests[request.RequestId] = request
	s.voteManager.SetSelectedWitnesses(request.RequestId, request.SelectedWitnesses)
	for _, v := range request.Votes {
		_ = s.voteManager.AddVote(v)
	}
}

// LoadChallenge 加载挑战记录到内存（供 VM 初始化时调用）
func (s *Service) LoadChallenge(record *pb.ChallengeRecord) {
	s.challengeManager.LoadChallenge(record)
}

// GetActiveWitnesses 获取活跃见证者列表
func (s *Service) GetActiveWitnesses() []*pb.WitnessInfo {
	return s.stakeManager.GetActiveWitnesses()
}
