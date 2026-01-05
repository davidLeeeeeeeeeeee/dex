// frost/runtime/transition_worker.go
// TransitionWorker: 处理 Vault DKG 轮换的运行时组件

package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"dex/frost/core/curve"
	"dex/frost/core/dkg"
	"dex/frost/security"
	"dex/logs"
	"dex/pb"
	"encoding/binary"
	"fmt"
	"math/big"
	"sync"
	"time"
)

// TransitionWorker 处理 Vault DKG 轮换
type TransitionWorker struct {
	mu sync.RWMutex

	stateReader    StateReader
	txSubmitter    TxSubmitter
	pubKeyProvider MinerPubKeyProvider
	localAddress   string

	// DKG 会话管理
	sessions map[string]*DKGSession // sessionID -> session

	// 配置
	commitTimeout time.Duration
	shareTimeout  time.Duration
	signTimeout   time.Duration
}

// DKGSession 单个 DKG 会话状态
type DKGSession struct {
	Chain     string
	VaultID   uint32
	EpochID   uint64
	SessionID string
	SignAlgo  pb.SignAlgo

	// DKG 密钥材料
	Polynomial  *dkg.Polynomial  // 本地多项式
	LocalShare  *big.Int         // 本地 share = Σ f_j(my_index)
	LocalShares map[int]*big.Int // 发送给各接收者的 share f(receiver_index)
	EncRands    map[int][]byte   // 加密随机数（用于 reveal）

	// 输出
	LocalShareBytes []byte // 本地 share（序列化后）
	GroupPubkey     []byte
	Commitments     map[string][]byte // miner -> commitment points
	ReceivedShrs    map[string][]byte // dealer -> ciphertext

	// 委员会信息
	MyIndex   int      // 本节点在委员会中的索引
	Committee []string // 委员会成员列表
	Threshold int      // 门限 t

	// 状态
	Phase     string // COMMITTING | SHARING | RESOLVING | KEY_READY
	CreatedAt time.Time
}

// NewTransitionWorker 创建 TransitionWorker
func NewTransitionWorker(stateReader StateReader, txSubmitter TxSubmitter, pubKeyProvider MinerPubKeyProvider, localAddr string) *TransitionWorker {
	return &TransitionWorker{
		stateReader:    stateReader,
		txSubmitter:    txSubmitter,
		pubKeyProvider: pubKeyProvider,
		localAddress:   localAddr,
		sessions:       make(map[string]*DKGSession),
		commitTimeout:  30 * time.Second,
		shareTimeout:   30 * time.Second,
		signTimeout:    60 * time.Second,
	}
}

// StartSession 启动新的 DKG 会话
func (w *TransitionWorker) StartSession(ctx context.Context, chain string, vaultID uint32, epochID uint64, signAlgo pb.SignAlgo) error {
	sessionID := fmt.Sprintf("%s_%d_%d", chain, vaultID, epochID)

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, exists := w.sessions[sessionID]; exists {
		logs.Debug("[TransitionWorker] session %s already exists", sessionID)
		return nil // 幂等
	}

	session := &DKGSession{
		Chain:        chain,
		VaultID:      vaultID,
		EpochID:      epochID,
		SessionID:    sessionID,
		SignAlgo:     signAlgo,
		Commitments:  make(map[string][]byte),
		ReceivedShrs: make(map[string][]byte),
		Phase:        "COMMITTING",
		CreatedAt:    time.Now(),
	}

	w.sessions[sessionID] = session
	logs.Info("[TransitionWorker] started DKG session %s", sessionID)

	// 异步执行 DKG 流程
	go w.runSession(ctx, session)

	return nil
}

// runSession 运行 DKG 会话
func (w *TransitionWorker) runSession(ctx context.Context, session *DKGSession) {
	logs.Debug("[TransitionWorker] runSession %s phase=%s", session.SessionID, session.Phase)

	// Phase 1: 提交 commitment
	if err := w.submitCommitment(ctx, session); err != nil {
		logs.Error("[TransitionWorker] submitCommitment failed: %v", err)
		return
	}

	// Phase 2: 等待所有 commitment 并提交 share
	if err := w.submitShares(ctx, session); err != nil {
		logs.Error("[TransitionWorker] submitShares failed: %v", err)
		return
	}

	// Phase 3: 收集 share 并生成密钥
	if err := w.generateKey(ctx, session); err != nil {
		logs.Error("[TransitionWorker] generateKey failed: %v", err)
		return
	}

	// Phase 4: 提交验证签名
	if err := w.submitValidation(ctx, session); err != nil {
		logs.Error("[TransitionWorker] submitValidation failed: %v", err)
		return
	}

	logs.Info("[TransitionWorker] DKG session %s completed", session.SessionID)
}

