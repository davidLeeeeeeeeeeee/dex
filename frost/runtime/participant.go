// frost/runtime/participant.go
// FROST Participant: 签名参与者，响应协调者请求，生成 nonce 和签名份额

package runtime

import (
	"crypto/rand"
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
	// ErrParticipantNotInCommittee 不在委员会中
	ErrParticipantNotInCommittee = errors.New("participant not in committee")
	// ErrInvalidNonceRequest 无效的 nonce 请求
	ErrInvalidNonceRequest = errors.New("invalid nonce request")
	// ErrInvalidSignRequest 无效的签名请求
	ErrInvalidSignRequest = errors.New("invalid sign request")
	// ErrNonceNotFound nonce 不存在
	ErrNonceNotFound = errors.New("nonce not found")
)

// ========== 配置 ==========

// ParticipantConfig 参与者配置
type ParticipantConfig struct {
	// 本地密钥份额存储路径
	ShareStorePath string
}

// DefaultParticipantConfig 默认配置
func DefaultParticipantConfig() *ParticipantConfig {
	return &ParticipantConfig{
		ShareStorePath: "./frost_shares",
	}
}

// ========== Participant ==========

// Participant 签名参与者
// 响应协调者请求，生成 nonce 承诺和签名份额
type Participant struct {
	mu sync.RWMutex

	// 配置
	config *ParticipantConfig
	nodeID NodeID

	// 本地状态
	sessions map[string]*ParticipantSession // jobID -> session

	// 依赖
	p2p           P2P
	vaultProvider VaultCommitteeProvider
	sessionStore  *session.SessionStore

	// 本地密钥份额（按 vault 和 epoch 存储）
	shares map[string][]byte // key: "chain_vaultID_epoch" -> share bytes
}

// ParticipantSession 参与者会话
type ParticipantSession struct {
	mu sync.RWMutex

	// 会话标识
	JobID    string
	VaultID  uint32
	Chain    string
	KeyEpoch uint64
	SignAlgo pb.SignAlgo

	// 待签名消息
	Messages [][]byte

	// 本节点信息
	MyIndex int    // 在委员会中的索引
	MyShare []byte // 本地密钥份额

	// 生成的 nonce（每个 task 一对）
	HidingNonces  [][]byte // k_i (hiding nonce 标量)
	BindingNonces [][]byte // k_i' (binding nonce 标量)

	// nonce 承诺点（发送给协调者的）
	HidingPoints  [][]byte // R_i = k_i * G
	BindingPoints [][]byte // R_i' = k_i' * G

	// 收到的聚合 nonce
	AggregatedNonces []byte

	// 状态
	State       ParticipantSessionState
	CreatedAt   time.Time
	CompletedAt time.Time
}

// ParticipantSessionState 参与者会话状态
type ParticipantSessionState int

const (
	ParticipantStateInit ParticipantSessionState = iota
	ParticipantStateNonceGenerated
	ParticipantStateShareGenerated
	ParticipantStateComplete
	ParticipantStateFailed
)

func (s ParticipantSessionState) String() string {
	switch s {
	case ParticipantStateInit:
		return "INIT"
	case ParticipantStateNonceGenerated:
		return "NONCE_GENERATED"
	case ParticipantStateShareGenerated:
		return "SHARE_GENERATED"
	case ParticipantStateComplete:
		return "COMPLETE"
	case ParticipantStateFailed:
		return "FAILED"
	default:
		return "UNKNOWN"
	}
}

// NewParticipant 创建参与者
func NewParticipant(nodeID NodeID, p2p P2P, vaultProvider VaultCommitteeProvider, sessionStore *session.SessionStore, config *ParticipantConfig) *Participant {
	if config == nil {
		config = DefaultParticipantConfig()
	}
	if sessionStore == nil {
		sessionStore = session.NewSessionStore(nil)
	}
	return &Participant{
		config:        config,
		nodeID:        nodeID,
		sessions:      make(map[string]*ParticipantSession),
		p2p:           p2p,
		vaultProvider: vaultProvider,
		sessionStore:  sessionStore,
		shares:        make(map[string][]byte),
	}
}

