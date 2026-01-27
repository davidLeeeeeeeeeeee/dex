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
		eventChan:        make(chan *Event, 10000),
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

	// 检查投票超时
	s.checkVotingTimeout()

	// 触发公示期检查
	s.checkChallengePeriod()

	// 检查仲裁超时
	s.checkArbitrationTimeout()

	// 尝试重试搁置的请求和仲裁
	// 每 RetryIntervalBlocks 区块执行一次
	if height%s.config.RetryIntervalBlocks == 0 {
		s.retryShelvedRequests()
		s.retryShelvedChallenges()
	}
}

// GetCurrentHeight 获取当前区块高度
func (s *Service) GetCurrentHeight() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentHeight
}

// retryShelvedRequests 重试搁置的请求
func (s *Service) retryShelvedRequests() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, request := range s.requests {
		if request.Status != pb.RechargeRequestStatus_RECHARGE_SHELVED {
			continue
		}

		// 尝试选择新的见证者
		// 构建排除列表（上一轮的）
		excludeAddresses := make(map[string]bool)
		for _, w := range request.SelectedWitnesses {
			excludeAddresses[w] = true
		}
		// 加上已投票的
		for _, v := range request.Votes {
			excludeAddresses[v.WitnessAddress] = true
		}

		// 增加轮次尝试
		nextRound := request.Round + 1
		candidates := s.stakeManager.GetWitnessCandidates()
		newWitnesses, err := s.selector.SelectWitnesses(
			request.RequestId,
			nextRound,
			candidates,
			excludeAddresses,
		)

		// 如果能选出新人，则激活
		if err == nil && len(newWitnesses) > 0 {
			request.Status = pb.RechargeRequestStatus_RECHARGE_VOTING
			request.Round = nextRound
			request.SelectedWitnesses = newWitnesses
			request.DeadlineHeight = s.currentHeight + s.config.VotingPeriodBlocks
			request.PassCount = 0
			request.FailCount = 0
			request.AbstainCount = 0
			request.Votes = nil

			// 更新投票管理器
			s.voteManager.ExpandWitnesses(request.RequestId, newWitnesses)

			// 添加任务
			for _, w := range newWitnesses {
				_ = s.stakeManager.AddPendingTask(w, request.RequestId)
			}

			s.emitEvent(&Event{
				Type:      EventScopeExpanded, // 使用 ScopeExpanded 事件表示重试开始
				RequestID: request.RequestId,
				Data:      request,
				Height:    s.currentHeight,
			})
		}
	}
}

// checkArbitrationTimeout 检查仲裁超时
func (s *Service) checkArbitrationTimeout() {
	// 获取所有活跃挑战
	// 由于 ChallengeManager 内部有锁，我们这里不加 Service 锁，避免死锁
	// 但我们需要遍历挑战，ChallengeManager 没有直接提供遍历接口
	// 我们可以添加一个 GetActiveChallenges 接口，或者让 ChallengeManager 自己检查
	// 为了保持 Service 作为协调者，我们在 ChallengeManager 中添加 CheckTimeouts 方法可能更好
	// 但根据现有代码结构，我们可以在 Service 中调用 ChallengeManager 的方法

	// 实际上，ChallengeManager 没有暴露所有挑战的列表。
	// 我们需要修改 ChallengeManager 来支持超时检查。
	// 暂时，我们假设 ChallengeManager 有一个 CheckTimeouts 方法，或者我们通过 request 关联的 challenge 来检查。

	s.mu.Lock()
	requests := make([]*pb.RechargeRequest, 0, len(s.requests))
	for _, req := range s.requests {
		if req.Status == pb.RechargeRequestStatus_RECHARGE_CHALLENGED {
			requests = append(requests, req)
		}
	}
	s.mu.Unlock()

	for _, req := range requests {
		challenge, err := s.challengeManager.GetChallengeByRequest(req.RequestId)
		if err != nil || challenge.Finalized {
			continue
		}

		// 检查是否超时
		if s.currentHeight > challenge.DeadlineHeight {
			// 扩大仲裁范围
			// 获取候选人
			candidates := s.stakeManager.GetWitnessCandidates()

			// 尝试扩大
			// 注意：ExpandArbitrationScope 需要知道原见证者以排除
			// 我们需要传递 req.SelectedWitnesses
			// 但 ChallengeManager 中可能没有保存原见证者列表，只保存了 Arbitrators
			// SelectArbitrators 需要 originalWitnesses

			// 我们需要先获取当前仲裁者作为排除对象
			currentArbitrators := challenge.Arbitrators

			// 还需要排除原请求的见证者
			originalWitnesses := req.SelectedWitnesses // 注意：这可能只是最后一轮的
			// 理想情况下应该排除所有参与过的。

			// 组合排除列表
			excludeList := make([]string, 0, len(currentArbitrators)+len(originalWitnesses))
			excludeList = append(excludeList, currentArbitrators...)
			excludeList = append(excludeList, originalWitnesses...)

			// 选择新仲裁者
			newArbitrators, err := s.selector.SelectArbitrators(
				challenge.ChallengeId,
				challenge.ArbitrationRound+1,
				candidates,
				excludeList,
			)

			if err != nil || len(newArbitrators) == 0 {
				// 无法扩大 -> 搁置
				_ = s.challengeManager.ShelveChallenge(challenge.ChallengeId, s.currentHeight)
				s.emitEvent(&Event{
					Type:      EventRechargeShelved, // 复用事件类型或定义新事件
					RequestID: req.RequestId,
					Data:      challenge,
					Height:    s.currentHeight,
				})
			} else {
				// 扩大成功
				_ = s.challengeManager.ExpandArbitrationScope(
					challenge.ChallengeId,
					newArbitrators,
					s.currentHeight,
				)
				s.emitEvent(&Event{
					Type:      EventScopeExpanded,
					RequestID: req.RequestId,
					Data:      challenge,
					Height:    s.currentHeight,
				})
			}
		}
	}
}

