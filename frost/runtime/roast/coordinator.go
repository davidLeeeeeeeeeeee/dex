// frost/runtime/roast/coordinator.go
// ROAST Coordinator: 协调签名会话，收集 nonce 承诺和签名份额

package roast

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"math/big"
	"sync"
	"time"

	roastsession "dex/frost/runtime/session"
	"dex/logs"
	"dex/pb"
)

// ========== 错误定义 ==========

var (
	// ErrCoordinatorNotLeader 不是当前协调者
	ErrCoordinatorNotLeader = errors.New("not current coordinator")
	// ErrSessionNotFound 会话不存在
	ErrSessionNotFound = errors.New("session not found")
	// ErrSessionAlreadyExists 会话已存在
	ErrSessionAlreadyExists = errors.New("session already exists")
	// ErrInsufficientParticipants 参与者不足
	ErrInsufficientParticipants = errors.New("insufficient participants")
	// ErrInvalidRoastPayload indicates a malformed ROAST payload.
	ErrInvalidRoastPayload = errors.New("invalid roast payload")
	// ErrParticipantNotFound indicates the sender is not in the committee.
	ErrParticipantNotFound = errors.New("participant not found")
	// ErrInsufficientNonces indicates not enough nonces for aggregation.
	ErrInsufficientNonces = errors.New("insufficient nonces for aggregation")
	// ErrInsufficientShares indicates not enough shares for aggregation.
	ErrInsufficientShares = errors.New("insufficient shares for aggregation")
)

// ========== 配置 ==========

// CoordinatorConfig 协调者配置
type CoordinatorConfig struct {
	// 超时配置
	NonceCollectTimeout    time.Duration // nonce 收集超时
	ShareCollectTimeout    time.Duration // share 收集超时
	AggregatorRotateBlocks uint64        // 聚合者轮换区块数

	// 重试配置
	MaxRetries int // 最大重试次数
}

// DefaultCoordinatorConfig 默认配置
func DefaultCoordinatorConfig() *CoordinatorConfig {
	return &CoordinatorConfig{
		NonceCollectTimeout:    10 * time.Second,
		ShareCollectTimeout:    10 * time.Second,
		AggregatorRotateBlocks: 10,
		MaxRetries:             5,
	}
}

// ========== Coordinator ==========

// Coordinator ROAST 协调者
// 负责协调签名会话、收集 nonce 承诺和签名份额
type Coordinator struct {
	mu sync.RWMutex

	// 配置
	config *CoordinatorConfig
	nodeID NodeID

	// 会话管理
	sessions map[string]*roastsession.Session // jobID -> session

	// 依赖
	messenger     RoastMessenger
	vaultProvider VaultCommitteeProvider
	cryptoFactory CryptoExecutorFactory // 密码学执行器工厂
	sessionStore  *roastsession.SessionStore
	Logger        logs.Logger

	// 当前区块高度（用于协调者选举）
	currentHeight uint64
}

// NewCoordinator 创建协调者
func NewCoordinator(nodeID NodeID, messenger RoastMessenger, vaultProvider VaultCommitteeProvider, cryptoFactory CryptoExecutorFactory, logger logs.Logger) *Coordinator {
	return &Coordinator{
		config:        DefaultCoordinatorConfig(),
		nodeID:        nodeID,
		sessions:      make(map[string]*roastsession.Session),
		messenger:     messenger,
		vaultProvider: vaultProvider,
		cryptoFactory: cryptoFactory,
		Logger:        logger,
	}
}

// UpdateHeight 更新当前区块高度
func (c *Coordinator) UpdateHeight(height uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.currentHeight = height
}

