// frost/runtime/roast/participant.go
// FROST Participant: 签名参与者，响应协调者请求，生成 nonce 和签名份额

package roast

import (
	"crypto/sha256"
	"errors"
	"fmt"
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

	// 当前区块高度（用于协调者验证）
	currentHeight uint64

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
		currentHeight: 0,
		shares:        make(map[string][]byte),
	}
}

// UpdateHeight 更新当前区块高度
func (p *Participant) UpdateHeight(height uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.currentHeight = height
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
func (p *Participant) HandleRoastNonceRequest(env *Envelope) error {
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

	// 验证请求是否来自当前协调者（防止旧协调者的请求）
	// 参与者仅接受当前 agg_index 对应协调者的请求
	if err := p.verifyCoordinator(env); err != nil {
		logs.Warn("[Participant] rejected nonce request from non-current coordinator: %v", err)
		return err
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

	msg := &Envelope{
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
		return p.messenger.Send(coordinator, toTypesRoastEnvelope(msg))
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
func (p *Participant) HandleRoastSignRequest(env *Envelope) error {
	if env == nil {
		return ErrInvalidSignRequest
	}
	logs.Debug("[Participant] received sign request from %s for job %s", env.From, env.SessionID)

	// 验证请求是否来自当前协调者（防止旧协调者的请求）
	if err := p.verifyCoordinator(env); err != nil {
		logs.Warn("[Participant] rejected sign request from non-current coordinator: %v", err)
		return err
	}

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
		// 获取待签名消息
		var msg []byte
		if i < len(sess.Messages) {
			msg = sess.Messages[i]
		}
		if msg == nil {
			msg = []byte(sess.JobID) // 默认使用 jobID 作为消息
		}

		// 验证 nonce 是否绑定到正确的消息（防二次签名攻击）
		hidingNonceBytes := sess.HidingNonces[i]
		if !p.sessionStore.ValidateNonceForMessage(hidingNonceBytes, msg) {
			return nil, fmt.Errorf("nonce for task %d is not bound to message (possible replay attack)", i)
		}

		// 解析本地 nonce
		hidingNonce := new(big.Int).SetBytes(hidingNonceBytes)
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

	msg := &Envelope{
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
		return p.messenger.Send(coordinator, toTypesRoastEnvelope(msg))
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

// verifyCoordinator 验证请求是否来自当前协调者
// 参与者仅接受当前 agg_index 对应协调者的请求
func (p *Participant) verifyCoordinator(env *Envelope) error {
	// 获取委员会
	committee, err := p.vaultProvider.VaultCommittee(env.Chain, env.VaultID, env.Epoch)
	if err != nil {
		return err
	}

	if len(committee) == 0 {
		return errors.New("empty committee")
	}

	// 计算当前协调者索引（与 Coordinator 使用相同的算法）
	coordIndex := p.computeCoordinatorIndex(env.SessionID, env.Epoch, committee, env.Chain)

	// 检查请求是否来自当前协调者
	if coordIndex < 0 || coordIndex >= len(committee) {
		return errors.New("invalid coordinator index")
	}

	currentCoordID := NodeID(committee[coordIndex].ID)
	if env.From != currentCoordID {
		return fmt.Errorf("request from non-current coordinator: expected=%s, got=%s", currentCoordID, env.From)
	}

	return nil
}

// computeCoordinatorIndex 计算当前协调者索引（与 Coordinator 使用相同的算法）
func (p *Participant) computeCoordinatorIndex(jobID string, keyEpoch uint64, committee []SignerInfo, chain string) int {
	if len(committee) == 0 {
		return 0
	}

	// 计算种子：seed = H(session_id || key_epoch || "frost_agg")
	seed := computeAggregatorSeed(jobID, keyEpoch)

	// 转换为 Participant 列表
	participants := make([]ParticipantInfo, len(committee))
	for i, member := range committee {
		participants[i] = ParticipantInfo{
			ID:    string(member.ID),
			Index: i,
		}
	}

	// 确定性排列委员会
	permuted := permuteParticipantList(participants, seed)

	// 获取会话的起始高度（简化：假设从当前高度开始，实际应从会话存储获取）
	// TODO: 从会话存储获取 StartHeight
	startHeight := p.currentHeight // 简化处理
	blocksElapsed := p.currentHeight - startHeight
	if blocksElapsed < 0 {
		blocksElapsed = 0
	}

	// 计算轮换次数（超时切换）
	// agg_index = floor((now_height - session_start_height) / agg_timeout_blocks) % len(agg_candidates)
	aggTimeoutBlocks := uint64(10) // TODO: 从配置获取
	rotations := blocksElapsed / aggTimeoutBlocks
	aggIndex := int(rotations) % len(permuted)

	// 找回原始索引
	for i, member := range committee {
		if member.ID == NodeID(permuted[aggIndex].ID) {
			return i
		}
	}
	return 0
}

// ParticipantInfo 参与者信息（用于排列）
type ParticipantInfo struct {
	ID    string
	Index int
}

// permuteParticipantList 确定性排列参与者列表
func permuteParticipantList(participants []ParticipantInfo, seed []byte) []ParticipantInfo {
	result := make([]ParticipantInfo, len(participants))
	copy(result, participants)

	// 使用 Fisher-Yates 洗牌，种子决定随机序列
	for i := len(result) - 1; i > 0; i-- {
		// 从种子派生确定性随机数
		indexSeed := sha256.Sum256(append(seed, byte(i)))
		j := int(indexSeed[0]) % (i + 1)
		result[i], result[j] = result[j], result[i]
	}

	return result
}
