// frost/runtime/coordinator.go
// ROAST Coordinator: 协调签名会话，收集 nonce 承诺和签名份额

package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"time"

	"dex/frost/runtime/session"
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
	sessions map[string]*CoordinatorSession // jobID -> session

	// 依赖
	p2p           P2P
	vaultProvider VaultCommitteeProvider

	// 当前区块高度（用于协调者选举）
	currentHeight uint64
}

// CoordinatorSession 协调者管理的签名会话
type CoordinatorSession struct {
	mu sync.RWMutex

	// 会话标识
	JobID    string
	VaultID  uint32
	Chain    string
	KeyEpoch uint64
	SignAlgo pb.SignAlgo

	// 待签名的消息（可能多个，如 BTC 多 input）
	Messages [][]byte // 每个 task 的消息哈希

	// 参与者信息
	Committee []SignerInfo // 委员会成员
	Threshold int          // 门限 t
	MyIndex   int          // 本节点在委员会中的索引（-1 表示不在）

	// 当前选中的参与者子集
	SelectedSet []int // 索引（指向 Committee）

	// 收集的 nonce 承诺
	Nonces map[int]*NonceData // 参与者索引 -> nonce

	// 收集的签名份额
	Shares map[int]*ShareData // 参与者索引 -> share

	// 状态
	State       CoordinatorSessionState
	RetryCount  int
	StartHeight uint64 // 会话开始高度（用于协调者选举）
	StartedAt   time.Time
	CompletedAt time.Time

	// 结果
	FinalSignatures [][]byte // 每个 task 的最终签名
}

// CoordinatorSessionState 协调者会话状态
type CoordinatorSessionState int

const (
	CoordStateInit CoordinatorSessionState = iota
	CoordStateCollectingNonces
	CoordStateCollectingShares
	CoordStateAggregating
	CoordStateComplete
	CoordStateFailed
)

func (s CoordinatorSessionState) String() string {
	switch s {
	case CoordStateInit:
		return "INIT"
	case CoordStateCollectingNonces:
		return "COLLECTING_NONCES"
	case CoordStateCollectingShares:
		return "COLLECTING_SHARES"
	case CoordStateAggregating:
		return "AGGREGATING"
	case CoordStateComplete:
		return "COMPLETE"
	case CoordStateFailed:
		return "FAILED"
	default:
		return "UNKNOWN"
	}
}

// NonceData nonce 承诺数据
type NonceData struct {
	ParticipantIndex int
	HidingNonces     [][]byte // 每个 task 一个 hiding nonce (32 bytes)
	BindingNonces    [][]byte // 每个 task 一个 binding nonce (32 bytes)
	ReceivedAt       time.Time
}

// ShareData 签名份额数据
type ShareData struct {
	ParticipantIndex int
	Shares           [][]byte // 每个 task 一个 share (32 bytes)
	ReceivedAt       time.Time
}