// StartSession 启动新的签名会话
func (c *Coordinator) StartSession(ctx context.Context, params *StartSessionParams) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.sessions[params.JobID]; exists {
		return ErrSessionAlreadyExists
	}

	// 获取 Vault 委员会
	committeeInfo, err := c.vaultProvider.VaultCommittee(params.Chain, params.VaultID, params.KeyEpoch)
	if err != nil {
		return err
	}

	if len(committeeInfo) < params.Threshold {
		return ErrInsufficientParticipants
	}

	// 查找本节点在委员会中的索引
	myIndex := -1
	committee := make([]roastsession.Participant, len(committeeInfo))
	for i, member := range committeeInfo {
		committee[i] = roastsession.Participant{
			ID:    string(member.ID),
			Index: i,
		}
		if member.ID == c.nodeID {
			myIndex = i
		}
	}

	// 创建会话
	sess := roastsession.NewSession(roastsession.SessionParams{
		JobID:       params.JobID,
		VaultID:     params.VaultID,
		Chain:       params.Chain,
		KeyEpoch:    params.KeyEpoch,
		SignAlgo:    int32(params.SignAlgo),
		Messages:    params.Messages,
		Committee:   committee,
		Threshold:   params.Threshold,
		MyIndex:     myIndex,
		StartHeight: c.currentHeight,
	})

	c.sessions[params.JobID] = sess

	// 如果本节点是协调者，开始收集 nonce
	if c.isCurrentCoordinator(sess) {
		if err := sess.Start(); err != nil {
			return err
		}
		go c.runCoordinatorLoop(ctx, sess)
	}

	return nil
}

// StartSessionParams 启动会话参数
type StartSessionParams struct {
	JobID        string
	maxInFlight  int // 最多并发 job 数
	localAddress string
	Logger       logs.Logger
	VaultID      uint32
	Chain        string
	KeyEpoch     uint64
	SignAlgo     pb.SignAlgo
	Messages     [][]byte // 待签名消息列表
	Threshold    int
}

// isCurrentCoordinator 检查本节点是否是当前协调者
func (c *Coordinator) isCurrentCoordinator(sess *roastsession.Session) bool {
	if len(sess.Committee) == 0 {
		return false
	}

	// 计算当前协调者索引
	coordIndex := c.computeCoordinatorIndex(sess)
	return sess.MyIndex == coordIndex
}

// computeCoordinatorIndex 计算当前协调者索引
// 确定性算法：基于 session_id、key_epoch 和区块高度
// 超时自动切换：agg_index = floor((now_height - session_start_height) / agg_timeout_blocks) % len(agg_candidates)
func (c *Coordinator) computeCoordinatorIndex(sess *roastsession.Session) int {
	if len(sess.Committee) == 0 {
		return 0
	}

	// 计算种子：seed = H(session_id || key_epoch || "frost_agg")
	seed := computeAggregatorSeed(sess.JobID, sess.KeyEpoch)

	// 确定性排列委员会（基于种子）
	permuted := permuteCommittee(sess.Committee, seed)

	// 计算轮换次数（超时切换）
	// agg_index = floor((now_height - session_start_height) / agg_timeout_blocks) % len(agg_candidates)
	blocksElapsed := c.currentHeight - sess.StartHeight
	rotations := blocksElapsed / c.config.AggregatorRotateBlocks
	aggIndex := int(rotations) % len(permuted)

	// 找回原始索引
	for i, member := range sess.Committee {
		if member.ID == permuted[aggIndex].ID {
			return i
		}
	}
	return 0
}

// shouldRotateCoordinator 检查是否应该切换协调者（超时切换）
func (c *Coordinator) shouldRotateCoordinator(sess *roastsession.Session) bool {
	// 计算自会话开始以来经过的区块数
	blocksElapsed := c.currentHeight - sess.StartHeight

	// 如果超过轮换区块数，且当前协调者索引已变化，则需要切换
	if blocksElapsed >= c.config.AggregatorRotateBlocks {
		// 计算新的协调者索引
		newCoordIndex := c.computeCoordinatorIndex(sess)
		// 如果新的协调者不是本节点，则需要切换
		return sess.MyIndex != newCoordIndex
	}

	return false
}

// computeAggregatorSeed 计算聚合者选举种子
func computeAggregatorSeed(jobID string, keyEpoch uint64) []byte {
	data := jobID + "|" + string(rune(keyEpoch)) + "|frost_agg"
	hash := sha256.Sum256([]byte(data))
	return hash[:]
}

// permuteCommittee 确定性排列委员会
func permuteCommittee(committee []roastsession.Participant, seed []byte) []roastsession.Participant {
	result := make([]roastsession.Participant, len(committee))
	copy(result, committee)

	// 使用 Fisher-Yates 洗牌，种子决定随机序列
	for i := len(result) - 1; i > 0; i-- {
		// 从种子派生确定性随机数
		indexSeed := sha256.Sum256(append(seed, byte(i)))
		j := int(indexSeed[0]) % (i + 1)
		result[i], result[j] = result[j], result[i]
	}

	return result
}