// SetShare 设置本地密钥份额
func (p *Participant) SetShare(chain string, vaultID uint32, epoch uint64, share []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := shareKey(chain, vaultID, epoch)
	p.shares[key] = share
}

// GetShare 获取本地密钥份额
func (p *Participant) GetShare(chain string, vaultID uint32, epoch uint64) []byte {
	p.mu.RLock()
	defer p.mu.RUnlock()
	key := shareKey(chain, vaultID, epoch)
	return p.shares[key]
}

func shareKey(chain string, vaultID uint32, epoch uint64) string {
	return chain + "_" + string(rune(vaultID)) + "_" + string(rune(epoch))
}

// HandleNonceRequest 处理 nonce 请求
func (p *Participant) HandleNonceRequest(env *FrostEnvelope) error {
	logs.Debug("[Participant] received nonce request from %s for job %s", env.From, env.SessionID)

	// 检查是否在委员会中
	committee, err := p.vaultProvider.VaultCommittee(env.Chain, env.VaultID, env.Epoch)
	if err != nil {
		return err
	}

	myIndex := -1
	for i, member := range committee {
		if member.ID == p.nodeID {
			myIndex = i
			break
		}
	}
	if myIndex < 0 {
		return ErrParticipantNotInCommittee
	}

	// 获取本地密钥份额
	myShare := p.GetShare(env.Chain, env.VaultID, env.Epoch)
	if myShare == nil {
		return errors.New("local share not found")
	}

	// 创建或获取会话
	sess := p.getOrCreateSession(env.SessionID, env.Chain, env.VaultID, env.Epoch, pb.SignAlgo(env.SignAlgo), myIndex, myShare)

	// 生成 nonce
	numTasks := 1 // 默认单任务，实际应从请求中获取
	if err := p.generateNonces(sess, numTasks); err != nil {
		return err
	}

	// 发送 nonce 承诺给协调者
	return p.sendNonceCommitment(sess, NodeID(env.From))
}

// getOrCreateSession 获取或创建会话
func (p *Participant) getOrCreateSession(jobID, chain string, vaultID uint32, epoch uint64, signAlgo pb.SignAlgo, myIndex int, myShare []byte) *ParticipantSession {
	p.mu.Lock()
	defer p.mu.Unlock()

	if sess, exists := p.sessions[jobID]; exists {
		return sess
	}

	sess := &ParticipantSession{
		JobID:     jobID,
		Chain:     chain,
		VaultID:   vaultID,
		KeyEpoch:  epoch,
		SignAlgo:  signAlgo,
		MyIndex:   myIndex,
		MyShare:   myShare,
		State:     ParticipantStateInit,
		CreatedAt: time.Now(),
	}

	p.sessions[jobID] = sess
	return sess
}

// generateNonces 生成 nonce 对
func (p *Participant) generateNonces(sess *ParticipantSession, numTasks int) error {
	sess.mu.Lock()
	defer sess.mu.Unlock()

	if sess.State != ParticipantStateInit {
		return nil // 已生成
	}

	sess.HidingNonces = make([][]byte, numTasks)
	sess.BindingNonces = make([][]byte, numTasks)
	sess.HidingPoints = make([][]byte, numTasks)
	sess.BindingPoints = make([][]byte, numTasks)

	for i := 0; i < numTasks; i++ {
		// 生成随机 nonce
		hiding := make([]byte, 32)
		binding := make([]byte, 32)
		if _, err := rand.Read(hiding); err != nil {
			return err
		}
		if _, err := rand.Read(binding); err != nil {
			return err
		}

		sess.HidingNonces[i] = hiding
		sess.BindingNonces[i] = binding

		// 计算承诺点 (简化实现：实际应该是椭圆曲线点)
		// R_i = k_i * G
		hidingPoint := sha256.Sum256(hiding)
		bindingPoint := sha256.Sum256(binding)
		sess.HidingPoints[i] = hidingPoint[:]
		sess.BindingPoints[i] = bindingPoint[:]

		// 绑定 nonce 到会话
		msg := []byte(sess.JobID)
		if err := p.sessionStore.BindNonce(hiding, msg, sess.KeyEpoch); err != nil {
			return err
		}
	}

	sess.State = ParticipantStateNonceGenerated
	return nil
}

