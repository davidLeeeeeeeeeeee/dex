// frost/runtime/workers/transition_worker.go
// TransitionWorker: 处理 Vault DKG 轮换的运行时组件

package workers

import (
	"context"
	"crypto/sha256"
	"dex/frost/security"
	"dex/logs"
	"dex/pb"
	"encoding/binary"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
)

// TransitionWorker 处理 Vault DKG 轮换
type TransitionWorker struct {
	mu sync.RWMutex

	stateReader    StateReader
	txSubmitter    TxSubmitter
	pubKeyProvider MinerPubKeyProvider
	cryptoFactory  CryptoExecutorFactory // 密码学执行器工厂
	vaultProvider  VaultCommitteeProvider
	signerProvider SignerSetProvider
	localAddress   string

	// DKG 会话管理
	sessions map[string]*DKGSession // sessionID -> session

	// 配置
	commitTimeout       time.Duration
	shareTimeout        time.Duration
	signTimeout         time.Duration
	transitionThreshold float64 // change_ratio 阈值（默认 0.2）
	ewmaAlpha           float64 // EWMA 平滑系数（默认 0.3）
}

// DKGSession 单个 DKG 会话状态
type DKGSession struct {
	Chain     string
	VaultID   uint32
	EpochID   uint64
	SessionID string
	SignAlgo  pb.SignAlgo

	// DKG 密钥材料（通过接口操作）
	Polynomial  PolynomialHandle // 本地多项式句柄
	LocalShare  *big.Int         // 本地 share = Σ f_j(my_index)
	LocalShares map[int][]byte   // 发送给各接收者的 share f(receiver_index)
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
func NewTransitionWorker(stateReader StateReader, txSubmitter TxSubmitter, pubKeyProvider MinerPubKeyProvider, cryptoFactory CryptoExecutorFactory, vaultProvider VaultCommitteeProvider, signerProvider SignerSetProvider, localAddr string) *TransitionWorker {
	return &TransitionWorker{
		stateReader:         stateReader,
		txSubmitter:         txSubmitter,
		pubKeyProvider:      pubKeyProvider,
		cryptoFactory:       cryptoFactory,
		vaultProvider:       vaultProvider,
		signerProvider:      signerProvider,
		localAddress:        localAddr,
		sessions:            make(map[string]*DKGSession),
		commitTimeout:       30 * time.Second,
		shareTimeout:        30 * time.Second,
		signTimeout:         60 * time.Second,
		transitionThreshold: 0.2, // 默认 20% 变化触发
		ewmaAlpha:           0.3, // EWMA 平滑系数
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

	// 获取 DKG 执行器
	dkgExec, err := w.cryptoFactory.NewDKGExecutor(int32(session.SignAlgo))
	if err != nil {
		return fmt.Errorf("create dkg executor: %w", err)
	}

	// 生成随机 t-1 阶多项式 f(x) = a_0 + a_1*x + ... + a_(t-1)*x^(t-1)
	// a_0 是本节点的秘密贡献
	poly, err := dkgExec.GeneratePolynomial(session.Threshold)
	if err != nil {
		return fmt.Errorf("generate polynomial: %w", err)
	}
	session.Polynomial = poly

	// 计算 Feldman VSS 承诺点：A_ik = g^{a_k}
	commitmentPoints := dkgExec.ComputeCommitments(poly)

	// AI0 = A_i0 = g^{a_0}（第一个承诺点）
	ai0 := commitmentPoints[0]

	// 预计算发送给每个接收者的 share：f(receiver_index)
	session.LocalShares = make(map[int][]byte)
	session.EncRands = make(map[int][]byte)
	for idx := 1; idx <= len(session.Committee); idx++ {
		// 通过接口计算 share
		shareBytes := dkgExec.EvaluateShare(poly, idx)
		session.LocalShares[idx] = shareBytes

		// 生成加密随机数（用于 ECIES 加密和潜在的 reveal）
		encRand := make([]byte, 32)
		// 使用 sha256 哈希生成随机数（实际应用需要更安全的方法）
		hash := sha256.Sum256(append([]byte(session.SessionID), byte(idx)))
		copy(encRand, hash[:])
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
		shareBytes := session.LocalShares[receiverIndex]
		encRand := session.EncRands[receiverIndex]

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

	// 获取 DKG 执行器
	dkgExec, err := w.cryptoFactory.NewDKGExecutor(int32(session.SignAlgo))
	if err != nil {
		return fmt.Errorf("create dkg executor: %w", err)
	}

	// 本地 share = 自己多项式在自己索引处的值 + 收到的所有 shares
	// s_i = Σ_j f_j(i)
	myShareBytes := dkgExec.EvaluateShare(session.Polynomial, session.MyIndex)

	// 注意：在完整实现中，需要：
	// 1. 等待收集所有 dealers 发送的加密 shares
	// 2. 解密每个 share
	// 3. 验证 share 与 commitment 一致
	// 4. 累加所有 shares（使用 dkgExec.AggregateShares）
	// 这里简化处理，假设已收集完成

	session.LocalShare = new(big.Int).SetBytes(myShareBytes)
	session.LocalShareBytes = myShareBytes

	// 计算 group_pubkey = Σ A_j0（所有 dealers 的第一个承诺点之和）
	// 在完整实现中需要从链上收集所有 commitments
	// 这里使用自己的 A_0 作为示例
	coeffs := session.Polynomial.Coefficients()
	if len(coeffs) > 0 {
		a0Pt := dkgExec.ScalarBaseMult(coeffs[0])
		session.GroupPubkey = serializeCurvePoint(a0Pt)
	}

	session.Phase = "KEY_READY"
	logs.Info("[TransitionWorker] generateKey completed: localShare=%x groupPubkey=%x",
		session.LocalShareBytes[:8], session.GroupPubkey[:8])
	return nil
}

// serializeCurvePoint 序列化 CurvePoint 为压缩格式
func serializeCurvePoint(p CurvePoint) []byte {
	result := make([]byte, 33)
	if p.Y.Bit(0) == 0 {
		result[0] = 0x02
	} else {
		result[0] = 0x03
	}
	p.X.FillBytes(result[1:])
	return result
}

// submitValidation 提交验证签名
func (w *TransitionWorker) submitValidation(ctx context.Context, session *DKGSession) error {
	logs.Debug("[TransitionWorker] submitValidation session=%s", session.SessionID)

	// 构造验证消息：chain || vault_id || epoch_id || group_pubkey
	msgHash := computeValidationMsgHash(session.Chain, session.VaultID, session.EpochID, session.GroupPubkey)

	// 获取 DKG 执行器
	dkgExec, err := w.cryptoFactory.NewDKGExecutor(int32(session.SignAlgo))
	if err != nil {
		return fmt.Errorf("create dkg executor: %w", err)
	}

	// 使用本地 share 生成 Schnorr 签名
	signature, err := dkgExec.SchnorrSign(session.LocalShare, msgHash)
	if err != nil {
		return fmt.Errorf("schnorr sign: %w", err)
	}

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

// GetSession 获取会话状态
func (w *TransitionWorker) GetSession(sessionID string) *DKGSession {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.sessions[sessionID]
}

// CheckTriggerConditions 检查各 Vault 的触发条件（按 Vault 独立检测）
// 扫描 Top10000 变化，计算 change_ratio，达到阈值时创建 VaultTransitionState
func (w *TransitionWorker) CheckTriggerConditions(ctx context.Context, height uint64) error {
	logs.Debug("[TransitionWorker] CheckTriggerConditions height=%d", height)

	// 1. 获取当前 Top10000
	currentSigners, err := w.signerProvider.Top10000(height)
	if err != nil {
		return fmt.Errorf("get top10000: %w", err)
	}

	// 2. 遍历所有链和 Vault，检查每个 Vault 的委员会变化
	// 这里简化处理，假设支持的链从配置读取
	chains := []string{"btc", "eth", "bnb"} // TODO: 从配置读取

	for _, chain := range chains {
		// 获取该链的 Vault 配置
		vaultCfgKey := fmt.Sprintf("v1_frost_vault_cfg_%s_0", chain)
		vaultCfgData, exists, err := w.stateReader.Get(vaultCfgKey)
		if err != nil || !exists {
			logs.Debug("[TransitionWorker] vault config not found for chain %s, skip", chain)
			continue
		}

		var vaultCfg pb.FrostVaultConfig
		if err := proto.Unmarshal(vaultCfgData, &vaultCfg); err != nil {
			logs.Warn("[TransitionWorker] failed to unmarshal vault config: %v", err)
			continue
		}

		// 遍历该链的所有 Vault
		for vaultID := uint32(0); vaultID < vaultCfg.VaultCount; vaultID++ {
			if err := w.checkVaultTrigger(ctx, chain, vaultID, height, currentSigners); err != nil {
				logs.Warn("[TransitionWorker] check vault trigger failed: chain=%s vault=%d: %v", chain, vaultID, err)
				// 继续检查其他 Vault
			}
		}
	}

	return nil
}

// checkVaultTrigger 检查单个 Vault 的触发条件
func (w *TransitionWorker) checkVaultTrigger(ctx context.Context, chain string, vaultID uint32, height uint64, currentSigners []SignerInfo) error {
	// 1. 获取当前 Vault 的 epoch
	currentEpoch := w.vaultProvider.VaultCurrentEpoch(chain, vaultID)

	// 2. 获取当前和上一个 epoch 的委员会
	currentCommittee, err := w.vaultProvider.VaultCommittee(chain, vaultID, currentEpoch)
	if err != nil {
		return fmt.Errorf("get current committee: %w", err)
	}

	prevEpoch := currentEpoch
	if currentEpoch > 1 {
		prevEpoch = currentEpoch - 1
	}
	prevCommittee, err := w.vaultProvider.VaultCommittee(chain, vaultID, prevEpoch)
	if err != nil {
		// 如果上一个 epoch 不存在，说明是首次，不需要触发
		return nil
	}

	// 3. 计算 change_ratio（委员会成员变化比例）
	changeRatio := w.calculateChangeRatio(prevCommittee, currentCommittee)

	// 4. 应用 EWMA 平滑（简化实现，实际应该维护历史状态）
	// TODO: 维护每个 Vault 的历史 change_ratio，应用 EWMA

	// 5. 检查是否达到阈值
	if changeRatio >= w.transitionThreshold {
		logs.Info("[TransitionWorker] trigger condition met: chain=%s vault=%d epoch=%d change_ratio=%.2f",
			chain, vaultID, currentEpoch+1, changeRatio)

		// 创建新的 VaultTransitionState
		return w.createTransitionState(ctx, chain, vaultID, currentEpoch+1, height, prevCommittee, currentCommittee)
	}

	return nil
}

// calculateChangeRatio 计算委员会变化比例
func (w *TransitionWorker) calculateChangeRatio(oldCommittee, newCommittee []SignerInfo) float64 {
	if len(oldCommittee) == 0 {
		return 1.0 // 全新委员会，100% 变化
	}

	// 构建旧委员会的地址集合
	oldSet := make(map[string]bool)
	for _, member := range oldCommittee {
		oldSet[string(member.ID)] = true
	}

	// 计算新委员会中不在旧委员会中的成员数量
	changedCount := 0
	for _, member := range newCommittee {
		if !oldSet[string(member.ID)] {
			changedCount++
		}
	}

	// change_ratio = 变化数量 / 总数量
	return float64(changedCount) / float64(len(oldCommittee))
}

// createTransitionState 创建 VaultTransitionState
func (w *TransitionWorker) createTransitionState(ctx context.Context, chain string, vaultID uint32, epochID uint64, triggerHeight uint64, oldCommittee, newCommittee []SignerInfo) error {
	// 检查是否已存在 transition state
	transitionKey := fmt.Sprintf("v1_frost_vault_transition_%s_%d_%s", chain, vaultID, padUint(epochID))
	existing, exists, err := w.stateReader.Get(transitionKey)
	if err != nil {
		return fmt.Errorf("check existing transition: %w", err)
	}
	if exists && len(existing) > 0 {
		logs.Debug("[TransitionWorker] transition state already exists: %s", transitionKey)
		return nil // 已存在，幂等
	}

	// 获取 VaultConfig 以确定 sign_algo 和 threshold
	vaultCfgKey := fmt.Sprintf("v1_frost_vault_cfg_%s_%d", chain, vaultID)
	vaultCfgData, exists, err := w.stateReader.Get(vaultCfgKey)
	if err != nil || !exists {
		return fmt.Errorf("vault config not found")
	}

	var vaultCfg pb.FrostVaultConfig
	if err := proto.Unmarshal(vaultCfgData, &vaultCfg); err != nil {
		return fmt.Errorf("unmarshal vault config: %w", err)
	}

	// 计算门限 t = ceil(K * threshold_ratio)
	threshold := int(float64(len(newCommittee)) * float64(vaultCfg.ThresholdRatio))
	if threshold < 1 {
		threshold = 1
	}

	// 获取旧 group_pubkey
	oldGroupPubkey, err := w.vaultProvider.VaultGroupPubkey(chain, vaultID, epochID-1)
	if err != nil {
		logs.Warn("[TransitionWorker] failed to get old group pubkey: %v", err)
		oldGroupPubkey = nil
	}

	// 构建旧/新委员会地址列表
	oldMembers := make([]string, len(oldCommittee))
	for i, m := range oldCommittee {
		oldMembers[i] = string(m.ID)
	}
	newMembers := make([]string, len(newCommittee))
	for i, m := range newCommittee {
		newMembers[i] = string(m.ID)
	}

	// 创建 transition state
	transition := &pb.VaultTransitionState{
		Chain:               chain,
		VaultId:             vaultID,
		EpochId:             epochID,
		SignAlgo:            vaultCfg.SignAlgo,
		TriggerHeight:       triggerHeight,
		OldCommitteeMembers: oldMembers,
		NewCommitteeMembers: newMembers,
		DkgStatus:           "NOT_STARTED",
		DkgSessionId:        fmt.Sprintf("%s_%d_%d", chain, vaultID, epochID),
		DkgThresholdT:       uint32(threshold),
		DkgN:                uint32(len(newCommittee)),
		DkgCommitDeadline:   triggerHeight + 100, // TODO: 从配置读取
		DkgDisputeDeadline:  triggerHeight + 200, // TODO: 从配置读取
		OldGroupPubkey:      oldGroupPubkey,
		ValidationStatus:    "NOT_STARTED",
		Lifecycle:           "ACTIVE",
	}

	// TODO: 这里应该通过 VM 交易创建 transition state
	// 目前 transition state 的创建应该由 VM 处理，这里只是触发 DKG 会话
	logs.Info("[TransitionWorker] transition state should be created by VM: chain=%s vault=%d epoch=%d (sign_algo=%v)",
		chain, vaultID, epochID, transition.SignAlgo)

	// 启动 DKG 会话
	return w.StartSession(ctx, chain, vaultID, epochID, vaultCfg.SignAlgo)
}

// padUint 将 uint64 格式化为固定长度的字符串（用于 key）
func padUint(n uint64) string {
	return fmt.Sprintf("%020d", n)
}

// PlanMigrationJobs 规划迁移 Job（扫描该 Vault 的资金，生成迁移模板）
func (w *TransitionWorker) PlanMigrationJobs(ctx context.Context, chain string, vaultID uint32, epochID uint64) error {
	logs.Info("[TransitionWorker] PlanMigrationJobs: chain=%s vault=%d epoch=%d", chain, vaultID, epochID)

	// 1. 获取 transition state，确认 DKG 已完成（KEY_READY）
	transitionKey := fmt.Sprintf("v1_frost_vault_transition_%s_%d_%s", chain, vaultID, padUint(epochID))
	transitionData, exists, err := w.stateReader.Get(transitionKey)
	if err != nil {
		return fmt.Errorf("get transition state: %w", err)
	}
	if !exists {
		return fmt.Errorf("transition state not found")
	}

	var transition pb.VaultTransitionState
	if err := proto.Unmarshal(transitionData, &transition); err != nil {
		return fmt.Errorf("unmarshal transition: %w", err)
	}

	if transition.DkgStatus != "KEY_READY" {
		return fmt.Errorf("dkg not ready, status=%s", transition.DkgStatus)
	}

	// 2. 扫描该 Vault 的资金（FundsLedger）
	// 根据链类型选择不同的迁移策略
	switch chain {
	case "btc":
		return w.planBTCMigrationJobs(ctx, chain, vaultID, epochID, &transition)
	case "eth", "bnb", "trx", "sol":
		return w.planContractMigrationJobs(ctx, chain, vaultID, epochID, &transition)
	default:
		return fmt.Errorf("unsupported chain: %s", chain)
	}
}

// planBTCMigrationJobs 规划 BTC 迁移 Job（sweep 交易）
func (w *TransitionWorker) planBTCMigrationJobs(ctx context.Context, chain string, vaultID uint32, epochID uint64, transition *pb.VaultTransitionState) error {
	// 1. 扫描该 Vault 的所有 UTXO
	utxoPrefix := fmt.Sprintf("v1_frost_btc_utxo_%d_", vaultID)
	utxos := make([]struct {
		txid  string
		vout  uint32
		value uint64
	}, 0)

	err := w.stateReader.Scan(utxoPrefix, func(k string, v []byte) bool {
		// 解析 UTXO key: v1_frost_btc_utxo_<vault_id>_<txid>_<vout>
		parts := strings.Split(k, "_")
		if len(parts) < 6 {
			return true // 继续扫描
		}
		txid := parts[4]
		voutStr := parts[5]
		vout, err := strconv.ParseUint(voutStr, 10, 32)
		if err != nil {
			return true
		}

		// 解析 UTXO 值（简化，实际应该从 v 中解析）
		// TODO: 从 v 中解析完整的 UTXO 信息
		utxos = append(utxos, struct {
			txid  string
			vout  uint32
			value uint64
		}{txid: txid, vout: uint32(vout), value: 0}) // value 需要从实际数据解析

		return true
	})
	if err != nil {
		return fmt.Errorf("scan utxos: %w", err)
	}

	if len(utxos) == 0 {
		logs.Info("[TransitionWorker] no UTXOs to migrate for vault %d", vaultID)
		return nil
	}

	// 2. 获取新地址（从新 group_pubkey 派生）
	newGroupPubkey := transition.NewGroupPubkey
	if len(newGroupPubkey) == 0 {
		return fmt.Errorf("new group pubkey not ready")
	}

	// TODO: 从 group_pubkey 派生 BTC 地址
	// newAddress := deriveBTCAddress(newGroupPubkey)

	// 3. 生成迁移交易模板（sweep：所有 UTXO -> 新地址）
	// TODO: 实现完整的 BTC 迁移模板生成
	// 这里需要：
	// - 构建 BTC 交易模板（inputs = 所有 UTXO，outputs = 新地址）
	// - 计算 template_hash
	// - 创建 MigrationJob 记录
	// - 启动 ROAST 签名会话

	logs.Info("[TransitionWorker] planned BTC migration: vault=%d utxos=%d", vaultID, len(utxos))
	return nil
}

// planContractMigrationJobs 规划合约链迁移 Job（updatePubkey）
func (w *TransitionWorker) planContractMigrationJobs(ctx context.Context, chain string, vaultID uint32, epochID uint64, transition *pb.VaultTransitionState) error {
	// 1. 获取新 group_pubkey
	newGroupPubkey := transition.NewGroupPubkey
	if len(newGroupPubkey) == 0 {
		return fmt.Errorf("new group pubkey not ready")
	}

	// 2. 生成 updatePubkey 模板
	// template = updatePubkey(new_pubkey, vault_id, epoch_id, ...)
	// TODO: 实现完整的合约调用模板生成
	// 这里需要：
	// - 构建合约调用 calldata
	// - 计算 template_hash
	// - 创建 MigrationJob 记录
	// - 启动 ROAST 签名会话

	logs.Info("[TransitionWorker] planned contract migration: chain=%s vault=%d epoch=%d", chain, vaultID, epochID)
	return nil
}