// runCoordinatorLoop 协调者主循环
func (c *Coordinator) runCoordinatorLoop(ctx context.Context, sess *roastsession.Session) {
	logs.Info("[Coordinator] starting session %s", sess.JobID)

	// 定期检查协调者切换的 ticker
	rotateTicker := time.NewTicker(1 * time.Second)
	defer rotateTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-rotateTicker.C:
			// 检查是否需要切换协调者（超时切换）
			if !c.isCurrentCoordinator(sess) {
				// 本节点不再是协调者，停止循环
				logs.Info("[Coordinator] no longer coordinator for session %s, stopping", sess.JobID)
				return
			}
		default:
		}

		state := sess.GetState()

		switch state {
		case roastsession.SignSessionStateCollectingNonces:
			// 广播 nonce 请求
			c.broadcastNonceRequest(sess)

			// 等待收集或超时
			if c.waitForNonces(ctx, sess) {
				sess.SetState(roastsession.SignSessionStateCollectingShares)
			} else {
				// 超时，检查是否需要切换协调者
				if c.shouldRotateCoordinator(sess) {
					logs.Info("[Coordinator] rotating coordinator for session %s due to timeout", sess.JobID)
					// 停止当前循环，让新的协调者接管
					return
				}
				// 否则尝试重试
				if !c.retryWithNewSet(sess) {
					sess.SetState(roastsession.SignSessionStateFailed)
					return
				}
			}

		case roastsession.SignSessionStateCollectingShares:
			// 广播签名请求（包含聚合的 nonce）
			c.broadcastSignRequest(sess)

			// 等待收集或超时
			if c.waitForShares(ctx, sess) {
				sess.SetState(roastsession.SignSessionStateAggregating)
			} else {
				// 超时，检查是否需要切换协调者
				if c.shouldRotateCoordinator(sess) {
					logs.Info("[Coordinator] rotating coordinator for session %s due to timeout", sess.JobID)
					// 停止当前循环，让新的协调者接管
					return
				}
				// 否则尝试重试
				if !c.retryWithNewSet(sess) {
					sess.SetState(roastsession.SignSessionStateFailed)
					return
				}
			}

		case roastsession.SignSessionStateAggregating:
			// 聚合签名（支持部分完成）
			signatures, err := c.aggregateSignatures(sess)
			if err != nil {
				logs.Error("[Coordinator] aggregate failed: %v", err)
				// 检查是否需要切换协调者
				if c.shouldRotateCoordinator(sess) {
					logs.Info("[Coordinator] rotating coordinator for session %s due to aggregate failure", sess.JobID)
					return
				}
				// 否则尝试重试（对未完成的 task 继续收集）
				if !c.retryWithNewSet(sess) {
					sess.SetState(roastsession.SignSessionStateFailed)
					return
				}
				// 重试时回到收集 share 状态
				sess.SetState(roastsession.SignSessionStateCollectingShares)
			} else {
				// 检查是否所有 task 都已完成
				allCompleted := true
				for i := 0; i < len(sess.Messages); i++ {
					if i >= len(signatures) || signatures[i] == nil {
						allCompleted = false
						break
					}
				}

				if allCompleted {
					// 所有 task 都已完成
					logs.Info("[Coordinator] session %s completed (all tasks)", sess.JobID)
					return
				} else {
					// 部分完成：对未完成的 task 继续收集 share
					logs.Info("[Coordinator] session %s partial completion, continuing for remaining tasks", sess.JobID)
					// 继续收集未完成 task 的 share
					sess.SetState(roastsession.SignSessionStateCollectingShares)
				}
			}

		case roastsession.SignSessionStateComplete, roastsession.SignSessionStateFailed:
			return
		}
	}
}

// broadcastNonceRequest 广播 nonce 请求
func (c *Coordinator) broadcastNonceRequest(sess *roastsession.Session) {
	if c.messenger == nil {
		return
	}
	selectedSet := sess.SelectedSetSnapshot()
	peers := make([]NodeID, 0, len(selectedSet))
	for _, idx := range selectedSet {
		if idx >= len(sess.Committee) {
			continue
		}
		peers = append(peers, NodeID(sess.Committee[idx].ID))
	}
	if len(peers) == 0 {
		return
	}

	msg := &Envelope{
		SessionID: sess.JobID,
		Kind:      "NonceRequest",
		From:      c.nodeID,
		Chain:     sess.Chain,
		VaultID:   sess.VaultID,
		SignAlgo:  pb.SignAlgo(sess.SignAlgo),
		Epoch:     sess.KeyEpoch,
		Round:     1,
	}

	_ = c.messenger.Broadcast(peers, toTypesRoastEnvelope(msg))
}

