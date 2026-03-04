// frost/runtime/roast/coordinator.go
// ROAST Coordinator: 协调签名会话，收集 nonce 承诺和签名份额

package roast

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"dex/frost/core/curve"
	"dex/frost/core/frost"
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
	c.logInfo("[Coordinator] start signing session job=%s chain=%s vault=%d epoch=%d threshold=%d sign_algo=%s tasks=%d",
		params.JobID, params.Chain, params.VaultID, params.KeyEpoch, params.Threshold, params.SignAlgo.String(), len(params.Messages))
	c.logInfo("[Coordinator] vault voter committee job=%s chain=%s vault=%d epoch=%d size=%d members=%s",
		params.JobID, params.Chain, params.VaultID, params.KeyEpoch, len(committeeInfo), formatSignerInfoList(committeeInfo))
	for i, msg := range params.Messages {
		c.logInfo("[Coordinator] signing message job=%s chain=%s vault=%d task=%d payload=%s",
			params.JobID, params.Chain, params.VaultID, i, hex.EncodeToString(msg))
	}
	if groupPubkey, pubErr := c.vaultProvider.VaultGroupPubkey(params.Chain, params.VaultID, params.KeyEpoch); pubErr != nil {
		c.logWarn("[Coordinator] failed to load aggregated public key for job=%s chain=%s vault=%d epoch=%d: %v",
			params.JobID, params.Chain, params.VaultID, params.KeyEpoch, pubErr)
	} else {
		c.logInfo("[Coordinator] aggregated public key job=%s chain=%s vault=%d epoch=%d pubkey=%s",
			params.JobID, params.Chain, params.VaultID, params.KeyEpoch, hex.EncodeToString(groupPubkey))
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
	if myIndex < 0 {
		return ErrParticipantNotFound
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

	if !c.isCurrentCoordinator(sess) {
		return ErrCoordinatorNotLeader
	}
	if err := sess.Start(); err != nil {
		return err
	}
	c.sessions[params.JobID] = sess
	go c.runCoordinatorLoop(ctx, sess)

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
	c.logInfo("[Coordinator] starting coordinator loop job=%s chain=%s vault=%d epoch=%d state=%d messenger=%v",
		sess.JobID, sess.Chain, sess.VaultID, sess.KeyEpoch, sess.GetState(), c.messenger != nil)

	// 定期检查协调者切换的 ticker
	rotateTicker := time.NewTicker(1 * time.Second)
	defer rotateTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.logInfo("[Coordinator] context cancelled for session %s", sess.JobID)
			return
		case <-rotateTicker.C:
			// 检查是否需要切换协调者（超时切换）
			if !c.isCurrentCoordinator(sess) {
				// 本节点不再是协调者，停止循环
				c.logInfo("[Coordinator] no longer coordinator for session %s, stopping", sess.JobID)
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
				c.logInfo("[Coordinator] nonces collected, freezing nonce payload and broadcasting sign request job=%s", sess.JobID)
				// 冻结 nonce payload：首次计算后缓存，后续重复广播使用相同 payload
				c.freezeAndBroadcastSignRequest(sess)
				sess.SetState(roastsession.SignSessionStateCollectingShares)
			} else {
				c.logInfo("[Coordinator] nonce collection timeout job=%s retry=%d", sess.JobID, sess.RetryCount)
				if c.shouldRotateCoordinator(sess) {
					c.logInfo("[Coordinator] rotating coordinator for session %s due to nonce timeout", sess.JobID)
					return
				}
				if !c.retryWithNewSet(sess) {
					c.logInfo("[Coordinator] max retries exceeded, session %s FAILED at nonce collection", sess.JobID)
					sess.SetState(roastsession.SignSessionStateFailed)
					return
				}
			}

		case roastsession.SignSessionStateCollectingShares:
			// 重复广播签名请求（使用冻结的 nonce payload），确保 participant 不会漏掉
			c.broadcastSignRequest(sess)

			// 等待收集或超时
			if c.waitForShares(ctx, sess) {
				c.logInfo("[Coordinator] shares collected, aggregating job=%s", sess.JobID)
				sess.SetState(roastsession.SignSessionStateAggregating)
			} else {
				c.logInfo("[Coordinator] share collection timeout job=%s retry=%d", sess.JobID, sess.RetryCount)
				if c.shouldRotateCoordinator(sess) {
					c.logInfo("[Coordinator] rotating coordinator for session %s due to share timeout", sess.JobID)
					return
				}
				if !c.retryWithNewSet(sess) {
					c.logInfo("[Coordinator] max retries exceeded, session %s FAILED at share collection", sess.JobID)
					sess.SetState(roastsession.SignSessionStateFailed)
					return
				}
			}

		case roastsession.SignSessionStateAggregating:
			// 聚合签名（支持部分完成）
			signatures, err := c.aggregateSignatures(sess)
			if err != nil {
				c.logWarn("[Coordinator] aggregate failed job=%s: %v", sess.JobID, err)
				if c.shouldRotateCoordinator(sess) {
					c.logInfo("[Coordinator] rotating coordinator for session %s due to aggregate failure", sess.JobID)
					return
				}
				if !c.retryWithNewSet(sess) {
					c.logInfo("[Coordinator] max retries exceeded, session %s FAILED at aggregation", sess.JobID)
					sess.SetState(roastsession.SignSessionStateFailed)
					return
				}
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
		Payload:   encodeRoastRequestPayload(sess.Messages, nil),
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

// broadcastSignRequest 广播签名请求（使用缓存的 nonce payload）
func (c *Coordinator) broadcastSignRequest(sess *roastsession.Session) {
	if c.messenger == nil {
		return
	}

	// 使用缓存的 nonce payload（由 freezeAndBroadcastSignRequest 设置）
	c.mu.RLock()
	payload := sess.CachedNoncePayload
	c.mu.RUnlock()
	if len(payload) == 0 {
		// 回退：如果没缓存，重新计算（不应该发生）
		payload = c.aggregateNonces(sess)
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
		Kind:      "SignRequest",
		From:      c.nodeID,
		Chain:     sess.Chain,
		VaultID:   sess.VaultID,
		SignAlgo:  pb.SignAlgo(sess.SignAlgo),
		Epoch:     sess.KeyEpoch,
		Round:     2,
		Payload:   payload,
	}

	_ = c.messenger.Broadcast(peers, toTypesRoastEnvelope(msg))
}

// freezeAndBroadcastSignRequest 首次计算 nonce payload 并缓存，然后广播
func (c *Coordinator) freezeAndBroadcastSignRequest(sess *roastsession.Session) {
	payload := c.aggregateNonces(sess)
	c.mu.Lock()
	sess.CachedNoncePayload = payload
	c.mu.Unlock()
	c.broadcastSignRequest(sess)
}

// aggregateNonces 聚合 nonce 承诺
func (c *Coordinator) aggregateNonces(sess *roastsession.Session) []byte {
	var buf bytes.Buffer
	signerIDs := make([]int, 0)
	selectedSet := sess.SelectedSetSnapshot()
	for _, idx := range selectedSet {
		if nonce, ok := sess.GetNonce(idx); ok {
			signerIDs = append(signerIDs, idx+1)
			for i := range nonce.HidingNonces {
				buf.Write(nonce.HidingNonces[i])
				buf.Write(nonce.BindingNonces[i])
			}
		}
	}
	if buf.Len() == 0 || len(signerIDs) == 0 {
		return nil
	}
	// Backward compatibility: legacy participants can only parse signer IDs as
	// 1..N from payload order. Keep raw payload for that case.
	needsHeader := false
	for pos, signerID := range signerIDs {
		if signerID != pos+1 {
			needsHeader = true
			break
		}
	}
	if !needsHeader {
		return buf.Bytes()
	}
	return encodeAggregatedNoncePayload(signerIDs, buf.Bytes())
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
		// 只使用同时提供了 nonce 和 share 的参与者（取交集）
		// 确保 R 的 nonce 贡献者集合 = z 的 share 贡献者集合
		nonces := make([]NonceInput, 0, len(selectedSet))
		shares := make([]ShareInput, 0, len(selectedSet))
		for _, idx := range selectedSet {
			nonceData, nonceOK := sess.GetNonce(idx)
			shareData, shareOK := sess.GetShare(idx)
			if !nonceOK || !shareOK {
				continue
			}
			if taskIdx >= len(nonceData.HidingNonces) || taskIdx >= len(nonceData.BindingNonces) || taskIdx >= len(shareData.Shares) {
				continue
			}

			hidingBytes := nonceData.HidingNonces[taskIdx]
			bindingBytes := nonceData.BindingNonces[taskIdx]
			hidingPoint, err := decodeSerializedPoint(pb.SignAlgo(sess.SignAlgo), hidingBytes)
			if err != nil {
				logs.Warn("[Coordinator] task %d: invalid hiding nonce point from signer %d: %v", taskIdx, idx+1, err)
				continue
			}
			bindingPoint, err := decodeSerializedPoint(pb.SignAlgo(sess.SignAlgo), bindingBytes)
			if err != nil {
				logs.Warn("[Coordinator] task %d: invalid binding nonce point from signer %d: %v", taskIdx, idx+1, err)
				continue
			}

			shareValue := new(big.Int).SetBytes(shareData.Shares[taskIdx])
			nonces = append(nonces, NonceInput{
				SignerID:     idx + 1,
				HidingPoint:  hidingPoint,
				BindingPoint: bindingPoint,
			})
			shares = append(shares, ShareInput{
				SignerID: idx + 1,
				Share:    shareValue,
			})
		}

		if len(nonces) < minSigners {
			logs.Debug("[Coordinator] task %d: insufficient nonce+share pairs (%d < %d), continue collecting", taskIdx, len(nonces), minSigners)
			continue
		}

		// 该 task 有足够的数据，尝试聚合
		R, err := roastExec.ComputeGroupCommitment(nonces, msg)
		if err != nil {
			logs.Warn("[Coordinator] task %d: failed to compute group commitment: %v", taskIdx, err)
			continue
		}

		// --- 诊断日志：输出 Coordinator 侧的 R、e、signer IDs、share 值 ---
		signerIDs := make([]int, 0, len(shares))
		for _, s := range shares {
			signerIDs = append(signerIDs, s.SignerID)
		}
		rParity := -1
		if R.Y != nil {
			rParity = int(R.Y.Bit(0))
		}
		coordGroupPubX := big.NewInt(0)
		if c.vaultProvider != nil {
			if gpb, gpErr := c.vaultProvider.VaultGroupPubkey(sess.Chain, sess.VaultID, sess.KeyEpoch); gpErr == nil && len(gpb) >= 32 {
				if len(gpb) == 33 {
					coordGroupPubX = new(big.Int).SetBytes(gpb[1:33])
				} else {
					coordGroupPubX = new(big.Int).SetBytes(gpb[:32])
				}
			}
		}
		diagE := roastExec.ComputeChallenge(R, coordGroupPubX, msg)
		diagLambdas := roastExec.ComputeLagrangeCoefficients(signerIDs)
		rxBytes := make([]byte, 32)
		if R.X != nil {
			R.X.FillBytes(rxBytes)
		}
		gpxBytes := make([]byte, 32)
		coordGroupPubX.FillBytes(gpxBytes)
		eBytes := make([]byte, 32)
		diagE.FillBytes(eBytes)
		c.logInfo("[Coordinator][sign-diag] job=%s task=%d R_x=%s R_y_parity=%d groupPubX=%s challenge=%s signer_ids=%v",
			sess.JobID, taskIdx,
			hex.EncodeToString(rxBytes), rParity,
			hex.EncodeToString(gpxBytes),
			hex.EncodeToString(eBytes),
			signerIDs)
		for _, s := range shares {
			sBytes := make([]byte, 32)
			s.Share.FillBytes(sBytes)
			lambdaHex := "nil"
			if diagLambdas[s.SignerID] != nil {
				lb := make([]byte, 32)
				diagLambdas[s.SignerID].FillBytes(lb)
				lambdaHex = hex.EncodeToString(lb)
			}
			c.logInfo("[Coordinator][sign-diag] job=%s task=%d signer=%d lambda=%s share=%s",
				sess.JobID, taskIdx, s.SignerID, lambdaHex, hex.EncodeToString(sBytes))
		}

		sig, err := roastExec.AggregateSignatures(R, shares)
		if err != nil {
			logs.Warn("[Coordinator] task %d: failed to aggregate signatures: %v", taskIdx, err)
			continue
		}
		// ===== 诊断：coordinator 端 z*G vs R_even + e*P =====
		{
			grp := curve.NewSecp256k1Group()
			// 聚合 z
			zTotal := big.NewInt(0)
			for _, s := range shares {
				zTotal.Add(zTotal, s.Share)
			}
			zTotal.Mod(zTotal, grp.Order())
			// z*G
			zG := grp.ScalarBaseMult(zTotal)
			// R_even (BIP340: lift_x with even Y)
			Reven := curve.Point{X: new(big.Int).Set(R.X), Y: new(big.Int).Set(R.Y)}
			if Reven.Y.Bit(0) == 1 {
				fieldP, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F", 16)
				Reven.Y = new(big.Int).Sub(fieldP, Reven.Y)
			}
			// e*P
			gpb, _ := c.vaultProvider.VaultGroupPubkey(sess.Chain, sess.VaultID, sess.KeyEpoch)
			var P curve.Point
			if len(gpb) == 33 {
				P = grp.DecompressPoint(gpb)
			} else if len(gpb) >= 32 {
				px := new(big.Int).SetBytes(gpb[:32])
				P = grp.DecompressPoint(grp.SerializePoint(curve.Point{X: px}))
			}
			eP := grp.ScalarMult(P, diagE)
			// R_even + e*P
			expected := grp.Add(Reven, eP)
			match := zG.X.Cmp(expected.X) == 0 && zG.Y.Cmp(expected.Y) == 0
			c.logInfo("[Coordinator][z-verify] job=%s task=%d z_total=%s zG=(%s,%s) R_even=(%s,%s) eP=(%s,%s) expected=(%s,%s) match=%v",
				sess.JobID, taskIdx,
				hex.EncodeToString(bigIntFillBytes32(zTotal)),
				hex.EncodeToString(bigIntFillBytes32(zG.X)), hex.EncodeToString(bigIntFillBytes32(zG.Y)),
				hex.EncodeToString(bigIntFillBytes32(Reven.X)), hex.EncodeToString(bigIntFillBytes32(Reven.Y)),
				hex.EncodeToString(bigIntFillBytes32(eP.X)), hex.EncodeToString(bigIntFillBytes32(eP.Y)),
				hex.EncodeToString(bigIntFillBytes32(expected.X)), hex.EncodeToString(bigIntFillBytes32(expected.Y)),
				match)
			// 分项：Σ nonce contributions (from R)
			c.logInfo("[Coordinator][z-verify] job=%s task=%d R_orig=(%s,%s) R_y_parity=%d P=(%s,%s)",
				sess.JobID, taskIdx,
				hex.EncodeToString(bigIntFillBytes32(R.X)), hex.EncodeToString(bigIntFillBytes32(R.Y)), R.Y.Bit(0),
				hex.EncodeToString(bigIntFillBytes32(P.X)), hex.EncodeToString(bigIntFillBytes32(P.Y)))
		}

		signerAddresses := participantAddressesBySignerIDs(sess, signerIDs)
		c.logInfo("[Coordinator] aggregate input job=%s chain=%s vault=%d epoch=%d task=%d signers=%s message=%s signature=%s",
			sess.JobID, sess.Chain, sess.VaultID, sess.KeyEpoch, taskIdx,
			strings.Join(signerAddresses, ","),
			hex.EncodeToString(msg),
			hex.EncodeToString(sig))
		valid, verifyErr, panicVal := c.verifyAggregatedSignature(sess, msg, sig)
		if panicVal != nil {
			c.logWarn("[Coordinator] task %d: aggregated signature panic during verify (job=%s signers=%s message=%s sig=%s panic=%v)",
				taskIdx, sess.JobID, strings.Join(signerAddresses, ","), hex.EncodeToString(msg), hex.EncodeToString(sig), panicVal)
			c.logWarn("[Coordinator] task %d: session dump during verify panic job=%s sess=%s",
				taskIdx, sess.JobID, formatSessionDump(sess))
			c.logVerifyPanicDiagnostics(sess, taskIdx, msg, sig, signerIDs)
			continue
		}
		if verifyErr != nil {
			c.logWarn("[Coordinator] task %d: aggregated signature verify error (job=%s signers=%s message=%s sig=%s err=%v)",
				taskIdx, sess.JobID, strings.Join(signerAddresses, ","), hex.EncodeToString(msg), hex.EncodeToString(sig), verifyErr)
			continue
		}
		if !valid {
			c.logWarn("[Coordinator] task %d: aggregated signature invalid (job=%s signers=%s message=%s sig=%s)",
				taskIdx, sess.JobID, strings.Join(signerAddresses, ","), hex.EncodeToString(msg), hex.EncodeToString(sig))
			continue
		}

		// 该 task 已完成
		signatures[taskIdx] = sig
		completedTasks[taskIdx] = true
		c.logInfo("[Coordinator] task %d completed job=%s signers=%s signature=%s",
			taskIdx, sess.JobID, strings.Join(signerAddresses, ","), hex.EncodeToString(sig))
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

func (c *Coordinator) verifyAggregatedSignature(sess *roastsession.Session, msg, sig []byte) (valid bool, err error, panicVal any) {
	if sess == nil {
		return false, errors.New("nil session"), nil
	}
	if c.vaultProvider == nil {
		return true, nil, nil
	}

	signAlgo := pb.SignAlgo(sess.SignAlgo)
	groupPubkey, err := c.vaultProvider.VaultGroupPubkey(sess.Chain, sess.VaultID, sess.KeyEpoch)
	if err != nil {
		return false, fmt.Errorf("load group pubkey failed: %w", err), nil
	}
	verifyPubkey := groupPubkey
	if signAlgo == pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340 {
		xOnly, normalizeErr := normalizeXOnlyPubKeyForVerify(groupPubkey)
		if normalizeErr != nil {
			return false, normalizeErr, nil
		}
		verifyPubkey = xOnly
	}
	c.logInfo("[Coordinator] verify aggregated signature input job=%s chain=%s vault=%d epoch=%d sign_algo=%s group_pubkey=%s verify_pubkey=%s message=%s signature=%s",
		sess.JobID, sess.Chain, sess.VaultID, sess.KeyEpoch, signAlgo.String(),
		hex.EncodeToString(groupPubkey),
		hex.EncodeToString(verifyPubkey),
		hex.EncodeToString(msg),
		hex.EncodeToString(sig))

	defer func() {
		if r := recover(); r != nil {
			panicVal = r
		}
	}()
	valid, err = frost.Verify(signAlgo, verifyPubkey, msg, sig)
	c.logInfo("[Coordinator] verify aggregated signature result job=%s chain=%s vault=%d epoch=%d valid=%t err=%v",
		sess.JobID, sess.Chain, sess.VaultID, sess.KeyEpoch, valid, err)
	return valid, err, nil
}

func normalizeXOnlyPubKeyForVerify(pubkey []byte) ([]byte, error) {
	if len(pubkey) == 32 {
		return append([]byte(nil), pubkey...), nil
	}
	if len(pubkey) != 33 {
		return nil, fmt.Errorf("invalid secp256k1 pubkey length: %d", len(pubkey))
	}
	if pubkey[0] != 0x02 && pubkey[0] != 0x03 {
		return nil, fmt.Errorf("invalid compressed secp256k1 prefix: 0x%02x", pubkey[0])
	}
	return append([]byte(nil), pubkey[1:]...), nil
}

func shortBytes(data []byte, n int) []byte {
	if len(data) <= n || n <= 0 {
		return data
	}
	return data[:n]
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
	shareHexList := make([]string, 0, len(shares))
	for _, share := range shares {
		shareHexList = append(shareHexList, hex.EncodeToString(share))
	}
	signerAddr := string(env.From)
	if participantIndex >= 0 && participantIndex < len(sess.Committee) {
		if sess.Committee[participantIndex].ID != "" {
			signerAddr = sess.Committee[participantIndex].ID
		}
	}
	c.logInfo("[Coordinator] received signature share job=%s chain=%s vault=%d epoch=%d signer=%s participant_index=%d shares=%s",
		env.SessionID, env.Chain, env.VaultID, env.Epoch, signerAddr, participantIndex+1, strings.Join(shareHexList, ","))

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

func (c *Coordinator) logInfo(format string, args ...any) {
	if c != nil && c.Logger != nil {
		c.Logger.Info(format, args...)
		return
	}
	logs.Info(format, args...)
}

func (c *Coordinator) logWarn(format string, args ...any) {
	if c != nil && c.Logger != nil {
		c.Logger.Warn(format, args...)
		return
	}
	logs.Warn(format, args...)
}

func formatSignerInfoList(committee []SignerInfo) string {
	if len(committee) == 0 {
		return "[]"
	}
	members := make([]string, 0, len(committee))
	for i, member := range committee {
		memberID := string(member.ID)
		if memberID == "" {
			memberID = "unknown"
		}
		members = append(members, fmt.Sprintf("%d:%s", i+1, memberID))
	}
	return "[" + strings.Join(members, ",") + "]"
}

func participantAddressesBySignerIDs(sess *roastsession.Session, signerIDs []int) []string {
	if len(signerIDs) == 0 {
		return nil
	}
	addresses := make([]string, 0, len(signerIDs))
	for _, signerID := range signerIDs {
		addr := fmt.Sprintf("signer#%d", signerID)
		participantIndex := signerID - 1
		if sess != nil && participantIndex >= 0 && participantIndex < len(sess.Committee) {
			if sess.Committee[participantIndex].ID != "" {
				addr = sess.Committee[participantIndex].ID
			}
		}
		addresses = append(addresses, addr)
	}
	return addresses
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

func formatSessionDump(sess *roastsession.Session) string {
	if sess == nil {
		return "<nil>"
	}

	signAlgo := pb.SignAlgo(sess.SignAlgo)
	selectedSet := sess.SelectedSetSnapshot()
	indexes := collectSessionParticipantIndexes(sess, selectedSet)
	committeeDump := make([]string, 0, len(sess.Committee))
	for i, participant := range sess.Committee {
		committeeDump = append(committeeDump,
			fmt.Sprintf("{index=%d id=%q address=%q ip=%q}", i, participant.ID, participant.Address, participant.IP))
	}

	nonceDump := make([]string, 0, len(indexes))
	shareDump := make([]string, 0, len(indexes))
	for _, idx := range indexes {
		label := formatSessionParticipantLabel(sess, idx)
		if nonce, ok := sess.GetNonce(idx); ok {
			nonceDump = append(nonceDump,
				fmt.Sprintf("{participant=%s received_at=%s hiding=%s binding=%s}",
					label,
					formatTimeForLog(nonce.ReceivedAt),
					formatByteMatrixHex(nonce.HidingNonces),
					formatByteMatrixHex(nonce.BindingNonces)))
		} else {
			nonceDump = append(nonceDump, fmt.Sprintf("{participant=%s missing=true}", label))
		}

		if share, ok := sess.GetShare(idx); ok {
			shareDump = append(shareDump,
				fmt.Sprintf("{participant=%s received_at=%s shares=%s}",
					label,
					formatTimeForLog(share.ReceivedAt),
					formatByteMatrixHex(share.Shares)))
		} else {
			shareDump = append(shareDump, fmt.Sprintf("{participant=%s missing=true}", label))
		}
	}

	return fmt.Sprintf("{job_id=%q vault_id=%d chain=%q key_epoch=%d sign_algo=%s threshold=%d my_index=%d state=%s retry_count=%d start_height=%d started_at=%s completed_at=%s selected_set=%v messages=%s committee=%s nonces=%s shares=%s final_signatures=%s}",
		sess.JobID,
		sess.VaultID,
		sess.Chain,
		sess.KeyEpoch,
		signAlgo.String(),
		sess.Threshold,
		sess.MyIndex,
		sess.GetState().String(),
		sess.RetryCount,
		sess.StartHeight,
		formatTimeForLog(sess.StartedAt),
		formatTimeForLog(sess.CompletedAt),
		selectedSet,
		formatByteMatrixHex(sess.Messages),
		"["+strings.Join(committeeDump, ",")+"]",
		"["+strings.Join(nonceDump, ",")+"]",
		"["+strings.Join(shareDump, ",")+"]",
		formatByteMatrixHex(sess.GetFinalSignatures()),
	)
}

func collectSessionParticipantIndexes(sess *roastsession.Session, selectedSet []int) []int {
	if sess == nil {
		return nil
	}
	indexes := make([]int, 0, len(sess.Committee)+len(selectedSet))
	seen := make(map[int]struct{}, len(sess.Committee)+len(selectedSet))
	for i := range sess.Committee {
		indexes = append(indexes, i)
		seen[i] = struct{}{}
	}
	for _, idx := range selectedSet {
		if _, ok := seen[idx]; ok {
			continue
		}
		indexes = append(indexes, idx)
		seen[idx] = struct{}{}
	}
	return indexes
}

func formatSessionParticipantLabel(sess *roastsession.Session, idx int) string {
	if sess != nil && idx >= 0 && idx < len(sess.Committee) {
		id := sess.Committee[idx].ID
		if id != "" {
			return fmt.Sprintf("%d(%s)", idx, id)
		}
	}
	return fmt.Sprintf("%d", idx)
}

func formatByteMatrixHex(matrix [][]byte) string {
	if len(matrix) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(matrix))
	for _, data := range matrix {
		parts = append(parts, hex.EncodeToString(data))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func formatTimeForLog(t time.Time) string {
	if t.IsZero() {
		return "zero"
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (c *Coordinator) logVerifyPanicDiagnostics(sess *roastsession.Session, taskIdx int, msg, sig []byte, shareSignerIDs []int) {
	if c == nil || sess == nil {
		return
	}

	signAlgo := pb.SignAlgo(sess.SignAlgo)
	selectedSet := sess.SelectedSetSnapshot()
	expectedSignerIDs := make([]int, 0, len(selectedSet))
	nonceSignerIDs := make([]int, 0, len(selectedSet))

	nonceFPGroups := make(map[string][]int)
	shareFPGroups := make(map[string][]int)
	for _, idx := range selectedSet {
		signerID := idx + 1
		expectedSignerIDs = append(expectedSignerIDs, signerID)

		if nonce, ok := sess.GetNonce(idx); ok && taskIdx < len(nonce.HidingNonces) && taskIdx < len(nonce.BindingNonces) {
			nonceSignerIDs = append(nonceSignerIDs, signerID)
			nonceFP := fmt.Sprintf("h=%s,b=%s",
				dataFingerprintForLog(nonce.HidingNonces[taskIdx]),
				dataFingerprintForLog(nonce.BindingNonces[taskIdx]))
			nonceFPGroups[nonceFP] = append(nonceFPGroups[nonceFP], signerID)
		}
		if share, ok := sess.GetShare(idx); ok && taskIdx < len(share.Shares) {
			shareFP := dataFingerprintForLog(share.Shares[taskIdx])
			shareFPGroups[shareFP] = append(shareFPGroups[shareFP], signerID)
		}
	}

	c.logWarn("[Coordinator][panic-diagnose] task=%d job=%s chain=%s vault=%d epoch=%d sign_algo=%s threshold=%d selected_set=%v expected_signer_ids=%v nonce_signer_ids=%v share_signer_ids=%v msg_fp=%s sig_fp=%s",
		taskIdx, sess.JobID, sess.Chain, sess.VaultID, sess.KeyEpoch, signAlgo.String(), sess.Threshold, selectedSet,
		expectedSignerIDs, nonceSignerIDs, shareSignerIDs, dataFingerprintForLog(msg), dataFingerprintForLog(sig))

	aggNoncePayload := c.aggregateNonces(sess)
	payloadSignerIDs, nonceBlob, hasHeader := decodeAggregatedNoncePayload(aggNoncePayload)
	legacyPosSignerIDs := make([]int, len(selectedSet))
	for i := range selectedSet {
		legacyPosSignerIDs[i] = i + 1
	}
	c.logWarn("[Coordinator][panic-diagnose] nonce payload header=%t payload_signer_ids=%v legacy_pos_signer_ids=%v payload_fp=%s nonce_blob_fp=%s",
		hasHeader, payloadSignerIDs, legacyPosSignerIDs, dataFingerprintForLog(aggNoncePayload), dataFingerprintForLog(nonceBlob))

	missingShareSignerIDs, extraShareSignerIDs := diffIntSet(expectedSignerIDs, shareSignerIDs)
	if len(missingShareSignerIDs) > 0 || len(extraShareSignerIDs) > 0 {
		c.logWarn("[Coordinator][panic-diagnose] signer mismatch expected_vs_share missing=%v extra=%v",
			missingShareSignerIDs, extraShareSignerIDs)
	}

	for shareFP, signerIDs := range shareFPGroups {
		if len(signerIDs) <= 1 {
			continue
		}
		c.logWarn("[Coordinator][panic-diagnose] duplicate share fingerprint task=%d share_fp=%s signer_ids=%v (possible wrong share source/mapping)",
			taskIdx, shareFP, signerIDs)
	}
	for nonceFP, signerIDs := range nonceFPGroups {
		if len(signerIDs) <= 1 {
			continue
		}
		c.logWarn("[Coordinator][panic-diagnose] duplicate nonce fingerprint task=%d nonce_fp=%s signer_ids=%v (possible nonce/mapping issue)",
			taskIdx, nonceFP, signerIDs)
	}

	selectedPosSignerID := make(map[int]int, len(selectedSet))
	for pos, idx := range selectedSet {
		selectedPosSignerID[idx+1] = pos + 1
	}
	for _, signerID := range shareSignerIDs {
		participantIndex := signerID - 1
		participantID := formatSessionParticipantLabel(sess, participantIndex)
		posID, inSelected := selectedPosSignerID[signerID]

		nonceSummary := "missing"
		if nonce, ok := sess.GetNonce(participantIndex); ok {
			nonceSummary = fmt.Sprintf("tasks_h=%d tasks_b=%d", len(nonce.HidingNonces), len(nonce.BindingNonces))
			if taskIdx < len(nonce.HidingNonces) && taskIdx < len(nonce.BindingNonces) {
				nonceSummary = fmt.Sprintf("%s task_h_fp=%s task_b_fp=%s",
					nonceSummary, dataFingerprintForLog(nonce.HidingNonces[taskIdx]), dataFingerprintForLog(nonce.BindingNonces[taskIdx]))
			} else {
				nonceSummary = nonceSummary + " task_missing=true"
			}
		}

		shareSummary := "missing"
		if share, ok := sess.GetShare(participantIndex); ok {
			shareSummary = fmt.Sprintf("tasks=%d", len(share.Shares))
			if taskIdx < len(share.Shares) {
				shareSummary = fmt.Sprintf("%s task_share_fp=%s", shareSummary, dataFingerprintForLog(share.Shares[taskIdx]))
			} else {
				shareSummary = shareSummary + " task_missing=true"
			}
		}

		c.logWarn("[Coordinator][panic-diagnose] signer detail signer_id=%d selected_pos_id=%d in_selected=%t participant=%s nonce={%s} share={%s}",
			signerID, posID, inSelected, participantID, nonceSummary, shareSummary)
	}

	if c.vaultProvider == nil {
		c.logWarn("[Coordinator][panic-diagnose] vault provider is nil, committee/group_pubkey cross-check skipped")
		return
	}

	chainCommittee, err := c.vaultProvider.VaultCommittee(sess.Chain, sess.VaultID, sess.KeyEpoch)
	if err != nil {
		c.logWarn("[Coordinator][panic-diagnose] load committee failed chain=%s vault=%d epoch=%d err=%v",
			sess.Chain, sess.VaultID, sess.KeyEpoch, err)
	} else {
		sessionCommitteeIDs := committeeIDsFromSession(sess.Committee)
		chainCommitteeIDs := committeeIDsFromSignerInfo(chainCommittee)
		onlySession, onlyChain := diffStringSet(sessionCommitteeIDs, chainCommitteeIDs)
		c.logWarn("[Coordinator][panic-diagnose] committee compare epoch=%d same_order=%t same_set=%t session_committee=%s chain_committee=%s only_session=%s only_chain=%s",
			sess.KeyEpoch,
			sameStringSlice(sessionCommitteeIDs, chainCommitteeIDs),
			len(onlySession) == 0 && len(onlyChain) == 0,
			formatStringSliceForLog(sessionCommitteeIDs),
			formatStringSliceForLog(chainCommitteeIDs),
			formatStringSliceForLog(onlySession),
			formatStringSliceForLog(onlyChain))
	}

	for _, epoch := range diagnosticEpochCandidates(sess.KeyEpoch) {
		groupPubkey, pubErr := c.vaultProvider.VaultGroupPubkey(sess.Chain, sess.VaultID, epoch)
		if pubErr != nil {
			c.logWarn("[Coordinator][panic-diagnose] epoch verify skipped epoch=%d load group pubkey failed: %v", epoch, pubErr)
			continue
		}
		valid, verifyErr, panicVal, verifyPubkey := verifyWithGroupPubkey(signAlgo, groupPubkey, msg, sig)
		c.logWarn("[Coordinator][panic-diagnose] epoch verify epoch=%d group_pubkey_fp=%s verify_pubkey_fp=%s valid=%t err=%v panic=%v",
			epoch,
			dataFingerprintForLog(groupPubkey),
			dataFingerprintForLog(verifyPubkey),
			valid, verifyErr, panicVal)
	}
}

func verifyWithGroupPubkey(signAlgo pb.SignAlgo, groupPubkey, msg, sig []byte) (valid bool, err error, panicVal any, verifyPubkey []byte) {
	verifyPubkey = append([]byte(nil), groupPubkey...)
	if signAlgo == pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340 {
		xOnly, normalizeErr := normalizeXOnlyPubKeyForVerify(groupPubkey)
		if normalizeErr != nil {
			return false, normalizeErr, nil, nil
		}
		verifyPubkey = xOnly
	}

	defer func() {
		if r := recover(); r != nil {
			panicVal = r
		}
	}()

	valid, err = frost.Verify(signAlgo, verifyPubkey, msg, sig)
	return valid, err, nil, verifyPubkey
}

func diagnosticEpochCandidates(epoch uint64) []uint64 {
	candidates := make([]uint64, 0, 3)
	if epoch > 0 {
		candidates = append(candidates, epoch-1)
	}
	candidates = append(candidates, epoch)
	if epoch < ^uint64(0) {
		candidates = append(candidates, epoch+1)
	}
	return candidates
}

func committeeIDsFromSession(committee []roastsession.Participant) []string {
	ids := make([]string, 0, len(committee))
	for i, member := range committee {
		if member.ID == "" {
			ids = append(ids, fmt.Sprintf("<empty@%d>", i))
			continue
		}
		ids = append(ids, member.ID)
	}
	return ids
}

func committeeIDsFromSignerInfo(committee []SignerInfo) []string {
	ids := make([]string, 0, len(committee))
	for i, member := range committee {
		memberID := strings.TrimSpace(string(member.ID))
		if memberID == "" {
			ids = append(ids, fmt.Sprintf("<empty@%d>", i))
			continue
		}
		ids = append(ids, memberID)
	}
	return ids
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func diffStringSet(expected, actual []string) (missing []string, extra []string) {
	expectedSet := make(map[string]struct{}, len(expected))
	actualSet := make(map[string]struct{}, len(actual))
	for _, item := range expected {
		expectedSet[item] = struct{}{}
	}
	for _, item := range actual {
		actualSet[item] = struct{}{}
	}
	for item := range expectedSet {
		if _, ok := actualSet[item]; !ok {
			missing = append(missing, item)
		}
	}
	for item := range actualSet {
		if _, ok := expectedSet[item]; !ok {
			extra = append(extra, item)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

func diffIntSet(expected, actual []int) (missing []int, extra []int) {
	expectedSet := make(map[int]struct{}, len(expected))
	actualSet := make(map[int]struct{}, len(actual))
	for _, item := range expected {
		expectedSet[item] = struct{}{}
	}
	for _, item := range actual {
		actualSet[item] = struct{}{}
	}
	for item := range expectedSet {
		if _, ok := actualSet[item]; !ok {
			missing = append(missing, item)
		}
	}
	for item := range actualSet {
		if _, ok := expectedSet[item]; !ok {
			extra = append(extra, item)
		}
	}
	sort.Ints(missing)
	sort.Ints(extra)
	return missing, extra
}

func formatStringSliceForLog(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	return "[" + strings.Join(items, ",") + "]"
}

func dataFingerprintForLog(data []byte) string {
	if len(data) == 0 {
		return "len=0"
	}
	sum := sha256.Sum256(data)
	prefixLen := 8
	if len(data) < prefixLen {
		prefixLen = len(data)
	}
	return fmt.Sprintf("len=%d,prefix=%s,sha256=%s", len(data), hex.EncodeToString(data[:prefixLen]), hex.EncodeToString(sum[:8]))
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