// NewCoordinator 创建协调者
func NewCoordinator(nodeID NodeID, p2p P2P, vaultProvider VaultCommitteeProvider, config *CoordinatorConfig) *Coordinator {
	if config == nil {
		config = DefaultCoordinatorConfig()
	}
	return &Coordinator{
		config:        config,
		nodeID:        nodeID,
		sessions:      make(map[string]*CoordinatorSession),
		p2p:           p2p,
		vaultProvider: vaultProvider,
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
	committee, err := c.vaultProvider.VaultCommittee(params.Chain, params.VaultID, params.KeyEpoch)
	if err != nil {
		return err
	}

	if len(committee) < params.Threshold {
		return ErrInsufficientParticipants
	}

	// 查找本节点在委员会中的索引
	myIndex := -1
	for i, member := range committee {
		if member.ID == c.nodeID {
			myIndex = i
			break
		}
	}

	// 创建会话
	sess := &CoordinatorSession{
		JobID:       params.JobID,
		VaultID:     params.VaultID,
		Chain:       params.Chain,
		KeyEpoch:    params.KeyEpoch,
		SignAlgo:    params.SignAlgo,
		Messages:    params.Messages,
		Committee:   committee,
		Threshold:   params.Threshold,
		MyIndex:     myIndex,
		Nonces:      make(map[int]*NonceData),
		Shares:      make(map[int]*ShareData),
		State:       CoordStateInit,
		StartHeight: c.currentHeight,
		StartedAt:   time.Now(),
	}

	c.sessions[params.JobID] = sess

	// 如果本节点是协调者，开始收集 nonce
	if c.isCurrentCoordinator(sess) {
		sess.selectInitialSet()
		sess.State = CoordStateCollectingNonces
		go c.runCoordinatorLoop(ctx, sess)
	}

	return nil
}

// StartSessionParams 启动会话参数
type StartSessionParams struct {
	JobID     string
	VaultID   uint32
	Chain     string
	KeyEpoch  uint64
	SignAlgo  pb.SignAlgo
	Messages  [][]byte // 待签名消息列表
	Threshold int
}

// isCurrentCoordinator 检查本节点是否是当前协调者
func (c *Coordinator) isCurrentCoordinator(sess *CoordinatorSession) bool {
	if len(sess.Committee) == 0 {
		return false
	}

	// 计算当前协调者索引
	coordIndex := c.computeCoordinatorIndex(sess)
	return sess.MyIndex == coordIndex
}

// computeCoordinatorIndex 计算当前协调者索引
// 确定性算法：基于 session_id 和区块高度
func (c *Coordinator) computeCoordinatorIndex(sess *CoordinatorSession) int {
	if len(sess.Committee) == 0 {
		return 0
	}

	// 计算种子
	seed := computeAggregatorSeed(sess.JobID, sess.KeyEpoch)

	// 计算轮换次数
	rotations := (c.currentHeight - sess.StartHeight) / c.config.AggregatorRotateBlocks

	// 确定性排列委员会
	permuted := permuteCommittee(sess.Committee, seed)

	// 选择协调者
	coordIndex := int(rotations) % len(permuted)

	// 找回原始索引
	for i, member := range sess.Committee {
		if member.ID == permuted[coordIndex].ID {
			return i
		}
	}
	return 0
}

// computeAggregatorSeed 计算聚合者选举种子
func computeAggregatorSeed(jobID string, keyEpoch uint64) []byte {
	data := jobID + "|" + string(rune(keyEpoch)) + "|frost_agg"
	hash := sha256.Sum256([]byte(data))
	return hash[:]
}

// permuteCommittee 确定性排列委员会
func permuteCommittee(committee []SignerInfo, seed []byte) []SignerInfo {
	result := make([]SignerInfo, len(committee))
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

// selectInitialSet 选择初始参与者集合
func (s *CoordinatorSession) selectInitialSet() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 选择前 t+1 个参与者
	minSigners := s.Threshold + 1
	s.SelectedSet = make([]int, 0, minSigners)
	for i := 0; i < len(s.Committee) && len(s.SelectedSet) < minSigners; i++ {
		s.SelectedSet = append(s.SelectedSet, i)
	}
}

// runCoordinatorLoop 协调者主循环
func (c *Coordinator) runCoordinatorLoop(ctx context.Context, sess *CoordinatorSession) {
	logs.Info("[Coordinator] starting session %s", sess.JobID)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		sess.mu.RLock()
		state := sess.State
		sess.mu.RUnlock()

		switch state {
		case CoordStateCollectingNonces:
			// 广播 nonce 请求
			c.broadcastNonceRequest(sess)

			// 等待收集或超时
			if c.waitForNonces(ctx, sess) {
				sess.mu.Lock()
				sess.State = CoordStateCollectingShares
				sess.mu.Unlock()
			} else {
				// 超时，尝试重试
				if !c.retryWithNewSet(sess) {
					sess.mu.Lock()
					sess.State = CoordStateFailed
					sess.mu.Unlock()
					return
				}
			}

		case CoordStateCollectingShares:
			// 广播签名请求（包含聚合的 nonce）
			c.broadcastSignRequest(sess)

			// 等待收集或超时
			if c.waitForShares(ctx, sess) {
				sess.mu.Lock()
				sess.State = CoordStateAggregating
				sess.mu.Unlock()
			} else {
				// 超时，尝试重试
				if !c.retryWithNewSet(sess) {
					sess.mu.Lock()
					sess.State = CoordStateFailed
					sess.mu.Unlock()
					return
				}
			}

		case CoordStateAggregating:
			// 聚合签名
			if err := c.aggregateSignatures(sess); err != nil {
				logs.Error("[Coordinator] aggregate failed: %v", err)
				if !c.retryWithNewSet(sess) {
					sess.mu.Lock()
					sess.State = CoordStateFailed
					sess.mu.Unlock()
					return
				}
			} else {
				sess.mu.Lock()
				sess.State = CoordStateComplete
				sess.CompletedAt = time.Now()
				sess.mu.Unlock()
				logs.Info("[Coordinator] session %s completed", sess.JobID)
				return
			}

		case CoordStateComplete, CoordStateFailed:
			return
		}
	}
}

