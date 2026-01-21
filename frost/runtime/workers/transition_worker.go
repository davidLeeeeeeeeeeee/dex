// frost/runtime/workers/transition_worker.go
// TransitionWorker: 处理 Vault DKG 轮换的运行时组件

package workers

import (
	"context"
	"crypto/sha256"
	"dex/frost/security"
	"dex/logs"
	"dex/pb"
	"dex/utils"
	"encoding/binary"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	adapterFactory ChainAdapterFactory // 链适配器工厂
	localAddress   string
	Logger         logs.Logger

	// DKG 会话管理
	sessions map[string]*DKGSession // sessionID -> session

	// 配置
	commitTimeout       time.Duration
	shareTimeout        time.Duration
	signTimeout         time.Duration
	transitionThreshold float64 // change_ratio 阈值（默认 0.2）
	ewmaAlpha           float64 // EWMA 平滑系数（默认 0.3）
	epochBlocks         uint64  // Epoch 边界区块数（默认 40000）

	// EWMA 状态维护（按 Vault 独立）
	// key: chain_vaultID, value: ewmaValue
	ewmaHistory map[string]float64
}

var dkgTxCounter uint64

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
	MyIndex          int               // 本节点在委员会中的索引
	Committee        []string          // 委员会成员列表
	CommitteePubKeys map[string][]byte // 委员会成员公钥
	Threshold        int               // 门限 t

	// 状态
	Phase     string // COMMITTING | SHARING | RESOLVING | KEY_READY
	CreatedAt time.Time
}

// ChainAdapterFactory 链适配器工厂接口（避免循环依赖）
type ChainAdapterFactory interface {
	Adapter(chain string) (ChainAdapter, error)
}

// ChainAdapter 链适配器接口（避免循环依赖）
type ChainAdapter interface {
	BuildWithdrawTemplate(params WithdrawTemplateParams) (*TemplateResult, error)
}

// WithdrawTemplateParams 提现模板参数（避免循环依赖）
type WithdrawTemplateParams struct {
	Chain         string
	Asset         string
	VaultID       uint32
	KeyEpoch      uint64
	WithdrawIDs   []string
	Outputs       []WithdrawOutput
	Inputs        []UTXO
	ChangeAddress string
	Fee           uint64
	ChangeAmount  uint64
	ContractAddr  string
	MethodID      []byte
}

// WithdrawOutput 提现输出
type WithdrawOutput struct {
	WithdrawID string
	To         string
	Amount     uint64
}

// UTXO UTXO 信息
type UTXO struct {
	TxID          string
	Vout          uint32
	Amount        uint64
	ScriptPubKey  []byte
	ConfirmHeight uint64
}

// TemplateResult 模板构建结果
type TemplateResult struct {
	TemplateHash []byte
	TemplateData []byte
	SigHashes    [][]byte
}

// NewTransitionWorker 创建 TransitionWorker
func NewTransitionWorker(
	stateReader StateReader,
	txSubmitter TxSubmitter,
	pubKeyProvider MinerPubKeyProvider,
	cryptoFactory CryptoExecutorFactory,
	vaultProvider VaultCommitteeProvider,
	signerProvider SignerSetProvider,
	adapterFactory ChainAdapterFactory,
	localAddress string,
	logger logs.Logger,
) *TransitionWorker {
	return &TransitionWorker{
		stateReader:         stateReader,
		txSubmitter:         txSubmitter,
		pubKeyProvider:      pubKeyProvider,
		cryptoFactory:       cryptoFactory,
		vaultProvider:       vaultProvider,
		signerProvider:      signerProvider,
		adapterFactory:      adapterFactory,
		localAddress:        localAddress,
		Logger:              logger,
		sessions:            make(map[string]*DKGSession),
		commitTimeout:       30 * time.Second,
		shareTimeout:        30 * time.Second,
		signTimeout:         60 * time.Second,
		transitionThreshold: 0.2,   // 默认 20% 变化触发
		ewmaAlpha:           0.3,   // EWMA 平滑系数
		epochBlocks:         40000, // 默认 Epoch 边界（40000 个区块）
		ewmaHistory:         make(map[string]float64),
	}
}