// waitForNonces 等待收集 nonce
func (c *Coordinator) waitForNonces(ctx context.Context, sess *roastsession.Session) bool {
	deadline := time.After(c.config.NonceCollectTimeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline:
			return false
		case <-ticker.C:
			if sess.HasEnoughNonces() {
				return true
			}
		}
	}
}

// broadcastSignRequest 广播签名请求
func (c *Coordinator) broadcastSignRequest(sess *roastsession.Session) {
	if c.messenger == nil {
		return
	}
	selectedSet := sess.SelectedSetSnapshot()

	// 聚合 nonce 承诺
	aggregatedNonces := c.aggregateNonces(sess)
	peers := make([]NodeID, 0, len(selectedSet))
	for _, idx := range selectedSet {
		if idx >= len(sess.Committee) {
			continue
		}
		peers = append(peers, NodeID(sess.Committee[idx].ID))
	}
	if len(peers) == 0 {
		return
	}

	msg := &Envelope{
		SessionID: sess.JobID,
		Kind:      "SignRequest",
		From:      c.nodeID,
		Chain:     sess.Chain,
		VaultID:   sess.VaultID,
		SignAlgo:  pb.SignAlgo(sess.SignAlgo),
		Epoch:     sess.KeyEpoch,
		Round:     2,
		Payload:   aggregatedNonces,
	}

	_ = c.messenger.Broadcast(peers, toTypesRoastEnvelope(msg))
}

// aggregateNonces 聚合 nonce 承诺
func (c *Coordinator) aggregateNonces(sess *roastsession.Session) []byte {
	// Simplified: concatenate all nonce commitments.
	var buf bytes.Buffer
	selectedSet := sess.SelectedSetSnapshot()
	for _, idx := range selectedSet {
		if nonce, ok := sess.GetNonce(idx); ok {
			for i := range nonce.HidingNonces {
				buf.Write(nonce.HidingNonces[i])
				buf.Write(nonce.BindingNonces[i])
			}
		}
	}
	return buf.Bytes()
}

// waitForShares 等待收集签名份额
func (c *Coordinator) waitForShares(ctx context.Context, sess *roastsession.Session) bool {
	deadline := time.After(c.config.ShareCollectTimeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline:
			return false
		case <-ticker.C:
			if sess.HasEnoughShares() {
				return true
			}
		}
	}
}

// aggregateSignatures 聚合签名（支持部分完成）
// 返回：签名列表（已完成的 task 有签名，未完成的为 nil）
func (c *Coordinator) aggregateSignatures(sess *roastsession.Session) ([][]byte, error) {
	signatures, err := c.aggregateSessionSignatures(sess)
	if err != nil {
		return nil, err
	}

	// 检查是否所有 task 都已完成
	allCompleted := true
	for i := 0; i < len(sess.Messages); i++ {
		if i >= len(signatures) || signatures[i] == nil {
			allCompleted = false
			break
		}
	}

	if allCompleted {
		// 所有 task 都已完成，标记会话完成
		sess.MarkCompleted(signatures)
	} else {
		// 部分完成：更新已完成的签名，但保持会话状态为 CollectingShares
		// 注意：这里不调用 MarkCompleted，让会话继续收集未完成的 task
		// 使用 UpdatePartialSignatures 方法（需要在 session 中添加）
		// 简化处理：直接调用 MarkCompleted，但保持状态为 CollectingShares
		// TODO: 添加 UpdatePartialSignatures 方法到 Session
		sess.MarkCompleted(signatures)
		// 重置状态为 CollectingShares，继续收集未完成的 task
		sess.SetState(roastsession.SignSessionStateCollectingShares)
	}

	return signatures, nil
}

