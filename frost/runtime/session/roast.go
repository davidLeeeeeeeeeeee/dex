// frost/runtime/session/roast.go
// ROAST 签名会话实现

package session

import (
	"dex/frost/core/curve"
	"dex/frost/core/roast"
	"errors"
	"math/big"
	"sync"
	"time"
)

// ========== 错误定义 ==========

var (
	// ErrSessionClosed 会话已关闭
	ErrSessionClosed = errors.New("session closed")
	// ErrNotAggregator 不是聚合者
	ErrNotAggregator = errors.New("not aggregator")
	// ErrInvalidState 无效状态
	ErrInvalidState = errors.New("invalid state")
	// ErrMaxRetriesExceeded 超过最大重试次数
	ErrMaxRetriesExceeded = errors.New("max retries exceeded")
)

// ========== ROAST 会话 ==========

// ROASTSession ROAST 签名会话
type ROASTSession struct {
	mu sync.RWMutex

	// 会话标识
	JobID    string
	KeyEpoch uint64
	Message  []byte // 待签名消息

	// 状态
	State       SignSessionState
	RetryCount  int
	StartedAt   time.Time
	CompletedAt time.Time

	// 参与者
	Participants []Participant
	MyIndex      uint16 // 本节点索引

	// 收集的数据
	Nonces map[uint16]*NonceCommitment // 收到的 nonce 承诺
	Shares map[uint16]*SignatureShare  // 收到的签名份额

	// 当前轮次选中的参与者
	SelectedSet []uint16

	// 配置
	Config *SignSessionConfig

	// 结果
	FinalSignature []byte

	// 回调
	OnComplete func(sig []byte)
	OnFailed   func(err error)

	// 内部
	closed bool
}

// NewROASTSession 创建新的 ROAST 会话
func NewROASTSession(jobID string, keyEpoch uint64, msg []byte, participants []Participant, myIndex uint16, config *SignSessionConfig) *ROASTSession {
	if config == nil {
		config = DefaultSignSessionConfig()
	}

	return &ROASTSession{
		JobID:        jobID,
		KeyEpoch:     keyEpoch,
		Message:      msg,
		State:        SignSessionStateInit,
		Participants: participants,
		MyIndex:      myIndex,
		Nonces:       make(map[uint16]*NonceCommitment),
		Shares:       make(map[uint16]*SignatureShare),
		Config:       config,
		StartedAt:    time.Now(),
	}
}

// IsAggregator 检查本节点是否是聚合者
func (s *ROASTSession) IsAggregator() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.MyIndex == s.Config.AggregatorIndex
}

// Start 启动会话（聚合者调用）
func (s *ROASTSession) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrSessionClosed
	}

	if s.MyIndex != s.Config.AggregatorIndex {
		return ErrNotAggregator
	}

	if s.State != SignSessionStateInit {
		return ErrInvalidState
	}

	// 选择初始参与者集合
	s.selectInitialSet()
	s.State = SignSessionStateCollectingNonces

	return nil
}

// selectInitialSet 选择初始参与者集合
func (s *ROASTSession) selectInitialSet() {
	// 选择前 t+1 个参与者
	s.SelectedSet = make([]uint16, 0, s.Config.MinSigners)
	for i := 0; i < len(s.Participants) && len(s.SelectedSet) < s.Config.MinSigners; i++ {
		s.SelectedSet = append(s.SelectedSet, s.Participants[i].Index)
	}
}

// AddNonce 添加 nonce 承诺
func (s *ROASTSession) AddNonce(participantIndex uint16, hiding, binding []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrSessionClosed
	}

	if s.State != SignSessionStateCollectingNonces {
		return ErrInvalidState
	}

	s.Nonces[participantIndex] = &NonceCommitment{
		ParticipantIndex: participantIndex,
		HidingNonce:      hiding,
		BindingNonce:     binding,
		ReceivedAt:       time.Now(),
	}

	// 检查是否收集够了
	if s.hasEnoughNonces() {
		s.State = SignSessionStateCollectingShares
	}

	return nil
}

// hasEnoughNonces 检查是否收集够 nonce
func (s *ROASTSession) hasEnoughNonces() bool {
	count := 0
	for _, idx := range s.SelectedSet {
		if _, ok := s.Nonces[idx]; ok {
			count++
		}
	}
	return count >= s.Config.MinSigners
}

// AddShare 添加签名份额
func (s *ROASTSession) AddShare(participantIndex uint16, share []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrSessionClosed
	}

	if s.State != SignSessionStateCollectingShares {
		return ErrInvalidState
	}

	s.Shares[participantIndex] = &SignatureShare{
		ParticipantIndex: participantIndex,
		Share:            share,
		ReceivedAt:       time.Now(),
	}

	// 检查是否收集够了
	if s.hasEnoughShares() {
		// 聚合签名
		if err := s.aggregateSignature(); err != nil {
			s.State = SignSessionStateFailed
			if s.OnFailed != nil {
				s.OnFailed(err)
			}
			return err
		}
		s.State = SignSessionStateComplete
		s.CompletedAt = time.Now()
		if s.OnComplete != nil {
			s.OnComplete(s.FinalSignature)
		}
	}

	return nil
}