// broadcastNonceRequest 广播 nonce 请求
func (c *Coordinator) broadcastNonceRequest(sess *CoordinatorSession) {
	sess.mu.RLock()
	selectedSet := sess.SelectedSet
	sess.mu.RUnlock()

	for _, idx := range selectedSet {
		if idx >= len(sess.Committee) {
			continue
		}
		member := sess.Committee[idx]

		env := &FrostEnvelope{
			SessionID: sess.JobID,
			Kind:      "NonceRequest",
			From:      c.nodeID,
			Chain:     sess.Chain,
			VaultID:   sess.VaultID,
			SignAlgo:  int32(sess.SignAlgo),
			Epoch:     sess.KeyEpoch,
			Round:     1,
		}

		if c.p2p != nil {
			_ = c.p2p.Send(member.ID, env)
		}
	}
}

// waitForNonces 等待收集 nonce
func (c *Coordinator) waitForNonces(ctx context.Context, sess *CoordinatorSession) bool {
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
			if c.hasEnoughNonces(sess) {
				return true
			}
		}
	}
}

// hasEnoughNonces 检查是否收集够 nonce
func (c *Coordinator) hasEnoughNonces(sess *CoordinatorSession) bool {
	sess.mu.RLock()
	defer sess.mu.RUnlock()

	count := 0
	for _, idx := range sess.SelectedSet {
		if _, ok := sess.Nonces[idx]; ok {
			count++
		}
	}
	return count >= sess.Threshold+1
}

// broadcastSignRequest 广播签名请求
func (c *Coordinator) broadcastSignRequest(sess *CoordinatorSession) {
	sess.mu.RLock()
	selectedSet := sess.SelectedSet
	sess.mu.RUnlock()

	// 聚合 nonce 承诺
	aggregatedNonces := c.aggregateNonces(sess)

	for _, idx := range selectedSet {
		if idx >= len(sess.Committee) {
			continue
		}
		member := sess.Committee[idx]

		env := &FrostEnvelope{
			SessionID: sess.JobID,
			Kind:      "SignRequest",
			From:      c.nodeID,
			Chain:     sess.Chain,
			VaultID:   sess.VaultID,
			SignAlgo:  int32(sess.SignAlgo),
			Epoch:     sess.KeyEpoch,
			Round:     2,
			Payload:   aggregatedNonces,
		}

		if c.p2p != nil {
			_ = c.p2p.Send(member.ID, env)
		}
	}
}

// aggregateNonces 聚合 nonce 承诺
func (c *Coordinator) aggregateNonces(sess *CoordinatorSession) []byte {
	sess.mu.RLock()
	defer sess.mu.RUnlock()

	// 简化实现：将所有 nonce 序列化
	var buf bytes.Buffer
	for _, idx := range sess.SelectedSet {
		if nonce, ok := sess.Nonces[idx]; ok {
			for i := range nonce.HidingNonces {
				buf.Write(nonce.HidingNonces[i])
				buf.Write(nonce.BindingNonces[i])
			}
		}
	}
	return buf.Bytes()
}

