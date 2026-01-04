// frost/runtime/transition_worker.go
// TransitionWorker: 处理 Vault DKG 轮换的运行时组件

package runtime

import (
	"context"
	"dex/logs"
	"dex/pb"
	"fmt"
	"sync"
	"time"
)

// TransitionWorker 处理 Vault DKG 轮换
type TransitionWorker struct {
	mu sync.RWMutex

	stateReader  StateReader
	txSubmitter  TxSubmitter
	localAddress string

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

	// 本地密钥材料
	LocalShareBytes []byte // 本地 share（序列化后）
	GroupPubkey     []byte
	Commitments     map[string][]byte // miner -> commitment
	ReceivedShrs    map[string][]byte // dealer -> ciphertext

	// 状态
	Phase     string // COMMITTING | SHARING | RESOLVING | KEY_READY
	CreatedAt time.Time
}

// NewTransitionWorker 创建 TransitionWorker
func NewTransitionWorker(stateReader StateReader, txSubmitter TxSubmitter, localAddr string) *TransitionWorker {
	return &TransitionWorker{
		stateReader:   stateReader,
		txSubmitter:   txSubmitter,
		localAddress:  localAddr,
		sessions:      make(map[string]*DKGSession),
		commitTimeout: 30 * time.Second,
		shareTimeout:  30 * time.Second,
		signTimeout:   60 * time.Second,
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

	// TODO: 使用 dkg 包生成 commitment
	commitmentPoints := [][]byte{[]byte("dummy_commitment_" + w.localAddress)}
	ai0 := []byte("dummy_ai0_" + w.localAddress)

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

// submitShares 提交 DKG shares
func (w *TransitionWorker) submitShares(ctx context.Context, session *DKGSession) error {
	logs.Debug("[TransitionWorker] submitShares session=%s", session.SessionID)

	// TODO: 等待收集所有 commitment，然后生成并提交 shares
	// 这里简化处理，假设有一个 receiver
	receiverID := "dummy_receiver"
	ciphertext := []byte("encrypted_share_for_" + receiverID)

	tx := &pb.FrostVaultDkgShareTx{
		Chain:      session.Chain,
		VaultId:    session.VaultID,
		EpochId:    session.EpochID,
		DealerId:   w.localAddress,
		ReceiverId: receiverID,
		Ciphertext: ciphertext,
	}

	if err := w.txSubmitter.SubmitDkgShareTx(ctx, tx); err != nil {
		return fmt.Errorf("submit share: %w", err)
	}

	session.Phase = "RESOLVING"
	return nil
}

// generateKey 收集 shares 并生成本地密钥
func (w *TransitionWorker) generateKey(ctx context.Context, session *DKGSession) error {
	logs.Debug("[TransitionWorker] generateKey session=%s", session.SessionID)

	// TODO: 收集所有 shares，解密并组合生成本地 key share
	// 计算 group_pubkey
	session.GroupPubkey = []byte("dummy_group_pubkey_" + session.SessionID)
	session.LocalShareBytes = []byte("dummy_local_share")

	session.Phase = "KEY_READY"
	return nil
}

// submitValidation 提交验证签名
func (w *TransitionWorker) submitValidation(ctx context.Context, session *DKGSession) error {
	logs.Debug("[TransitionWorker] submitValidation session=%s", session.SessionID)

	// TODO: 使用新密钥对 msg_hash 签名
	msgHash := []byte("dummy_msg_hash")
	signature := []byte("dummy_signature")

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

// GetSession 获取会话状态
func (w *TransitionWorker) GetSession(sessionID string) *DKGSession {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.sessions[sessionID]
}