func (c *Coordinator) aggregateSessionSignatures(sess *roastsession.Session) ([][]byte, error) {
	if sess == nil {
		return nil, ErrInvalidRoastPayload
	}
	if len(sess.Messages) == 0 {
		return nil, errors.New("no messages to sign")
	}

	// 通过工厂获取执行器
	roastExec, err := c.cryptoFactory.NewROASTExecutor(sess.SignAlgo)
	if err != nil {
		return nil, err
	}

	minSigners := sess.Threshold + 1
	if minSigners <= 0 {
		minSigners = 1
	}

	selectedSet := sess.SelectedSetSnapshot()
	signatures := make([][]byte, len(sess.Messages))
	completedTasks := make(map[int]bool) // 已完成的 task 索引

	// 按 task 级别聚合签名（支持部分完成）
	for taskIdx, msg := range sess.Messages {
		// 检查该 task 是否已有足够的数据
		nonces := make([]NonceInput, 0, len(selectedSet))
		for _, idx := range selectedSet {
			nonceData, ok := sess.GetNonce(idx)
			if !ok || taskIdx >= len(nonceData.HidingNonces) || taskIdx >= len(nonceData.BindingNonces) {
				continue
			}

			hidingBytes := nonceData.HidingNonces[taskIdx]
			bindingBytes := nonceData.BindingNonces[taskIdx]
			nonces = append(nonces, NonceInput{
				SignerID: idx + 1,
				HidingPoint: CurvePoint{
					X: new(big.Int).SetBytes(hidingBytes),
					Y: big.NewInt(0),
				},
				BindingPoint: CurvePoint{
					X: new(big.Int).SetBytes(bindingBytes),
					Y: big.NewInt(0),
				},
			})
		}

		if len(nonces) < minSigners {
			// 该 task 的 nonce 不足，跳过（继续收集）
			logs.Debug("[Coordinator] task %d: insufficient nonces (%d < %d), continue collecting", taskIdx, len(nonces), minSigners)
			continue
		}

		shares := make([]ShareInput, 0, len(selectedSet))
		for _, idx := range selectedSet {
			shareData, ok := sess.GetShare(idx)
			if !ok || taskIdx >= len(shareData.Shares) {
				continue
			}

			shareValue := new(big.Int).SetBytes(shareData.Shares[taskIdx])
			shares = append(shares, ShareInput{
				SignerID: idx + 1,
				Share:    shareValue,
			})
		}

		if len(shares) < minSigners {
			// 该 task 的 share 不足，跳过（继续收集）
			logs.Debug("[Coordinator] task %d: insufficient shares (%d < %d), continue collecting", taskIdx, len(shares), minSigners)
			continue
		}

		// 该 task 有足够的数据，尝试聚合
		R, err := roastExec.ComputeGroupCommitment(nonces, msg)
		if err != nil {
			logs.Warn("[Coordinator] task %d: failed to compute group commitment: %v", taskIdx, err)
			continue
		}

		sig, err := roastExec.AggregateSignatures(R, shares)
		if err != nil {
			logs.Warn("[Coordinator] task %d: failed to aggregate signatures: %v", taskIdx, err)
			continue
		}

		// 该 task 已完成
		signatures[taskIdx] = sig
		completedTasks[taskIdx] = true
		logs.Info("[Coordinator] task %d completed, signature=%x", taskIdx, sig[:8])
	}

	// 检查是否有任何 task 完成
	if len(completedTasks) == 0 {
		return nil, ErrInsufficientShares
	}

	// 返回部分完成的签名（未完成的 task 为 nil）
	// 协调者可对未完成 task 继续向新子集收集 share
	logs.Info("[Coordinator] partial completion: %d/%d tasks completed", len(completedTasks), len(sess.Messages))
	return signatures, nil
}
func (c *Coordinator) retryWithNewSet(sess *roastsession.Session) bool {
	ok := sess.ResetForRetry(c.config.MaxRetries)
	if ok {
		logs.Info("[Coordinator] session %s retry %d", sess.JobID, sess.RetryCount)
	}
	return ok
}
func (c *Coordinator) AddNonce(jobID string, participantIndex int, hidingNonces, bindingNonces [][]byte) error {
	c.mu.RLock()
	sess, exists := c.sessions[jobID]
	c.mu.RUnlock()

	if !exists {
		return ErrSessionNotFound
	}

	return sess.AddNonce(participantIndex, hidingNonces, bindingNonces)
}