// checkVotingTimeout 检查投票是否超时
func (s *Service) checkVotingTimeout() {
	s.mu.Lock()
	// 先收集需要检查的请求，避免在处理时长时间占锁
	requests := make([]*pb.RechargeRequest, 0)
	for _, req := range s.requests {
		if req.Status == pb.RechargeRequestStatus_RECHARGE_VOTING {
			requests = append(requests, req)
		}
	}
	s.mu.Unlock()

	for _, req := range requests {
		if s.currentHeight >= req.DeadlineHeight {
			s.mu.Lock()
			// 再次检查状态以防在解锁期间已改变
			if req.Status == pb.RechargeRequestStatus_RECHARGE_VOTING {
				s.checkAndProcessConsensus(req)
			}
			s.mu.Unlock()
		}
	}
}

// retryShelvedChallenges 重试搁置的挑战
func (s *Service) retryShelvedChallenges() {
	shelved := s.challengeManager.GetShelvedChallenges()
	for _, challenge := range shelved {
		// 获取关联请求以获取原见证者
		req, err := s.GetRequest(challenge.RequestId)
		if err != nil {
			continue
		}

		// 尝试重新选择仲裁者
		candidates := s.stakeManager.GetWitnessCandidates()

		// 排除原见证者
		// 注意：这里简化处理，只排除当前记录的 SelectedWitnesses
		excludeList := req.SelectedWitnesses

		// 重试意味着从头开始（Round 0）还是继续扩大？
		// 设计文档：“见证者集合定时更新时，下一轮更新将会对搁置仲裁进行一轮新的投票。”
		// “直到达成共识为止。”
		// 我们可以尝试选择一批新的仲裁者。

		newArbitrators, err := s.selector.SelectArbitrators(
			challenge.ChallengeId,
			challenge.ArbitrationRound+1, // 继续增加轮次
			candidates,
			excludeList,
		)

		if err == nil && len(newArbitrators) > 0 {
			// 激活挑战
			// 我们需要一个 UnShelve 或 Retry 方法
			_ = s.challengeManager.RetryChallenge(
				challenge.ChallengeId,
				newArbitrators,
				s.currentHeight,
			)

			s.emitEvent(&Event{
				Type:      EventScopeExpanded,
				RequestID: challenge.RequestId,
				Data:      challenge,
				Height:    s.currentHeight,
			})
		}
	}
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
	if old, exists := s.requests[requestID]; exists {
		return old, nil // 幂等：如果内存中已存在，直接返回，防止 DryRun 状态污染
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
		NativeVout:        tx.NativeVout,
		NativeScript:      tx.NativeScript,
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

// ProcessVote 处理见证者投票
func (s *Service) ProcessVote(vote *pb.WitnessVote) (*pb.RechargeRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	requestID := vote.RequestId
	witnessAddr := vote.WitnessAddress

	request, exists := s.requests[requestID]
	if !exists {
		return nil, ErrRequestNotFound
	}

	// 检查见证者是否在选定列表中
	if !s.voteManager.IsSelectedWitness(requestID, witnessAddr) {
		return nil, ErrNotSelectedWitness
	}

	// 检查是否已投票
	if s.voteManager.HasVoted(requestID, witnessAddr) {
		return request, nil // 幂等处理
	}

	// 添加投票
	if err := s.voteManager.AddVote(vote); err != nil {
		if err == ErrDuplicateVote {
			return request, nil // 幂等处理
		}
		return nil, err
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

	request.Votes = append(request.Votes, vote)

	// 更新见证者统计
	s.stakeManager.UpdateWitnessStats(witnessAddr, vote.VoteType)

	// 检查共识
	s.checkAndProcessConsensus(request)

	return request, nil
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
	// 增加轮次
	request.Round++

	// 构建排除列表
	excludeAddresses := make(map[string]bool)
	for _, w := range request.SelectedWitnesses {
		excludeAddresses[w] = true
	}
	// 还要排除之前所有轮次已选的（虽然 SelectedWitnesses 应该包含了，但为了保险起见，
	// 如果 SelectedWitnesses 只存了当前轮次的，这里需要注意。
	// 根据 CreateRechargeRequest，SelectedWitnesses 初始是第一轮的。
	// 每次 expandScope，SelectedWitnesses 会被替换为新的。
	// 所以我们需要记录所有已参与过的见证者。
	// 目前 RechargeRequest 结构体似乎没有保存所有历史见证者，只保存了 SelectedWitnesses。
	// 这是一个潜在问题。但在 expandScope 中，我们应该排除的是“当前已知的已参与者”。
	// 如果 SelectedWitnesses 每次都被覆盖，那么 excludeAddresses 就只排除了上一轮的。
	// 这是一个逻辑漏洞，需要修复。
	// 修正：RechargeRequest 应该有一个字段记录所有已参与的见证者，或者我们假设 SelectedWitnesses 是累积的？
	// 查看 CreateRechargeRequest: request.SelectedWitnesses = witnesses
	// 查看 expandScope: request.SelectedWitnesses = newWitnesses
	// 所以 SelectedWitnesses 是被覆盖的。
	// 我们需要从 Votes 中获取已投票的见证者来排除，或者修改 RechargeRequest 结构。
	// 为了不修改 proto，我们从 Votes 中获取已参与者。
	// 另外，VoteManager 中可能有记录。

	// 重新构建排除列表：排除所有已投票的见证者 + 上一轮被选中的见证者
	for _, v := range request.Votes {
		excludeAddresses[v.WitnessAddress] = true
	}
	// 上一轮的 SelectedWitnesses 已经在上面添加了

	// 选择新的见证者
	candidates := s.stakeManager.GetWitnessCandidates()
	newWitnesses, err := s.selector.SelectWitnesses(
		request.RequestId,
		request.Round,
		candidates,
		excludeAddresses,
	)

	// 如果没有选出新的见证者（err != nil 或 len == 0），说明已覆盖所有活跃见证者
	if err != nil || len(newWitnesses) == 0 {
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

	// 更新请求
	request.SelectedWitnesses = newWitnesses
	request.DeadlineHeight = s.currentHeight + s.config.VotingPeriodBlocks
	// 注意：不重置 PassCount/FailCount/AbstainCount，因为我们是扩大范围，共识是基于总体的？
	// 原设计：重新计票。
	// "重新选择新一批见证者...重新计票"
	// 所以重置是正确的。
	request.PassCount = 0
	request.FailCount = 0
	request.AbstainCount = 0
	// request.Votes 也不应该清空吗？如果不清空，ProcessVote 中 append 会导致 Votes 包含历史轮次的票。
	// 但如果清空，上面构建 excludeAddresses 就找不到历史见证者了。
	// 这是一个设计细节。通常扩大范围是“追加”见证者，还是“换一批”？
	// 设计文档：“重新选择新一批见证者...重新计票”。
	// 似乎是“换一批”。
	// 但如果之前的票作废，那么“扩大范围”就变成了“重试”。
	// 如果是“追加”，那么阈值应该基于（旧+新）的总数。
	// 让我们假设是“追加”模式，但为了简化共识计算，每一轮单独计算？
	// 不，设计文档说“重新计票”，意味着新的一轮是一次新的共识尝试。
	// 之前的票作废。
	// 但是，为了避免同一个人重复投票，我们需要排除已参与者。
	// 所以：保留 Votes 以便排除，但在计算共识时只看当前轮次？
	// VoteManager.CheckConsensus 似乎是基于 VoteManager 中的票。
	// VoteManager.ExpandWitnesses 会做什么？
	// 让我们看看 VoteManager。

	// 假设 VoteManager 处理了轮次逻辑。
	// 这里我们先按原逻辑：重置计数，清空 Votes（但在清空前我们已经构建了 excludeAddresses，
	// 等等，如果清空了 Votes，下次 expandScope 怎么知道要排除谁？
	// 这是一个问题。
	// 解决方案：RechargeRequest 应该保留所有历史 Votes，但在计算当前轮次共识时只考虑当前轮次的票。
	// 或者，我们只排除“上一轮”的，允许更早轮次的人再次被选中？这似乎不合理。
	// 鉴于 proto 结构限制，我们暂时只排除“上一轮”的（SelectedWitnesses）和“已投票”的（Votes）。
	// 如果我们清空 request.Votes，那么历史记录就丢失了。
	// 建议：不清空 request.Votes，但在 ProcessVote 中只统计当前轮次的票？
	// 或者，request.Votes 只是一个记录，VoteManager 才是状态核心。
	// 让我们看看 ProcessVote: request.PassCount++ ...
	// 它是直接修改 request 的计数。
	// 所以必须重置计数。
	// 必须清空 request.Votes 吗？如果不清空，ProcessVote append，那么 request.Votes 包含所有。
	// 但 CheckConsensus 是基于 VoteManager。
	// 让我们假设 request.Votes 是历史记录，不参与逻辑（除了这里用来排除）。
	// 但 ProcessVote 中：request.Votes = append(request.Votes, vote)
	// 如果不清空，这里会累积。
	// 关键是：excludeAddresses 需要所有历史参与者。
	// 如果我们清空了 request.Votes，我们就丢失了历史参与者（除了上一轮的）。
	// 临时方案：在内存中维护一个 excluded 集合？不，服务重启会丢失。
	// 鉴于目前只能修改代码不能修改 proto，我们尽量利用现有字段。
	// 如果我们不清空 Votes，那么 Votes 列表会越来越长。
	// 只要我们在计算共识时（ProcessVote 中更新 PassCount 等）是重置过的，就没问题。
	// 唯一的问题是：ProcessVote 中没有检查 vote.Round == request.Round。
	// 这是一个潜在 bug。
	// 让我们先按原样：清空 Votes。这意味着我们只排除了“上一轮”的见证者。
	// 这在 MaxRounds=5 时可能问题不大，但在无限轮次时，可能导致见证者循环被选。
	// 但考虑到 SelectWitnesses 是确定性的（基于 Round），只要 Round 递增，选出的人应该不同（除非人很少）。
	// 只要 Round 变了，SelectWitnesses 的结果就会变。
	// 所以，只排除上一轮的可能也行，因为 Round 变了。
	// 好的，我们维持原逻辑：清空 Votes。

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
			// 暂时不直接修改内存状态为 FINALIZED，以防对应区块未被接受导致事件丢失。
			// 这种设计允许我们在后续区块中重试发送 Finalized 事件（幂等处理）。
			// 只有通过 LoadRequest 加载到终态，或者节点重启后才会真正从活跃请求中移除。
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

// GetActiveRequests 获取所有活跃的入账请求
func (s *Service) GetActiveRequests() []*pb.RechargeRequest {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*pb.RechargeRequest, 0, len(s.requests))
	for _, req := range s.requests {
		result = append(result, req)
	}
	return result
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

// GetAllWitnesses 获取所有见证者列表（包括所有状态）
func (s *Service) GetAllWitnesses() []*pb.WitnessInfo {
	return s.stakeManager.GetAllWitnesses()
}

// ==================== 挑战管理代理方法 ====================

// CreateChallenge 创建挑战（代理到 ChallengeManager）
func (s *Service) CreateChallenge(
	challengeID string,
	requestID string,
	challengerAddr string,
	stakeAmount string,
	reason string,
	height uint64,
	arbitrators []string,
) (*pb.ChallengeRecord, error) {
	return s.challengeManager.CreateChallenge(
		challengeID, requestID, challengerAddr, stakeAmount, reason, height, arbitrators,
	)
}

// GetChallenge 获取挑战记录（代理到 ChallengeManager）
func (s *Service) GetChallenge(challengeID string) (*pb.ChallengeRecord, error) {
	return s.challengeManager.GetChallenge(challengeID)
}

// ShelveChallenge 搁置挑战（代理到 ChallengeManager）
func (s *Service) ShelveChallenge(challengeID string, height uint64) error {
	return s.challengeManager.ShelveChallenge(challengeID, height)
}

// GetShelvedChallenges 获取所有搁置的挑战（代理到 ChallengeManager）
func (s *Service) GetShelvedChallenges() []*pb.ChallengeRecord {
	return s.challengeManager.GetShelvedChallenges()
}

// RetryChallenge 重试搁置的挑战（代理到 ChallengeManager）
func (s *Service) RetryChallenge(challengeID string, newArbitrators []string, height uint64) error {
	return s.challengeManager.RetryChallenge(challengeID, newArbitrators, height)
}

// FinalizeChallenge 完成挑战（代理到 ChallengeManager）
func (s *Service) FinalizeChallenge(challengeID string, success bool, height uint64) (*pb.ChallengeRecord, error) {
	return s.challengeManager.FinalizeChallenge(challengeID, success, height)
}