// submitCommitment 提交 DKG 承诺
func (w *TransitionWorker) submitCommitment(ctx context.Context, session *DKGSession) error {
	logs.Debug("[TransitionWorker] submitCommitment session=%s", session.SessionID)

	group := curve.NewSecp256k1Group()

	// 生成随机 t-1 阶多项式 f(x) = a_0 + a_1*x + ... + a_(t-1)*x^(t-1)
	// a_0 是本节点的秘密贡献
	poly := dkg.NewPolynomial(session.Threshold, group)
	session.Polynomial = poly

	// 计算 Feldman VSS 承诺点：A_ik = g^{a_k}
	commitmentPoints := make([][]byte, len(poly.Coefficients))
	for k, coef := range poly.Coefficients {
		point := group.ScalarBaseMult(coef)
		// 序列化点（压缩格式，33 字节）
		commitmentPoints[k] = serializePoint(point)
	}

	// AI0 = A_i0 = g^{a_0}（第一个承诺点）
	ai0 := commitmentPoints[0]

	// 预计算发送给每个接收者的 share：f(receiver_index)
	session.LocalShares = make(map[int]*big.Int)
	session.EncRands = make(map[int][]byte)
	for idx := 1; idx <= len(session.Committee); idx++ {
		x := big.NewInt(int64(idx))
		share := poly.Evaluate(x, group)
		session.LocalShares[idx] = share

		// 生成加密随机数（用于 ECIES 加密和潜在的 reveal）
		encRand := make([]byte, 32)
		if _, err := rand.Read(encRand); err != nil {
			return fmt.Errorf("generate enc_rand: %w", err)
		}
		session.EncRands[idx] = encRand
	}

	tx := &pb.FrostVaultDkgCommitTx{
		Chain:            session.Chain,
		VaultId:          session.VaultID,
		EpochId:          session.EpochID,
		SignAlgo:         session.SignAlgo,
		CommitmentPoints: commitmentPoints,
		AI0:              ai0,
	}

	if err := w.txSubmitter.SubmitDkgCommitTx(ctx, tx); err != nil {
		return fmt.Errorf("submit commitment: %w", err)
	}

	session.Phase = "SHARING"
	return nil
}

// serializePoint 序列化椭圆曲线点为压缩格式
func serializePoint(p curve.Point) []byte {
	// 压缩格式：1 字节前缀 + 32 字节 X 坐标
	// 前缀：0x02 (Y 为偶数) 或 0x03 (Y 为奇数)
	result := make([]byte, 33)
	if p.Y.Bit(0) == 0 {
		result[0] = 0x02
	} else {
		result[0] = 0x03
	}
	p.X.FillBytes(result[1:])
	return result
}

// submitShares 提交 DKG shares
func (w *TransitionWorker) submitShares(ctx context.Context, session *DKGSession) error {
	logs.Debug("[TransitionWorker] submitShares session=%s", session.SessionID)

	// 为每个委员会成员提交加密的 share
	for idx, receiverID := range session.Committee {
		receiverIndex := idx + 1 // 1-based index

		// 获取接收者公钥
		receiverPubKey, err := w.pubKeyProvider.GetMinerSigningPubKey(receiverID, session.SignAlgo)
		if err != nil {
			logs.Warn("[TransitionWorker] cannot get pubkey for %s: %v", receiverID, err)
			continue
		}

		// 获取预计算的 share 和加密随机数
		share := session.LocalShares[receiverIndex]
		encRand := session.EncRands[receiverIndex]

		// 序列化 share 为 32 字节
		shareBytes := make([]byte, 32)
		share.FillBytes(shareBytes)

		// ECIES 加密
		ciphertext, err := security.ECIESEncrypt(receiverPubKey, shareBytes, encRand)
		if err != nil {
			return fmt.Errorf("encrypt share for %s: %w", receiverID, err)
		}

		tx := &pb.FrostVaultDkgShareTx{
			Chain:      session.Chain,
			VaultId:    session.VaultID,
			EpochId:    session.EpochID,
			DealerId:   w.localAddress,
			ReceiverId: receiverID,
			Ciphertext: ciphertext,
		}

		if err := w.txSubmitter.SubmitDkgShareTx(ctx, tx); err != nil {
			return fmt.Errorf("submit share to %s: %w", receiverID, err)
		}
	}

	session.Phase = "RESOLVING"
	return nil
}