// StartSession 启动新的 DKG 会话
func (w *TransitionWorker) StartSession(ctx context.Context, chain string, vaultID uint32, epochID uint64, signAlgo pb.SignAlgo) error {
	sessionID := fmt.Sprintf("%s_%d_%d", chain, vaultID, epochID)

	committeeInfos, err := w.vaultProvider.VaultCommittee(chain, vaultID, epochID)
	if err != nil {
		return fmt.Errorf("load committee: %w", err)
	}
	if len(committeeInfos) == 0 {
		return fmt.Errorf("empty committee for chain=%s vault=%d epoch=%d", chain, vaultID, epochID)
	}

	threshold, err := w.vaultProvider.CalculateThreshold(chain, vaultID)
	if err != nil || threshold <= 0 {
		threshold = int(float64(len(committeeInfos))*0.67 + 0.5)
	}
	if threshold < 1 {
		threshold = 1
	}
	if threshold > len(committeeInfos) {
		threshold = len(committeeInfos)
	}

	committeeMembers := make([]string, 0, len(committeeInfos))
	committeePubKeys := make(map[string][]byte, len(committeeInfos))
	myIndex := 0
	for i, member := range committeeInfos {
		memberID := string(member.ID)
		committeeMembers = append(committeeMembers, memberID)
		if len(member.PublicKey) > 0 {
			pubCopy := make([]byte, len(member.PublicKey))
			copy(pubCopy, member.PublicKey)
			committeePubKeys[memberID] = pubCopy
		}
		if memberID == w.localAddress {
			myIndex = i + 1
		}
	}
	if myIndex == 0 {
		w.Logger.Warn("[TransitionWorker] node %s not in committee for session %s. Committee: %v",
			w.localAddress, sessionID, committeeMembers)
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, exists := w.sessions[sessionID]; exists {
		w.Logger.Debug("[TransitionWorker] session %s already exists", sessionID)
		return nil // 幂等
	}

	session := &DKGSession{
		Chain:            chain,
		VaultID:          vaultID,
		EpochID:          epochID,
		SessionID:        sessionID,
		SignAlgo:         signAlgo,
		Commitments:      make(map[string][]byte),
		ReceivedShrs:     make(map[string][]byte),
		Committee:        committeeMembers,
		CommitteePubKeys: committeePubKeys,
		MyIndex:          myIndex,
		Threshold:        threshold,
		Phase:            "COMMITTING",
		CreatedAt:        time.Now(),
	}

	w.sessions[sessionID] = session
	w.Logger.Info("[TransitionWorker] started DKG session %s", sessionID)

	// 异步执行 DKG 流程
	go w.runSession(ctx, session)

	return nil
}

// runSession 运行 DKG 会话
func (w *TransitionWorker) runSession(ctx context.Context, session *DKGSession) {
	// logs.SetThreadNodeContext(w.localAddress) removed
	w.Logger.Debug("[TransitionWorker] runSession %s phase=%s", session.SessionID, session.Phase)

	// Phase 1: 提交 commitment
	if err := w.submitCommitment(ctx, session); err != nil {
		w.Logger.Error("[TransitionWorker] submitCommitment failed: %v", err)
		return
	}

	// ⏳ 等待区块确认（承诺阶段上链）
	time.Sleep(10 * time.Second)

	// Phase 2: 等待所有 commitment 并提交 share
	if err := w.submitShares(ctx, session); err != nil {
		w.Logger.Error("[TransitionWorker] submitShares failed: %v", err)
		return
	}

	// ⏳ 等待区块确认（分发阶段上链）
	time.Sleep(15 * time.Second)

	// Phase 3: 收集 share 并生成密钥
	if err := w.generateKey(ctx, session); err != nil {
		w.Logger.Error("[TransitionWorker] generateKey failed: %v", err)
		return
	}

	// Phase 4: 提交验证签名
	if err := w.submitValidation(ctx, session); err != nil {
		w.Logger.Error("[TransitionWorker] submitValidation failed: %v", err)
		return
	}

	w.Logger.Info("[TransitionWorker] DKG session %s completed", session.SessionID)
}

// submitCommitment 提交 DKG 承诺
func (w *TransitionWorker) submitCommitment(ctx context.Context, session *DKGSession) error {
	w.Logger.Debug("[TransitionWorker] submitCommitment session=%s", session.SessionID)

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
		Base:             w.newBaseMessage(),
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
	w.Logger.Debug("[TransitionWorker] submitShares session=%s", session.SessionID)

	// 为每个委员会成员提交加密的 share
	for idx, receiverID := range session.Committee {
		receiverIndex := idx + 1 // 1-based index

		receiverPubKey, err := w.getReceiverPubKey(session, receiverID)
		if err != nil {
			w.Logger.Warn("[TransitionWorker] cannot get pubkey for %s: %v", receiverID, err)
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
			Base:       w.newBaseMessage(),
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

func (w *TransitionWorker) getReceiverPubKey(session *DKGSession, receiverID string) ([]byte, error) {
	if w.pubKeyProvider != nil {
		pub, err := w.pubKeyProvider.GetMinerSigningPubKey(receiverID, session.SignAlgo)
		if err == nil && len(pub) > 0 {
			out := make([]byte, len(pub))
			copy(out, pub)
			return out, nil
		}
	}
	if session != nil && session.CommitteePubKeys != nil {
		if pub, ok := session.CommitteePubKeys[receiverID]; ok && len(pub) > 0 {
			out := make([]byte, len(pub))
			copy(out, pub)
			return out, nil
		}
	}
	return nil, fmt.Errorf("pubkey not found")
}

// generateKey 收集 shares 并生成本地密钥
func (w *TransitionWorker) generateKey(ctx context.Context, session *DKGSession) error {
	w.Logger.Debug("[TransitionWorker] generateKey session=%s", session.SessionID)

	// 获取 DKG 执行器
	dkgExec, err := w.cryptoFactory.NewDKGExecutor(int32(session.SignAlgo))
	if err != nil {
		return fmt.Errorf("create dkg executor: %w", err)
	}

	// 1) 计算本地对自己的多项式份额（dealer 为自己，receiver 为自己索引）
	myShareBytes := dkgExec.EvaluateShare(session.Polynomial, session.MyIndex)

	// 2) 收集并解密所有发给本节点的 shares，验证后聚合
	decryptedShares, err := w.collectAndVerifyShares(ctx, session)
	if err != nil {
		return fmt.Errorf("collect shares: %w", err)
	}

	// 聚合所有 share：s_i = Σ_j f_j(i)
	allShares := make([][]byte, 0, 1+len(decryptedShares))
	allShares = append(allShares, myShareBytes)
	for _, s := range decryptedShares {
		allShares = append(allShares, s)
	}
	aggShare := dkgExec.AggregateShares(allShares)

	session.LocalShare = new(big.Int).SetBytes(aggShare)
	session.LocalShareBytes = aggShare

	// 3) 聚合 group_pubkey = Σ A_j0（收集所有 dealer 的 a_i0）
	groupPubkey, err := w.aggregateGroupPubkey(ctx, session)
	if err != nil {
		return fmt.Errorf("aggregate group pubkey: %w", err)
	}
	session.GroupPubkey = groupPubkey

	session.Phase = "KEY_READY"
	w.Logger.Info("[TransitionWorker] generateKey completed: localShare=%x groupPubkey=%x",
		session.LocalShareBytes[:8], session.GroupPubkey[:8])
	return nil
}

// collectAndVerifyShares 扫描链上密文 shares，解密属于本节点的份额并做一致性校验
func (w *TransitionWorker) collectAndVerifyShares(ctx context.Context, session *DKGSession) (map[string][]byte, error) {
	result := make(map[string][]byte) // dealerID -> share

	// 1. 扫描该 session 的所有 DKG share（按前缀）
	sharePrefix := fmt.Sprintf("v1_frost_vault_dkg_share_%s_%d_%s_", session.Chain, session.VaultID, padUint(session.EpochID))

	// 解析本地 secp256k1 私钥（用于 ECIES 解密）
	privStr := utils.GetKeyManager().GetPrivateKey()
	privK, err := utils.ParseSecp256k1PrivateKey(privStr)
	if err != nil {
		return nil, fmt.Errorf("parse local privkey: %w", err)
	}
	localPriv32 := privK.Serialize()

	err = w.stateReader.Scan(sharePrefix, func(k string, v []byte) bool {
		// 反序列化存储的 share 记录
		var share pb.FrostVaultDkgShare
		if err := proto.Unmarshal(v, &share); err != nil {
			w.Logger.Warn("[DKG] skip invalid share record: %v", err)
			return true
		}
		// 只处理发给本节点的份额
		if share.ReceiverId != w.localAddress {
			return true
		}

		// 解密密文
		plain, err := security.ECIESDecrypt(localPriv32, share.Ciphertext)
		if err != nil {
			w.Logger.Warn("[DKG] decrypt share from %s failed: %v", share.DealerId, err)
			return true
		}

		// 验证与 dealer 承诺点一致
		if ok := w.verifyShareAgainstCommitments(session, share.DealerId, plain, session.MyIndex); !ok {
			w.Logger.Warn("[DKG] share verification failed for dealer=%s", share.DealerId)
			return true
		}

		// 记录
		result[share.DealerId] = plain
		return true
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// verifyShareAgainstCommitments 使用 Feldman VSS 校验 share 与 dealer 承诺点一致性
func (w *TransitionWorker) verifyShareAgainstCommitments(session *DKGSession, dealerID string, share []byte, receiverIndex int) bool {
	// 读取 dealer 的 commitment
	commitKey := fmt.Sprintf("v1_frost_vault_dkg_commit_%s_%d_%s_%s", session.Chain, session.VaultID, padUint(session.EpochID), dealerID)
	data, exists, err := w.stateReader.Get(commitKey)
	if err != nil || !exists || len(data) == 0 {
		return false
	}
	var commit pb.FrostVaultDkgCommitment
	if err := proto.Unmarshal(data, &commit); err != nil {
		return false
	}
	// Verify: g^share == Π(A_k^(x^k)), x=receiverIndex
	return security.VerifyShareAgainstCommitment(share, commit.CommitmentPoints, big.NewInt(int64(receiverIndex)))
}

// aggregateGroupPubkey 收集所有 dealer 的 a_i0 聚合得到 group_pubkey
func (w *TransitionWorker) aggregateGroupPubkey(ctx context.Context, session *DKGSession) ([]byte, error) {
	dkgExec, err := w.cryptoFactory.NewDKGExecutor(int32(session.SignAlgo))
	if err != nil {
		return nil, err
	}

	commitPrefix := fmt.Sprintf("v1_frost_vault_dkg_commit_%s_%d_%s_", session.Chain, session.VaultID, padUint(session.EpochID))
	ai0Points := make([][]byte, 0, len(session.Committee))

	err = w.stateReader.Scan(commitPrefix, func(k string, v []byte) bool {
		var commit pb.FrostVaultDkgCommitment
		if err := proto.Unmarshal(v, &commit); err != nil {
			return true
		}
		if len(commit.AI0) == 0 {
			return true
		}
		ai0Points = append(ai0Points, commit.AI0)
		return true
	})
	if err != nil {
		return nil, err
	}
	if len(ai0Points) == 0 {
		// 回退：使用本地多项式的 a0 计算（保证不为空）
		coeffs := session.Polynomial.Coefficients()
		if len(coeffs) == 0 {
			return nil, fmt.Errorf("no commitments found and local polynomial empty")
		}
		pt := dkgExec.ScalarBaseMult(coeffs[0])
		return serializeCurvePoint(pt), nil
	}

	// 使用 DKG 执行器聚合公钥
	groupPubkey := dkgExec.ComputeGroupPubkey(ai0Points)
	return groupPubkey, nil
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
	w.Logger.Debug("[TransitionWorker] submitValidation session=%s", session.SessionID)

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
		Base:           w.newBaseMessage(),
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

func (w *TransitionWorker) newBaseMessage() *pb.BaseMessage {
	// 获取当前账户的 Nonce (从状态机读取)
	var currentNonce uint64
	accountKey := fmt.Sprintf("v1_account_%s", w.localAddress)
	if data, exists, err := w.stateReader.Get(accountKey); err == nil && exists {
		var acc pb.Account
		if err := proto.Unmarshal(data, &acc); err == nil {
			currentNonce = acc.Nonce
		}
	}

	// 策略：使用状态 Nonce + 进程内原子计数器，确保绝对不重复
	// 避免使用 time.Now().UnixNano()，因为它可能在毫秒级并发时导致 Nonce 冲突
	offset := atomic.AddUint64(&dkgTxCounter, 1)
	nonce := currentNonce + (offset % 1000)

	w.Logger.Debug("[TransitionWorker] newBaseMessage: sender=%s, base_nonce=%d, offset=%d, final_nonce=%d",
		w.localAddress, currentNonce, offset, nonce)

	return &pb.BaseMessage{
		TxId:        generateDkgTxID(),
		FromAddress: w.localAddress,
		Nonce:       nonce,
		Status:      pb.Status_PENDING,
	}
}

func generateDkgTxID() string {
	counter := atomic.AddUint64(&dkgTxCounter, 1)
	return fmt.Sprintf("0x%016x%016x", time.Now().UnixNano(), counter)
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
	w.Logger.Debug("[TransitionWorker] CheckTriggerConditions height=%d", height)

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
			w.Logger.Debug("[TransitionWorker] vault config not found for chain %s, skip", chain)
			continue
		}

		var vaultCfg pb.FrostVaultConfig
		if err := proto.Unmarshal(vaultCfgData, &vaultCfg); err != nil {
			w.Logger.Warn("[TransitionWorker] failed to unmarshal vault config: %v", err)
			continue
		}

		// 遍历该链的所有 Vault
		for vaultID := uint32(0); vaultID < vaultCfg.VaultCount; vaultID++ {
			// 检查 DKG 失败并触发重启
			if err := w.checkAndRestartDKG(ctx, chain, vaultID, height); err != nil {
				w.Logger.Warn("[TransitionWorker] check DKG restart failed: chain=%s vault=%d: %v", chain, vaultID, err)
			}

			// 检查触发条件
			if err := w.checkVaultTrigger(ctx, chain, vaultID, height, currentSigners); err != nil {
				w.Logger.Warn("[TransitionWorker] check vault trigger failed: chain=%s vault=%d: %v", chain, vaultID, err)
				// 继续检查其他 Vault
			}
		}
	}

	return nil
}

// StartPendingSessions scans transition states and starts DKG sessions when needed.
func (w *TransitionWorker) StartPendingSessions(ctx context.Context) error {
	// 使用更通用的前缀，确保能匹配到 keys.KeyFrostVaultTransition 生成的键
	prefix := "v1_frost_vault_transition"

	return w.stateReader.Scan(prefix, func(k string, v []byte) bool {
		w.Logger.Debug("[TransitionWorker] Found transition key: %s", k)
		var state pb.VaultTransitionState
		if err := proto.Unmarshal(v, &state); err != nil {
			w.Logger.Error("[TransitionWorker] Failed to unmarshal transition state for key %s: %v", k, err)
			return true
		}
		if state.DkgStatus == "KEY_READY" || state.DkgStatus == "FAILED" {
			return true
		}
		if err := w.StartSession(ctx, state.Chain, state.VaultId, state.EpochId, state.SignAlgo); err != nil {
			w.Logger.Warn("[TransitionWorker] start session failed: chain=%s vault=%d epoch=%d: %v",
				state.Chain, state.VaultId, state.EpochId, err)
		}
		return true
	})
}

// checkVaultTrigger 检查单个 Vault 的触发条件
func (w *TransitionWorker) checkVaultTrigger(ctx context.Context, chain string, vaultID uint32, height uint64, currentSigners []SignerInfo) error {
	// 1. 固定边界检查：轮换只在 epochBlocks 边界生效
	// 计算当前高度是否在 Epoch 边界
	epochBoundary := (height / w.epochBlocks) * w.epochBlocks
	if height != epochBoundary {
		// 不在边界，不触发轮换
		return nil
	}

	// 2. 获取当前 Vault 的 epoch
	currentEpoch := w.vaultProvider.VaultCurrentEpoch(chain, vaultID)

	// 3. 获取当前和上一个 epoch 的委员会
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

	// 4. 计算 change_ratio（委员会成员变化比例）
	changeRatio := w.calculateChangeRatio(prevCommittee, currentCommittee)

	// 5. 应用 EWMA 平滑（维护历史状态）
	ewmaKey := fmt.Sprintf("%s_%d", chain, vaultID)
	w.mu.Lock()
	prevEWMA, exists := w.ewmaHistory[ewmaKey]
	if !exists {
		// 首次计算，直接使用当前值
		w.ewmaHistory[ewmaKey] = changeRatio
		prevEWMA = changeRatio
	} else {
		// EWMA: new_value = alpha * current + (1 - alpha) * prev
		newEWMA := w.ewmaAlpha*changeRatio + (1-w.ewmaAlpha)*prevEWMA
		w.ewmaHistory[ewmaKey] = newEWMA
		changeRatio = newEWMA // 使用平滑后的值
	}
	w.mu.Unlock()

	w.Logger.Debug("[TransitionWorker] chain=%s vault=%d height=%d change_ratio=%.4f ewma=%.4f threshold=%.4f",
		chain, vaultID, height, changeRatio, prevEWMA, w.transitionThreshold)

	// 6. 检查是否达到阈值
	if changeRatio >= w.transitionThreshold {
		w.Logger.Info("[TransitionWorker] trigger condition met: chain=%s vault=%d epoch=%d height=%d change_ratio=%.4f (ewma)",
			chain, vaultID, currentEpoch+1, height, changeRatio)

		// 创建新的 VaultTransitionState
		return w.createTransitionState(ctx, chain, vaultID, currentEpoch+1, height, prevCommittee, currentCommittee)
	}

	return nil
}

// checkAndRestartDKG 检查 DKG 失败状态并触发重启
// 当检测到 DkgStatusFailed 时，创建新的 transition state 并重启 DKG
func (w *TransitionWorker) checkAndRestartDKG(ctx context.Context, chain string, vaultID uint32, height uint64) error {
	// 获取当前 epoch
	currentEpoch := w.vaultProvider.VaultCurrentEpoch(chain, vaultID)

	// 检查当前 epoch 的 transition state
	transitionKey := fmt.Sprintf("v1_frost_vault_transition_%s_%d_%s", chain, vaultID, padUint(currentEpoch))
	transitionData, exists, err := w.stateReader.Get(transitionKey)
	if err != nil {
		return fmt.Errorf("get transition state: %w", err)
	}
	if !exists || len(transitionData) == 0 {
		return nil // 没有 transition state，无需重启
	}

	var transition pb.VaultTransitionState
	if err := proto.Unmarshal(transitionData, &transition); err != nil {
		return fmt.Errorf("unmarshal transition: %w", err)
	}

	// 检查 DKG 状态是否为 FAILED
	if transition.DkgStatus != "FAILED" {
		return nil // 不是失败状态，无需重启
	}

	w.Logger.Info("[TransitionWorker] DKG failed detected: chain=%s vault=%d epoch=%d, triggering restart", chain, vaultID, currentEpoch)

	// 获取完整委员会（从 Top10000 重新分配）
	signers, err := w.signerProvider.Top10000(height)
	if err != nil {
		return fmt.Errorf("get top10000: %w", err)
	}

	// 获取 VaultConfig
	vaultCfgKey := fmt.Sprintf("v1_frost_vault_cfg_%s_%d", chain, vaultID)
	vaultCfgData, exists, err := w.stateReader.Get(vaultCfgKey)
	if err != nil || !exists {
		return fmt.Errorf("vault config not found")
	}

	var vaultCfg pb.FrostVaultConfig
	if err := proto.Unmarshal(vaultCfgData, &vaultCfg); err != nil {
		return fmt.Errorf("unmarshal vault config: %w", err)
	}

	// 重新计算委员会（使用新的 epoch）
	newEpoch := currentEpoch + 1
	// 使用确定性分配算法重新分配委员会
	// 这里简化处理，实际应该调用 committee.AssignToVaults
	// 为了简化，我们使用当前 Top10000 的前 N 个作为委员会
	committeeSize := int(vaultCfg.CommitteeSize)
	if len(signers) < committeeSize {
		return fmt.Errorf("insufficient signers: have %d, need %d", len(signers), committeeSize)
	}

	// 构建新的委员会成员列表
	newCommitteeMembers := make([]string, 0, committeeSize)
	for i := 0; i < committeeSize && i < len(signers); i++ {
		newCommitteeMembers = append(newCommitteeMembers, string(signers[i].ID))
	}

	// 计算新的门限
	thresholdRatio := float64(vaultCfg.ThresholdRatio)
	if thresholdRatio <= 0 || thresholdRatio > 1 {
		thresholdRatio = 0.67 // 默认 2/3
	}
	threshold := int(float64(len(newCommitteeMembers)) * thresholdRatio)
	if threshold < 1 {
		threshold = 1
	}

	// 提交新的 transition state 交易
	// 注意：这里需要通过 txSubmitter 提交 transition state
	// 为了简化，我们直接启动新的 DKG 会话，transition state 会在 DKG 流程中创建
	// TODO: 实现 SubmitTransitionStateTx 方法以显式创建 transition state
	w.Logger.Info("[TransitionWorker] DKG restart: chain=%s vault=%d new_epoch=%d n=%d t=%d",
		chain, vaultID, newEpoch, len(newCommitteeMembers), threshold)

	// 启动新的 DKG 会话
	if err := w.StartSession(ctx, chain, vaultID, newEpoch, vaultCfg.SignAlgo); err != nil {
		return fmt.Errorf("start new DKG session: %w", err)
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
		w.Logger.Debug("[TransitionWorker] transition state already exists: %s", transitionKey)
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
		w.Logger.Warn("[TransitionWorker] failed to get old group pubkey: %v", err)
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
// 扫描该 Vault 的所有 UTXO，生成 sweep 模板（所有 UTXO -> 新地址）
func (w *TransitionWorker) planBTCMigrationJobs(ctx context.Context, chain string, vaultID uint32, epochID uint64, transition *pb.VaultTransitionState) error {
	// 1. 扫描该 Vault 的所有 UTXO（未锁定的）
	utxoPrefix := fmt.Sprintf("v1_frost_btc_utxo_%d_", vaultID)
	utxos := make([]UTXO, 0)

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

		// 检查是否已锁定（正在提现中）
		lockKey := fmt.Sprintf("v1_frost_btc_locked_utxo_%d_%s_%d", vaultID, txid, vout)
		_, locked, _ := w.stateReader.Get(lockKey)
		if locked {
			return true // 已锁定，跳过
		}

		// 解析 UTXO 信息（从 v 或从 RechargeRequest 获取）
		// TODO: 从 v 中解析完整的 UTXO 信息（amount, scriptPubKey, confirmHeight）
		// 这里简化处理，假设可以从其他地方获取
		utxo := UTXO{
			TxID:          txid,
			Vout:          uint32(vout),
			Amount:        0, // TODO: 从实际数据解析
			ScriptPubKey:  nil,
			ConfirmHeight: 0,
		}

		utxos = append(utxos, utxo)
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

	// TODO: 从 group_pubkey 派生 BTC Taproot 地址
	// newAddress := deriveBTCTaprootAddress(newGroupPubkey)
	// 这里简化处理，假设可以从 VaultState 获取新地址
	newAddress := "" // TODO: 从新 VaultState 获取地址

	// 3. 计算总金额和手续费
	var totalAmount uint64
	for _, utxo := range utxos {
		totalAmount += utxo.Amount
	}
	// 估算手续费（简化：基于 input/output 数量）
	fee := estimateBTCMigrationFee(len(utxos), 1) // 1 个输出（新地址）
	changeAmount := uint64(0)
	if totalAmount > fee {
		changeAmount = totalAmount - fee
		// 如果找零小于粉尘限制，不创建找零输出
		if changeAmount < 546 { // DustLimit
			changeAmount = 0
		}
	}

	// 4. 获取链适配器并构建迁移模板
	// 使用 WithdrawTemplateParams，但用于迁移场景
	adapter, err := w.getChainAdapter(chain)
	if err != nil {
		return fmt.Errorf("get chain adapter: %w", err)
	}

	params := WithdrawTemplateParams{
		Chain:       chain,
		Asset:       "BTC",
		VaultID:     vaultID,
		KeyEpoch:    epochID - 1, // 使用旧 key_epoch（迁移时使用旧密钥签名）
		WithdrawIDs: []string{},  // 迁移没有 withdraw_id
		Inputs:      utxos,
		Outputs: []WithdrawOutput{
			{
				WithdrawID: fmt.Sprintf("migration_%d_%d", vaultID, epochID),
				To:         newAddress,
				Amount:     changeAmount,
			},
		},
		Fee:          fee,
		ChangeAmount: 0, // 不创建找零（全部迁移到新地址）
	}

	result, err := adapter.BuildWithdrawTemplate(params)
	if err != nil {
		return fmt.Errorf("build migration template: %w", err)
	}

	// 5. 创建 MigrationJob 记录（通过 VM 交易或直接存储）
	// TODO: 创建 MigrationJob 并启动 ROAST 签名会话
	// 这里简化处理，记录日志
	logs.Info("[TransitionWorker] planned BTC migration: vault=%d epoch=%d utxos=%d template_hash=%x",
		vaultID, epochID, len(utxos), result.TemplateHash)

	return nil
}

// estimateBTCMigrationFee 估算 BTC 迁移手续费
func estimateBTCMigrationFee(inputCount, outputCount int) uint64 {
	// 简化估算：每个 input 约 148 bytes，每个 output 约 34 bytes
	baseSize := 10
	inputSize := inputCount * 148
	outputSize := outputCount * 34
	witnessSize := inputCount * 64 // Taproot witness
	totalVBytes := baseSize + inputSize + outputSize + witnessSize/4
	feeRate := uint64(10) // sat/vbyte
	return uint64(totalVBytes) * feeRate
}

// getChainAdapter 获取链适配器
func (w *TransitionWorker) getChainAdapter(chain string) (ChainAdapter, error) {
	if w.adapterFactory == nil {
		return nil, fmt.Errorf("chain adapter factory not available")
	}
	return w.adapterFactory.Adapter(chain)
}

// planContractMigrationJobs 规划合约链迁移 Job（updatePubkey）
// 生成 updatePubkey(new_pubkey, vault_id, epoch_id) 模板
func (w *TransitionWorker) planContractMigrationJobs(ctx context.Context, chain string, vaultID uint32, epochID uint64, transition *pb.VaultTransitionState) error {
	// 1. 获取新 group_pubkey
	newGroupPubkey := transition.NewGroupPubkey
	if len(newGroupPubkey) == 0 {
		return fmt.Errorf("new group pubkey not ready")
	}

	// 2. 获取合约地址（从 VaultConfig 或 VaultState 获取）
	// TODO: 从 VaultConfig 获取合约地址
	contractAddr := "" // TODO: 从配置获取

	// 3. 构建 updatePubkey 方法调用
	// 方法签名：updatePubkey(bytes32 newPubkey, uint32 vaultId, uint64 epochId)
	// 方法 ID：keccak256("updatePubkey(bytes32,uint32,uint64)")[:4]
	methodID := []byte{0x12, 0x34, 0x56, 0x78} // TODO: 计算实际的方法 ID

	// 4. 构建 calldata（ABI 编码）
	// calldata = methodID + encode(newPubkey) + encode(vaultId) + encode(epochId)
	// TODO: 实现 ABI 编码
	calldata := make([]byte, 0)
	calldata = append(calldata, methodID...)
	// 这里简化处理，实际需要完整的 ABI 编码

	// 5. 获取链适配器并构建迁移模板
	adapter, err := w.getChainAdapter(chain)
	if err != nil {
		return fmt.Errorf("get chain adapter: %w", err)
	}

	params := WithdrawTemplateParams{
		Chain:        chain,
		Asset:        "NATIVE", // 原生币
		VaultID:      vaultID,
		KeyEpoch:     epochID - 1, // 使用旧 key_epoch
		WithdrawIDs:  []string{},
		Outputs:      []WithdrawOutput{}, // updatePubkey 没有输出
		ContractAddr: contractAddr,
		MethodID:     methodID,
	}

	result, err := adapter.BuildWithdrawTemplate(params)
	if err != nil {
		return fmt.Errorf("build migration template: %w", err)
	}

	// 6. 创建 MigrationJob 记录
	// TODO: 创建 MigrationJob 并启动 ROAST 签名会话
	logs.Info("[TransitionWorker] planned contract migration: chain=%s vault=%d epoch=%d template_hash=%x",
		chain, vaultID, epochID, result.TemplateHash)

	return nil
}