// sendNonceCommitment 发送 nonce 承诺
func (p *Participant) sendNonceCommitment(sess *ParticipantSession, coordinator NodeID) error {
	sess.mu.RLock()
	defer sess.mu.RUnlock()

	env := &FrostEnvelope{
		SessionID: sess.JobID,
		Kind:      "NonceCommit",
		From:      p.nodeID,
		Chain:     sess.Chain,
		VaultID:   sess.VaultID,
		SignAlgo:  int32(sess.SignAlgo),
		Epoch:     sess.KeyEpoch,
		Round:     1,
		Payload:   p.serializeNonces(sess),
	}

	if p.p2p != nil {
		return p.p2p.Send(coordinator, env)
	}
	return nil
}

// serializeNonces 序列化 nonce 承诺
func (p *Participant) serializeNonces(sess *ParticipantSession) []byte {
	var result []byte
	for i := range sess.HidingPoints {
		result = append(result, sess.HidingPoints[i]...)
		result = append(result, sess.BindingPoints[i]...)
	}
	return result
}

// HandleSignRequest 处理签名请求
func (p *Participant) HandleSignRequest(env *FrostEnvelope) error {
	logs.Debug("[Participant] received sign request from %s for job %s", env.From, env.SessionID)

	p.mu.RLock()
	sess, exists := p.sessions[env.SessionID]
	p.mu.RUnlock()

	if !exists {
		return ErrSessionNotFound
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()

	if sess.State != ParticipantStateNonceGenerated {
		return session.ErrInvalidState
	}

	// 保存聚合的 nonce
	sess.AggregatedNonces = env.Payload

	// 生成签名份额
	shares, err := p.computeSignatureShares(sess)
	if err != nil {
		sess.State = ParticipantStateFailed
		return err
	}

	sess.State = ParticipantStateShareGenerated

	// 发送签名份额
	return p.sendSignatureShares(sess, NodeID(env.From), shares)
}

// computeSignatureShares 计算签名份额
func (p *Participant) computeSignatureShares(sess *ParticipantSession) ([][]byte, error) {
	numTasks := len(sess.HidingNonces)
	shares := make([][]byte, numTasks)

	for i := 0; i < numTasks; i++ {
		// TODO: 实现真正的 FROST 签名份额计算
		// z_i = k_i + λ_i * e * s_i
		// 这里简化为 dummy 实现
		share := make([]byte, 32)
		copy(share, sess.HidingNonces[i])
		shares[i] = share
	}

	return shares, nil
}

// sendSignatureShares 发送签名份额
func (p *Participant) sendSignatureShares(sess *ParticipantSession, coordinator NodeID, shares [][]byte) error {
	var payload []byte
	for _, share := range shares {
		payload = append(payload, share...)
	}

	env := &FrostEnvelope{
		SessionID: sess.JobID,
		Kind:      "SigShare",
		From:      p.nodeID,
		Chain:     sess.Chain,
		VaultID:   sess.VaultID,
		SignAlgo:  int32(sess.SignAlgo),
		Epoch:     sess.KeyEpoch,
		Round:     2,
		Payload:   payload,
	}

	if p.p2p != nil {
		return p.p2p.Send(coordinator, env)
	}
	return nil
}

// GetSession 获取会话
func (p *Participant) GetSession(jobID string) *ParticipantSession {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sessions[jobID]
}

// CloseSession 关闭会话
func (p *Participant) CloseSession(jobID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if sess, exists := p.sessions[jobID]; exists {
		// 释放 nonce
		for _, nonce := range sess.HidingNonces {
			_ = p.sessionStore.ReleaseNonce(nonce)
		}
		delete(p.sessions, jobID)
	}
}