// AddShare 添加签名份额
func (c *Coordinator) AddShare(jobID string, participantIndex int, shares [][]byte) error {
	c.mu.RLock()
	sess, exists := c.sessions[jobID]
	c.mu.RUnlock()

	if !exists {
		return ErrSessionNotFound
	}

	return sess.AddShare(participantIndex, shares)
}

// HandleNonceCommit handles nonce commitments from participants.
func (c *Coordinator) HandleNonceCommit(env *FrostEnvelope) error {
	return c.HandleRoastNonceCommit(FromFrostEnvelope(env))
}

// HandleRoastNonceCommit handles nonce commitments from a RoastEnvelope.
func (c *Coordinator) HandleRoastNonceCommit(env *Envelope) error {
	if env == nil {
		return ErrInvalidRoastPayload
	}

	c.mu.RLock()
	sess, exists := c.sessions[env.SessionID]
	c.mu.RUnlock()

	if !exists {
		return ErrSessionNotFound
	}

	participantIndex, ok := participantIndexFor(sess, env.From)
	if !ok {
		return ErrParticipantNotFound
	}

	hiding, binding, err := splitNonceCommitPayload(env.Payload, env.SignAlgo)
	if err != nil {
		return err
	}

	return sess.AddNonce(participantIndex, hiding, binding)
}

// HandleSigShare handles signature shares from participants.
func (c *Coordinator) HandleSigShare(env *FrostEnvelope) error {
	return c.HandleRoastSigShare(FromFrostEnvelope(env))
}

// HandleRoastSigShare handles signature shares from a RoastEnvelope.
func (c *Coordinator) HandleRoastSigShare(env *Envelope) error {
	if env == nil {
		return ErrInvalidRoastPayload
	}

	c.mu.RLock()
	sess, exists := c.sessions[env.SessionID]
	c.mu.RUnlock()

	if !exists {
		return ErrSessionNotFound
	}

	participantIndex, ok := participantIndexFor(sess, env.From)
	if !ok {
		return ErrParticipantNotFound
	}

	shares, err := splitSignatureShares(env.Payload)
	if err != nil {
		return err
	}

	return sess.AddShare(participantIndex, shares)
}

// GetSession 获取会话
func (c *Coordinator) GetSession(jobID string) *roastsession.Session {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessions[jobID]
}

// GetFinalSignatures 获取最终签名
func (c *Coordinator) GetFinalSignatures(jobID string) ([][]byte, error) {
	c.mu.RLock()
	sess, exists := c.sessions[jobID]
	c.mu.RUnlock()

	if !exists {
		return nil, ErrSessionNotFound
	}

	if sess.GetState() != roastsession.SignSessionStateComplete {
		return nil, errors.New("session not complete")
	}

	return sess.GetFinalSignatures(), nil
}

// CloseSession 关闭会话
func (c *Coordinator) CloseSession(jobID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sessions, jobID)
}

func participantIndexFor(sess *roastsession.Session, nodeID NodeID) (int, bool) {
	if sess == nil {
		return -1, false
	}
	for i, member := range sess.Committee {
		if member.ID == string(nodeID) {
			return i, true
		}
	}
	return -1, false
}

func splitNonceCommitPayload(payload []byte, signAlgo pb.SignAlgo) ([][]byte, [][]byte, error) {
	pointSize := getPointSize(signAlgo)
	chunks, err := splitFixedPayload(payload, 2*pointSize)
	if err != nil {
		return nil, nil, err
	}

	hiding := make([][]byte, len(chunks))
	binding := make([][]byte, len(chunks))
	for i, chunk := range chunks {
		hiding[i] = chunk[:pointSize]
		binding[i] = chunk[pointSize:]
	}
	return hiding, binding, nil
}

func splitSignatureShares(payload []byte) ([][]byte, error) {
	return splitFixedPayload(payload, 32)
}

func splitFixedPayload(payload []byte, chunkSize int) ([][]byte, error) {
	if chunkSize <= 0 || len(payload) == 0 || len(payload)%chunkSize != 0 {
		return nil, ErrInvalidRoastPayload
	}
	count := len(payload) / chunkSize
	result := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		start := i * chunkSize
		chunk := make([]byte, chunkSize)
		copy(chunk, payload[start:start+chunkSize])
		result = append(result, chunk)
	}
	return result, nil
}