// waitForShares 等待收集签名份额
func (c *Coordinator) waitForShares(ctx context.Context, sess *CoordinatorSession) bool {
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
			if c.hasEnoughShares(sess) {
				return true
			}
		}
	}
}

// hasEnoughShares 检查是否收集够签名份额
func (c *Coordinator) hasEnoughShares(sess *CoordinatorSession) bool {
	sess.mu.RLock()
	defer sess.mu.RUnlock()

	count := 0
	for _, idx := range sess.SelectedSet {
		if _, ok := sess.Shares[idx]; ok {
			count++
		}
	}
	return count >= sess.Threshold+1
}

// aggregateSignatures 聚合签名
func (c *Coordinator) aggregateSignatures(sess *CoordinatorSession) error {
	sess.mu.Lock()
	defer sess.mu.Unlock()

	// TODO: 实现真正的 Schnorr 签名聚合
	// 这里简化为生成 dummy 签名
	numTasks := len(sess.Messages)
	sess.FinalSignatures = make([][]byte, numTasks)
	for i := range sess.Messages {
		// 生成 64 字节的 dummy 签名
		sig := make([]byte, 64)
		copy(sig, []byte("aggregated_sig_"+sess.JobID))
		sess.FinalSignatures[i] = sig
	}

	return nil
}

// retryWithNewSet 使用新的参与者子集重试
func (c *Coordinator) retryWithNewSet(sess *CoordinatorSession) bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()

	sess.RetryCount++
	if sess.RetryCount > c.config.MaxRetries {
		return false
	}

	// 选择新的子集
	sess.selectAlternativeSetLocked()

	// 清空之前收集的数据
	sess.Nonces = make(map[int]*NonceData)
	sess.Shares = make(map[int]*ShareData)
	sess.State = CoordStateCollectingNonces

	logs.Info("[Coordinator] session %s retry %d", sess.JobID, sess.RetryCount)
	return true
}

// selectAlternativeSetLocked 选择替代参与者集合（需要持有锁）
func (s *CoordinatorSession) selectAlternativeSetLocked() {
	minSigners := s.Threshold + 1
	offset := s.RetryCount % len(s.Committee)
	s.SelectedSet = make([]int, 0, minSigners)

	for i := 0; i < len(s.Committee) && len(s.SelectedSet) < minSigners; i++ {
		idx := (i + offset) % len(s.Committee)
		s.SelectedSet = append(s.SelectedSet, idx)
	}
}

// AddNonce 添加 nonce 承诺
func (c *Coordinator) AddNonce(jobID string, participantIndex int, hidingNonces, bindingNonces [][]byte) error {
	c.mu.RLock()
	sess, exists := c.sessions[jobID]
	c.mu.RUnlock()

	if !exists {
		return ErrSessionNotFound
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()

	if sess.State != CoordStateCollectingNonces {
		return session.ErrInvalidState
	}

	sess.Nonces[participantIndex] = &NonceData{
		ParticipantIndex: participantIndex,
		HidingNonces:     hidingNonces,
		BindingNonces:    bindingNonces,
		ReceivedAt:       time.Now(),
	}

	return nil
}

// AddShare 添加签名份额
func (c *Coordinator) AddShare(jobID string, participantIndex int, shares [][]byte) error {
	c.mu.RLock()
	sess, exists := c.sessions[jobID]
	c.mu.RUnlock()

	if !exists {
		return ErrSessionNotFound
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()

	if sess.State != CoordStateCollectingShares {
		return session.ErrInvalidState
	}

	sess.Shares[participantIndex] = &ShareData{
		ParticipantIndex: participantIndex,
		Shares:           shares,
		ReceivedAt:       time.Now(),
	}

	return nil
}

// GetSession 获取会话
func (c *Coordinator) GetSession(jobID string) *CoordinatorSession {
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

	sess.mu.RLock()
	defer sess.mu.RUnlock()

	if sess.State != CoordStateComplete {
		return nil, errors.New("session not complete")
	}

	return sess.FinalSignatures, nil
}

// CloseSession 关闭会话
func (c *Coordinator) CloseSession(jobID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sessions, jobID)
}
