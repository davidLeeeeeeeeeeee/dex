// frost/runtime/roast/participant.go
// FROST Participant: 签名参与者，响应协调者请求，生成 nonce 和签名份额

package roast

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"dex/frost/core/curve"
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
	shareStore    LocalShareStore

	// 当前区块高度（用于协调者验证）
	currentHeight uint64

	// 本地密钥份额（按 vault 和 epoch 存储）
	shares map[string][]byte // key: "chain_vaultID_epoch" -> share bytes

	Logger logs.Logger
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
func NewParticipant(nodeID NodeID, messenger RoastMessenger, vaultProvider VaultCommitteeProvider, cryptoFactory CryptoExecutorFactory, sessionStore *session.SessionStore, shareStore LocalShareStore, logger logs.Logger) *Participant {
	if sessionStore == nil {
		sessionStore = session.NewSessionStore(nil)
	}
	return &Participant{
		config:        DefaultParticipantConfig(),
		nodeID:        nodeID,
		sessions:      make(map[string]*ParticipantSession),
		messenger:     messenger,
		vaultProvider: vaultProvider,
		cryptoFactory: cryptoFactory,
		sessionStore:  sessionStore,
		shareStore:    shareStore,
		Logger:        logger,
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
	if len(share) == 0 {
		return
	}
	shareCopy := cloneBytes(share)

	p.mu.Lock()
	key := shareKey(chain, vaultID, epoch)
	p.shares[key] = shareCopy
	store := p.shareStore
	p.mu.Unlock()

	if store == nil {
		return
	}
	if err := store.SaveLocalShare(chain, vaultID, epoch, shareCopy); err != nil {
		p.Logger.Warn("[Participant] failed to persist local share for %s/%d/%d: %v", chain, vaultID, epoch, err)
	}
}

// GetShare 获取本地密钥份额
func (p *Participant) GetShare(chain string, vaultID uint32, epoch uint64) []byte {
	p.mu.RLock()
	key := shareKey(chain, vaultID, epoch)
	cached := cloneBytes(p.shares[key])
	store := p.shareStore
	p.mu.RUnlock()

	if store != nil {
		if persisted, err := store.LoadLocalShare(chain, vaultID, epoch); err == nil && len(persisted) > 0 {
			persistedCopy := cloneBytes(persisted)
			p.mu.Lock()
			p.shares[key] = persistedCopy
			p.mu.Unlock()
			return persistedCopy
		}
	}

	return cached
}

func shareKey(chain string, vaultID uint32, epoch uint64) string {
	return fmt.Sprintf("%s_%d_%d", chain, vaultID, epoch)
}

func cloneBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}

func cloneMessages(src [][]byte) [][]byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([][]byte, len(src))
	for i := range src {
		dst[i] = cloneBytes(src[i])
	}
	return dst
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
	decodedMsgs, _, ok := decodeRoastRequestPayload(env.Payload)
	if !ok || len(decodedMsgs) == 0 {
		return errors.New("nonce request missing signing messages")
	}
	messages := decodedMsgs
	sess := p.getOrCreateSession(env.SessionID, env.Chain, env.VaultID, env.Epoch, env.SignAlgo, myIndex, myShare, messages)

	// 生成 nonce
	numTasks := len(sess.Messages)
	if numTasks <= 0 {
		numTasks = 1
	}
	if err := p.generateNonces(sess, numTasks); err != nil {
		return err
	}

	// 发送 nonce 承诺给协调者
	return p.sendNonceCommitment(sess, env.From)
}