// generateKey 收集 shares 并生成本地密钥
func (w *TransitionWorker) generateKey(ctx context.Context, session *DKGSession) error {
	logs.Debug("[TransitionWorker] generateKey session=%s", session.SessionID)

	group := curve.NewSecp256k1Group()

	// 本地 share = 自己多项式在自己索引处的值 + 收到的所有 shares
	// s_i = Σ_j f_j(i)
	myX := big.NewInt(int64(session.MyIndex))
	localShare := session.Polynomial.Evaluate(myX, group)

	// 注意：在完整实现中，需要：
	// 1. 等待收集所有 dealers 发送的加密 shares
	// 2. 解密每个 share
	// 3. 验证 share 与 commitment 一致
	// 4. 累加所有 shares
	// 这里简化处理，假设已收集完成

	session.LocalShare = localShare
	session.LocalShareBytes = make([]byte, 32)
	localShare.FillBytes(session.LocalShareBytes)

	// 计算 group_pubkey = Σ A_j0（所有 dealers 的第一个承诺点之和）
	// 在完整实现中需要从链上收集所有 commitments
	// 这里使用自己的 A_0 作为示例
	groupPubkey := group.ScalarBaseMult(session.Polynomial.Coefficients[0])
	session.GroupPubkey = serializePoint(groupPubkey)

	session.Phase = "KEY_READY"
	logs.Info("[TransitionWorker] generateKey completed: localShare=%x groupPubkey=%x",
		session.LocalShareBytes[:8], session.GroupPubkey[:8])
	return nil
}

// submitValidation 提交验证签名
func (w *TransitionWorker) submitValidation(ctx context.Context, session *DKGSession) error {
	logs.Debug("[TransitionWorker] submitValidation session=%s", session.SessionID)

	// 构造验证消息：chain || vault_id || epoch_id || group_pubkey
	msgHash := computeValidationMsgHash(session.Chain, session.VaultID, session.EpochID, session.GroupPubkey)

	// 使用本地 share 生成部分签名
	// 注意：这里需要 FROST 签名协议，简化处理使用 Schnorr 签名
	group := curve.NewSecp256k1Group()
	signature := computeSchnorrSignature(session.LocalShare, msgHash, group)

	tx := &pb.FrostVaultDkgValidationSignedTx{
		Chain:          session.Chain,
		VaultId:        session.VaultID,
		EpochId:        session.EpochID,
		MsgHash:        msgHash,
		NewGroupPubkey: session.GroupPubkey,
		Signature:      signature,
	}

	if err := w.txSubmitter.SubmitDkgValidationSignedTx(ctx, tx); err != nil {
		return fmt.Errorf("submit validation: %w", err)
	}

	return nil
}

// computeValidationMsgHash 计算验证消息哈希
func computeValidationMsgHash(chain string, vaultID uint32, epochID uint64, groupPubkey []byte) []byte {
	h := sha256.New()
	h.Write([]byte(chain))
	binary.Write(h, binary.BigEndian, vaultID)
	binary.Write(h, binary.BigEndian, epochID)
	h.Write(groupPubkey)
	return h.Sum(nil)
}

// computeSchnorrSignature 计算 Schnorr 签名（简化版）
func computeSchnorrSignature(privateKey *big.Int, msgHash []byte, group curve.Group) []byte {
	// 生成随机 nonce
	k := dkg.RandomScalar(group.Order())

	// R = g^k
	R := group.ScalarBaseMult(k)

	// e = H(R || P || m)
	P := group.ScalarBaseMult(privateKey)
	eBytes := sha256.Sum256(append(append(serializePoint(R), serializePoint(P)...), msgHash...))
	e := new(big.Int).SetBytes(eBytes[:])
	e.Mod(e, group.Order())

	// s = k + e * x
	s := new(big.Int).Mul(e, privateKey)
	s.Add(s, k)
	s.Mod(s, group.Order())

	// 签名 = R.x || s
	sig := make([]byte, 64)
	R.X.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return sig
}

// GetSession 获取会话状态
func (w *TransitionWorker) GetSession(sessionID string) *DKGSession {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.sessions[sessionID]
}
