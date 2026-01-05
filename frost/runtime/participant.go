// frost/runtime/participant.go
// FROST Participant: 签名参与者，响应协调者请求，生成 nonce 和签名份额

package runtime

import (
	"errors"
	"math/big"
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
	messenger     RoastMessenger
	vaultProvider VaultCommitteeProvider
	cryptoFactory CryptoExecutorFactory // 密码学执行器工厂
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
func NewParticipant(nodeID NodeID, messenger RoastMessenger, vaultProvider VaultCommitteeProvider, cryptoFactory CryptoExecutorFactory, sessionStore *session.SessionStore, config *ParticipantConfig) *Participant {
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
		messenger:     messenger,
		vaultProvider: vaultProvider,
		cryptoFactory: cryptoFactory,
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
	return p.HandleRoastNonceRequest(FromFrostEnvelope(env))
}

// HandleRoastNonceRequest handles nonce requests using RoastEnvelope.
func (p *Participant) HandleRoastNonceRequest(env *RoastEnvelope) error {
	if env == nil {
		return ErrInvalidNonceRequest
	}
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
	sess := p.getOrCreateSession(env.SessionID, env.Chain, env.VaultID, env.Epoch, env.SignAlgo, myIndex, myShare)

	// 生成 nonce
	numTasks := 1 // 默认单任务，实际应从请求中获取
	if err := p.generateNonces(sess, numTasks); err != nil {
		return err
	}

	// 发送 nonce 承诺给协调者
	return p.sendNonceCommitment(sess, env.From)
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

	// 获取密码学执行器
	roastExec, err := p.cryptoFactory.NewROASTExecutor(int32(sess.SignAlgo))
	if err != nil {
		return err
	}

	sess.HidingNonces = make([][]byte, numTasks)
	sess.BindingNonces = make([][]byte, numTasks)
	sess.HidingPoints = make([][]byte, numTasks)
	sess.BindingPoints = make([][]byte, numTasks)

	for i := 0; i < numTasks; i++ {
		// 通过接口生成 nonce
		hiding, binding, hidingPt, bindingPt, err := roastExec.GenerateNoncePair()
		if err != nil {
			return err
		}

		// 序列化 nonce 标量
		hidingBytes := make([]byte, 32)
		bindingBytes := make([]byte, 32)
		hiding.FillBytes(hidingBytes)
		binding.FillBytes(bindingBytes)

		sess.HidingNonces[i] = hidingBytes
		sess.BindingNonces[i] = bindingBytes

		// 序列化承诺点
		hidingPointBytes := make([]byte, 32)
		bindingPointBytes := make([]byte, 32)
		hidingPt.X.FillBytes(hidingPointBytes)
		bindingPt.X.FillBytes(bindingPointBytes)
		sess.HidingPoints[i] = hidingPointBytes
		sess.BindingPoints[i] = bindingPointBytes

		// 绑定 nonce 到会话
		msg := []byte(sess.JobID)
		if err := p.sessionStore.BindNonce(hidingBytes, msg, sess.KeyEpoch); err != nil {
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

	msg := &RoastEnvelope{
		SessionID: sess.JobID,
		Kind:      "NonceCommit",
		From:      p.nodeID,
		Chain:     sess.Chain,
		VaultID:   sess.VaultID,
		SignAlgo:  sess.SignAlgo,
		Epoch:     sess.KeyEpoch,
		Round:     1,
		Payload:   p.serializeNonces(sess),
	}

	if p.messenger != nil {
		return p.messenger.Send(coordinator, msg)
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
	return p.HandleRoastSignRequest(FromFrostEnvelope(env))
}

// HandleRoastSignRequest handles sign requests using RoastEnvelope.
func (p *Participant) HandleRoastSignRequest(env *RoastEnvelope) error {
	if env == nil {
		return ErrInvalidSignRequest
	}
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
	return p.sendSignatureShares(sess, env.From, shares)
}

// computeSignatureShares 计算签名份额
// z_i = k_i + ρ_i * k'_i + λ_i * e * s_i
func (p *Participant) computeSignatureShares(sess *ParticipantSession) ([][]byte, error) {
	numTasks := len(sess.HidingNonces)
	shares := make([][]byte, numTasks)

	// 获取密码学执行器
	roastExec, err := p.cryptoFactory.NewROASTExecutor(int32(sess.SignAlgo))
	if err != nil {
		return nil, err
	}

	// 解析本地密钥份额
	myShare := new(big.Int).SetBytes(sess.MyShare)

	// 解析聚合的 nonces 获取所有参与者的 nonce 承诺
	allNonces := parseAggregatedNonces(sess.AggregatedNonces, numTasks)

	for i := 0; i < numTasks; i++ {
		// 解析本地 nonce
		hidingNonce := new(big.Int).SetBytes(sess.HidingNonces[i])
		bindingNonce := new(big.Int).SetBytes(sess.BindingNonces[i])

		// 计算 nonce 承诺点（通过接口）
		hidingPt := roastExec.ScalarBaseMult(hidingNonce)
		bindingPt := roastExec.ScalarBaseMult(bindingNonce)

		// 构建当前任务的 nonces
		taskNonces := allNonces[i]
		if len(taskNonces) == 0 {
			// 至少包含本节点的 nonce
			taskNonces = []NonceInput{{
				SignerID:     sess.MyIndex + 1,
				HidingNonce:  hidingNonce,
				BindingNonce: bindingNonce,
				HidingPoint:  hidingPt,
				BindingPoint: bindingPt,
			}}
		}

		// 构建签名者 ID 列表
		signerIDs := make([]int, len(taskNonces))
		for j, n := range taskNonces {
			signerIDs[j] = n.SignerID
		}

		// 计算绑定系数 ρ_i
		var msg []byte
		if i < len(sess.Messages) {
			msg = sess.Messages[i]
		}
		if msg == nil {
			msg = []byte(sess.JobID) // 默认使用 jobID 作为消息
		}
		rho := roastExec.ComputeBindingCoefficient(sess.MyIndex+1, msg, taskNonces)

		// 计算拉格朗日系数 λ_i
		lambdas := roastExec.ComputeLagrangeCoefficients(signerIDs)
		lambda := lambdas[sess.MyIndex+1]
		if lambda == nil {
			lambda = big.NewInt(1)
		}

		// 计算群承诺 R
		R, err := roastExec.ComputeGroupCommitment(taskNonces, msg)
		if err != nil {
			return nil, err
		}

		// 从 vaultProvider 获取群公钥
		groupPubX := big.NewInt(0)
		if p.vaultProvider != nil {
			groupPubBytes, err := p.vaultProvider.VaultGroupPubkey(sess.Chain, sess.VaultID, sess.KeyEpoch)
			if err == nil && len(groupPubBytes) >= 32 {
				// 解析群公钥 X 坐标（假设压缩格式：1 字节前缀 + 32 字节 X）
				if len(groupPubBytes) == 33 {
					groupPubX = new(big.Int).SetBytes(groupPubBytes[1:33])
				} else if len(groupPubBytes) >= 32 {
					groupPubX = new(big.Int).SetBytes(groupPubBytes[:32])
				}
			}
		}

		// 计算挑战值 e
		e := roastExec.ComputeChallenge(R, groupPubX, msg)

		// 计算部分签名 z_i = k_i + ρ_i * k'_i + λ_i * e * s_i
		z := roastExec.ComputePartialSignature(PartialSignParams{
			SignerID:     sess.MyIndex + 1,
			HidingNonce:  hidingNonce,
			BindingNonce: bindingNonce,
			SecretShare:  myShare,
			Rho:          rho,
			Lambda:       lambda,
			Challenge:    e,
		})

		// 序列化份额（32 字节）
		shareBytes := make([]byte, 32)
		z.FillBytes(shareBytes)
		shares[i] = shareBytes
	}

	return shares, nil
}

// parseAggregatedNonces 解析聚合的 nonces
func parseAggregatedNonces(data []byte, numTasks int) [][]NonceInput {
	result := make([][]NonceInput, numTasks)
	for i := range result {
		result[i] = []NonceInput{}
	}

	if len(data) == 0 {
		return result
	}

	// 简化实现：假设 data 格式为 [task0_nonces...][task1_nonces...]
	// 每个 nonce 为 64 字节（hiding 32 + binding 32）
	nonceSize := 64
	noncesPerTask := len(data) / (nonceSize * numTasks)
	if noncesPerTask == 0 {
		noncesPerTask = 1
	}

	offset := 0
	for taskIdx := 0; taskIdx < numTasks && offset < len(data); taskIdx++ {
		for j := 0; j < noncesPerTask && offset+nonceSize <= len(data); j++ {
			hiding := data[offset : offset+32]
			binding := data[offset+32 : offset+64]
			offset += nonceSize

			result[taskIdx] = append(result[taskIdx], NonceInput{
				SignerID: j + 1,
				HidingPoint: CurvePoint{
					X: new(big.Int).SetBytes(hiding),
					Y: big.NewInt(0),
				},
				BindingPoint: CurvePoint{
					X: new(big.Int).SetBytes(binding),
					Y: big.NewInt(0),
				},
			})
		}
	}

	return result
}

// sendSignatureShares 发送签名份额
func (p *Participant) sendSignatureShares(sess *ParticipantSession, coordinator NodeID, shares [][]byte) error {
	var payload []byte
	for _, share := range shares {
		payload = append(payload, share...)
	}

	msg := &RoastEnvelope{
		SessionID: sess.JobID,
		Kind:      "SigShare",
		From:      p.nodeID,
		Chain:     sess.Chain,
		VaultID:   sess.VaultID,
		SignAlgo:  sess.SignAlgo,
		Epoch:     sess.KeyEpoch,
		Round:     2,
		Payload:   payload,
	}

	if p.messenger != nil {
		return p.messenger.Send(coordinator, msg)
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