// getOrCreateSession 获取或创建会话
func (p *Participant) getOrCreateSession(jobID, chain string, vaultID uint32, epoch uint64, signAlgo pb.SignAlgo, myIndex int, myShare []byte, messages [][]byte) *ParticipantSession {
	p.mu.Lock()
	defer p.mu.Unlock()

	if sess, exists := p.sessions[jobID]; exists {
		if len(sess.Messages) == 0 && len(messages) > 0 {
			sess.Messages = cloneMessages(messages)
		}
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
		Messages:  cloneMessages(messages),
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
		sess.HidingPoints[i] = roastExec.SerializePoint(hidingPt)
		sess.BindingPoints[i] = roastExec.SerializePoint(bindingPt)

		// 绑定 nonce 到消息（优先使用任务消息，兼容旧流程 fallback 到 jobID）
		msg := []byte(sess.JobID)
		if i < len(sess.Messages) && len(sess.Messages[i]) > 0 {
			msg = sess.Messages[i]
		}
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

	// 保存聚合的 nonce 和消息（优先解析新格式，兼容旧格式）
	if decodedMsgs, decodedNonces, ok := decodeRoastRequestPayload(env.Payload); ok {
		if len(decodedMsgs) > 0 {
			sess.Messages = cloneMessages(decodedMsgs)
		}
		sess.AggregatedNonces = decodedNonces
	} else {
		sess.AggregatedNonces = env.Payload
	}

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
	if len(sess.Messages) == 0 {
		return nil, errors.New("missing signing messages in participant session")
	}
	if len(sess.HidingNonces) != len(sess.Messages) {
		return nil, fmt.Errorf("nonce/message task count mismatch: nonces=%d messages=%d", len(sess.HidingNonces), len(sess.Messages))
	}

	numTasks := len(sess.HidingNonces)
	shares := make([][]byte, numTasks)

	// 获取密码学执行器
	roastExec, err := p.cryptoFactory.NewROASTExecutor(int32(sess.SignAlgo))
	if err != nil {
		return nil, err
	}

	// 解析本地密钥份额
	myShare := new(big.Int).SetBytes(sess.MyShare)
	resolvedGroupPubX := big.NewInt(0)
	var groupPubBytes []byte
	if p.vaultProvider == nil {
		return nil, errors.New("vault provider is required for signing")
	}
	pubBytes, err := p.vaultProvider.VaultGroupPubkey(sess.Chain, sess.VaultID, sess.KeyEpoch)
	if err != nil {
		return nil, fmt.Errorf("failed to load group pubkey for signing chain=%s vault=%d epoch=%d: %w", sess.Chain, sess.VaultID, sess.KeyEpoch, err)
	}
	if len(pubBytes) < 32 {
		return nil, fmt.Errorf("invalid group pubkey length for signing: %d", len(pubBytes))
	}
	groupPubBytes = pubBytes
	logs.Info("[Participant] signing context miner=%s job=%s chain=%s vault=%d epoch=%d sign_algo=%s tasks=%d aggregated_pubkey=%s",
		p.nodeID, sess.JobID, sess.Chain, sess.VaultID, sess.KeyEpoch, sess.SignAlgo.String(), numTasks, hex.EncodeToString(groupPubBytes))
	if len(pubBytes) == 33 {
		resolvedGroupPubX = new(big.Int).SetBytes(pubBytes[1:33])
	} else {
		resolvedGroupPubX = new(big.Int).SetBytes(pubBytes[:32])
	}
	if normalizedShare := normalizeSecretShareForBIP340(sess.SignAlgo, groupPubBytes, myShare); normalizedShare != myShare {
		myShare = normalizedShare
		logs.Info("[Participant] normalized BIP340 secret share for odd-Y group pubkey chain=%s vault=%d epoch=%d", sess.Chain, sess.VaultID, sess.KeyEpoch)
	}

	// 解析聚合的 nonces 获取所有参与者的 nonce 承诺
	allNonces := parseAggregatedNonces(sess.AggregatedNonces, numTasks, sess.SignAlgo)

	for i := 0; i < numTasks; i++ {
		// 获取待签名消息（必须由协调者下发，禁止隐式 fallback）
		msg := sess.Messages[i]
		if len(msg) == 0 {
			return nil, fmt.Errorf("empty signing message for task %d", i)
		}
		logs.Info("[Participant] signing input miner=%s job=%s chain=%s vault=%d epoch=%d task=%d message=%s",
			p.nodeID, sess.JobID, sess.Chain, sess.VaultID, sess.KeyEpoch, i, hex.EncodeToString(msg))

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
		selfSignerID, found := findSignerIDByNoncePoints(taskNonces, hidingPt, bindingPt)
		if !found {
			return nil, fmt.Errorf("self nonce missing from aggregated nonces for task %d (job=%s my_index=%d)",
				i, sess.JobID, sess.MyIndex+1)
		}
		if selfSignerID != sess.MyIndex+1 {
			logs.Warn("[Participant] signer id remapped by nonce match job=%s task=%d my_index=%d resolved_signer_id=%d",
				sess.JobID, i, sess.MyIndex+1, selfSignerID)
		}

		// 计算绑定系数 ρ_i
		rho := roastExec.ComputeBindingCoefficient(selfSignerID, msg, taskNonces)

		// 计算拉格朗日系数 λ_i
		lambdas := roastExec.ComputeLagrangeCoefficients(signerIDs)
		lambda := lambdas[selfSignerID]
		if lambda == nil {
			lambda = big.NewInt(1)
		}

		// 计算群承诺 R
		R, err := roastExec.ComputeGroupCommitment(taskNonces, msg)
		if err != nil {
			return nil, err
		}

		// Use the already validated group pubkey X coordinate for the whole session.
		groupPubX := new(big.Int).Set(resolvedGroupPubX)

		// 计算挑战值 e
		e := roastExec.ComputeChallenge(R, groupPubX, msg)

		// 计算部分签名 z_i = k_i + ρ_i * k'_i + λ_i * e * s_i
		adjHidingNonce, adjBindingNonce := adjustNoncePairForBIP340(sess.SignAlgo, R, hidingNonce, bindingNonce)
		z := roastExec.ComputePartialSignature(PartialSignParams{
			SignerID:     selfSignerID,
			HidingNonce:  adjHidingNonce,
			BindingNonce: adjBindingNonce,
			SecretShare:  myShare,
			Rho:          rho,
			Lambda:       lambda,
			Challenge:    e,
		})

		// 序列化份额（32 字节）
		shareBytes := make([]byte, 32)
		z.FillBytes(shareBytes)
		shares[i] = shareBytes
		logs.Info("[Participant] signature share generated miner=%s job=%s chain=%s vault=%d epoch=%d task=%d signer_id=%d share=%s",
			p.nodeID, sess.JobID, sess.Chain, sess.VaultID, sess.KeyEpoch, i, selfSignerID, hex.EncodeToString(shareBytes))
	}

	return shares, nil
}

func normalizeSecretShareForBIP340(signAlgo pb.SignAlgo, groupPubBytes []byte, share *big.Int) *big.Int {
	if share == nil {
		return nil
	}
	if signAlgo != pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340 {
		return share
	}
	if len(groupPubBytes) != 33 || groupPubBytes[0] != 0x03 {
		return share
	}

	order := curve.NewSecp256k1Group().Order()
	normalized := new(big.Int).Sub(order, share)
	normalized.Mod(normalized, order)
	return normalized
}

func adjustNoncePairForBIP340(signAlgo pb.SignAlgo, groupCommit CurvePoint, hidingNonce, bindingNonce *big.Int) (*big.Int, *big.Int) {
	if hidingNonce == nil || bindingNonce == nil {
		return hidingNonce, bindingNonce
	}
	if signAlgo != pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340 {
		return hidingNonce, bindingNonce
	}
	if groupCommit.Y == nil || groupCommit.Y.Bit(0) == 0 {
		return hidingNonce, bindingNonce
	}

	order := curve.NewSecp256k1Group().Order()
	adjHiding := new(big.Int).Sub(order, hidingNonce)
	adjHiding.Mod(adjHiding, order)
	adjBinding := new(big.Int).Sub(order, bindingNonce)
	adjBinding.Mod(adjBinding, order)
	return adjHiding, adjBinding
}

func findSignerIDByNoncePoints(taskNonces []NonceInput, hidingPoint, bindingPoint CurvePoint) (int, bool) {
	for _, n := range taskNonces {
		if sameCurvePoint(n.HidingPoint, hidingPoint) && sameCurvePoint(n.BindingPoint, bindingPoint) {
			return n.SignerID, true
		}
	}
	return 0, false
}

func sameCurvePoint(a, b CurvePoint) bool {
	if a.X == nil || a.Y == nil || b.X == nil || b.Y == nil {
		return false
	}
	return a.X.Cmp(b.X) == 0 && a.Y.Cmp(b.Y) == 0
}

// parseAggregatedNonces 解析聚合的 nonces
func parseAggregatedNonces(data []byte, numTasks int, signAlgo pb.SignAlgo) [][]NonceInput {
	result := make([][]NonceInput, numTasks)
	for i := range result {
		result[i] = []NonceInput{}
	}

	if len(data) == 0 || numTasks <= 0 {
		return result
	}

	signerIDs, noncePayload, hasHeader := decodeAggregatedNoncePayload(data)
	if !hasHeader {
		noncePayload = data
	}

	// each nonce commitment is (hiding point + binding point)
	pointSize := getPointSize(signAlgo)
	nonceSize := 2 * pointSize
	if nonceSize <= 0 || len(noncePayload)%nonceSize != 0 {
		return result
	}

	var signerCount int
	if hasHeader {
		signerCount = len(signerIDs)
		if signerCount == 0 {
			return result
		}
		expectedLen := signerCount * nonceSize * numTasks
		if len(noncePayload) != expectedLen {
			logs.Warn("[Participant] invalid aggregated nonce payload length: got=%d want=%d signers=%d tasks=%d",
				len(noncePayload), expectedLen, signerCount, numTasks)
			return result
		}
	} else {
		totalPerSigner := nonceSize * numTasks
		if totalPerSigner <= 0 || len(noncePayload)%totalPerSigner != 0 {
			logs.Warn("[Participant] legacy aggregated nonce payload length mismatch: got=%d per_signer=%d tasks=%d",
				len(noncePayload), totalPerSigner, numTasks)
			return result
		}
		signerCount = len(noncePayload) / totalPerSigner
		if signerCount == 0 {
			return result
		}
		signerIDs = make([]int, signerCount)
		for i := 0; i < signerCount; i++ {
			signerIDs[i] = i + 1
		}
	}

	offset := 0
	// signer-major order:
	// signer1(task0..taskN), signer2(task0..taskN), ...
	for signerPos := 0; signerPos < signerCount && offset < len(noncePayload); signerPos++ {
		signerID := signerIDs[signerPos]
		if signerID <= 0 {
			continue
		}
		for taskIdx := 0; taskIdx < numTasks && offset+nonceSize <= len(noncePayload); taskIdx++ {
			hiding := noncePayload[offset : offset+pointSize]
			binding := noncePayload[offset+pointSize : offset+nonceSize]
			offset += nonceSize

			hidingPoint, err := decodeSerializedPoint(signAlgo, hiding)
			if err != nil {
				logs.Warn("[Participant] failed to decode hiding nonce point (signer=%d task=%d): %v", signerID, taskIdx, err)
				continue
			}
			bindingPoint, err := decodeSerializedPoint(signAlgo, binding)
			if err != nil {
				logs.Warn("[Participant] failed to decode binding nonce point (signer=%d task=%d): %v", signerID, taskIdx, err)
				continue
			}

			result[taskIdx] = append(result[taskIdx], NonceInput{
				SignerID:     signerID,
				HidingPoint:  hidingPoint,
				BindingPoint: bindingPoint,
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