// hasEnoughShares 检查是否收集够签名份额
func (s *ROASTSession) hasEnoughShares() bool {
	count := 0
	for _, idx := range s.SelectedSet {
		if _, ok := s.Shares[idx]; ok {
			count++
		}
	}
	return count >= s.Config.MinSigners
}

// Retry 重试（选择不同子集）
func (s *ROASTSession) Retry() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrSessionClosed
	}

	s.RetryCount++
	if s.RetryCount > s.Config.MaxRetries {
		s.State = SignSessionStateFailed
		return ErrMaxRetriesExceeded
	}

	// 选择不同的参与者子集
	s.selectAlternativeSet()
	s.State = SignSessionStateCollectingNonces

	return nil
}

// selectAlternativeSet 选择替代参与者集合
func (s *ROASTSession) selectAlternativeSet() {
	// 简单策略：轮换选择
	offset := s.RetryCount % len(s.Participants)
	s.SelectedSet = make([]uint16, 0, s.Config.MinSigners)

	for i := 0; i < len(s.Participants) && len(s.SelectedSet) < s.Config.MinSigners; i++ {
		idx := (i + offset) % len(s.Participants)
		s.SelectedSet = append(s.SelectedSet, s.Participants[idx].Index)
	}
}

// SwitchAggregator 切换聚合者
func (s *ROASTSession) SwitchAggregator() uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 轮换到下一个聚合者
	nextIdx := (int(s.Config.AggregatorIndex) % len(s.Participants)) + 1
	s.Config.AggregatorIndex = uint16(nextIdx)

	return s.Config.AggregatorIndex
}

// GetState 获取当前状态
func (s *ROASTSession) GetState() SignSessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.State
}

// GetSelectedSet 获取当前选中的参与者
func (s *ROASTSession) GetSelectedSet() []uint16 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]uint16, len(s.SelectedSet))
	copy(result, s.SelectedSet)
	return result
}

// Close 关闭会话
func (s *ROASTSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

// aggregateSignature 聚合签名份额
func (s *ROASTSession) aggregateSignature() error {
	group := curve.NewSecp256k1Group()

	// 计算群组承诺 R = Σ (D_i + ρ_i * E_i)
	R := s.computeGroupCommitment(group)

	// 收集签名份额
	var signerShares []roast.SignerShare
	for _, idx := range s.SelectedSet {
		share, ok := s.Shares[idx]
		if !ok {
			continue
		}
		shareInt := new(big.Int).SetBytes(share.Share)
		signerShares = append(signerShares, roast.SignerShare{
			SignerID: int(idx),
			Share:    shareInt,
		})
	}

	// 聚合签名
	sig, err := roast.AggregateSignatures(R, signerShares, group)
	if err != nil {
		return err
	}

	s.FinalSignature = sig
	return nil
}

// computeGroupCommitment 计算群组承诺 R
func (s *ROASTSession) computeGroupCommitment(group curve.Group) curve.Point {
	// R = Σ (D_i + ρ_i * E_i)
	// 其中 D_i = hiding nonce point, E_i = binding nonce point
	// ρ_i = binding coefficient

	var R curve.Point
	first := true

	for _, idx := range s.SelectedSet {
		nonce, ok := s.Nonces[idx]
		if !ok {
			continue
		}

		// 解析 nonce 点
		D := parsePoint(nonce.HidingNonce, group)
		E := parsePoint(nonce.BindingNonce, group)

		// 计算 binding coefficient
		rho := roast.ComputeBindingCoefficient(int(idx), s.Message, s.collectSignerNonces(), group)

		// D_i + ρ_i * E_i
		rhoE := group.ScalarMult(E, rho)
		contribution := group.Add(D, rhoE)

		if first {
			R = contribution
			first = false
		} else {
			R = group.Add(R, contribution)
		}
	}

	return R
}

// collectSignerNonces 收集签名者 nonce 用于计算 binding coefficient
func (s *ROASTSession) collectSignerNonces() []roast.SignerNonce {
	var nonces []roast.SignerNonce
	for _, idx := range s.SelectedSet {
		nonce, ok := s.Nonces[idx]
		if !ok {
			continue
		}
		nonces = append(nonces, roast.SignerNonce{
			SignerID:     int(idx),
			HidingPoint:  parsePoint(nonce.HidingNonce, s.getGroup()),
			BindingPoint: parsePoint(nonce.BindingNonce, s.getGroup()),
		})
	}
	return nonces
}

// getGroup 获取椭圆曲线群
func (s *ROASTSession) getGroup() curve.Group {
	return curve.NewSecp256k1Group()
}

// parsePoint 解析压缩格式的点
func parsePoint(data []byte, group curve.Group) curve.Point {
	if len(data) == 33 {
		// 压缩格式
		return group.DecompressPoint(data)
	} else if len(data) == 65 {
		// 非压缩格式
		x := new(big.Int).SetBytes(data[1:33])
		y := new(big.Int).SetBytes(data[33:65])
		return curve.Point{X: x, Y: y}
	}
	// 假设是 32 字节的标量，转换为点
	scalar := new(big.Int).SetBytes(data)
	return group.ScalarBaseMult(scalar)
}
