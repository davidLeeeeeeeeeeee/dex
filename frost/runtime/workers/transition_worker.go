// frost/runtime/workers/transition_worker.go
// TransitionWorker: 澶勭悊 Vault DKG 杞崲鐨勮繍琛屾椂缁勪欢

package workers

import (
	"context"
	"crypto/sha256"
	"dex/frost/security"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"dex/utils"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"
)

// TransitionWorker 澶勭悊 Vault DKG 杞崲
type TransitionWorker struct {
	mu sync.RWMutex

	stateReader     StateReader
	txSubmitter     TxSubmitter
	pubKeyProvider  MinerPubKeyProvider
	cryptoFactory   CryptoExecutorFactory // 瀵嗙爜瀛︽墽琛屽櫒宸ュ巶
	vaultProvider   VaultCommitteeProvider
	signerProvider  SignerSetProvider
	adapterFactory  ChainAdapterFactory // 閾鹃€傞厤鍣ㄥ伐鍘?
	localShareStore LocalShareStore
	localPrivateKey string
	localAddress    string
	Logger          logs.Logger

	// DKG 浼氳瘽绠＄悊
	sessions map[string]*DKGSession // sessionID -> session

	// 閰嶇疆
	commitTimeout       time.Duration
	shareTimeout        time.Duration
	signTimeout         time.Duration
	transitionThreshold float64 // change_ratio 闃堝€硷紙榛樿 0.2锛?
	ewmaAlpha           float64 // EWMA 骞虫粦绯绘暟锛堥粯璁?0.3锛?
	epochBlocks         uint64  // Epoch 杈圭晫鍖哄潡鏁帮紙榛樿 40000锛?

	// EWMA 鐘舵€佺淮鎶わ紙鎸?Vault 鐙珛锛?
	// key: chain_vaultID, value: ewmaValue
	ewmaHistory map[string]float64

	// Nonce 绠＄悊
	lastIssuedNonce uint64
}

var dkgTxCounter uint64

// DKGSession 鍗曚釜 DKG 浼氳瘽鐘舵€?
type DKGSession struct {
	Chain     string
	VaultID   uint32
	EpochID   uint64
	SessionID string
	SignAlgo  pb.SignAlgo

	// DKG 瀵嗛挜鏉愭枡锛堥€氳繃鎺ュ彛鎿嶄綔锛?
	Polynomial  PolynomialHandle // 鏈湴澶氶」寮忓彞鏌?
	LocalShare  *big.Int         // 鏈湴 share = 危 f_j(my_index)
	LocalShares map[int][]byte   // 鍙戦€佺粰鍚勬帴鏀惰€呯殑 share f(receiver_index)
	EncRands    map[int][]byte   // 鍔犲瘑闅忔満鏁帮紙鐢ㄤ簬 reveal锛?

	// 杈撳嚭
	LocalShareBytes []byte // 鏈湴 share锛堝簭鍒楀寲鍚庯級
	GroupPubkey     []byte
	Commitments     map[string][]byte // miner -> commitment points
	ReceivedShrs    map[string][]byte // dealer -> ciphertext

	// 濮斿憳浼氫俊鎭?
	MyIndex          int               // 鏈妭鐐瑰湪濮斿憳浼氫腑鐨勭储寮?
	Committee        []string          // 濮斿憳浼氭垚鍛樺垪琛?
	CommitteePubKeys map[string][]byte // 濮斿憳浼氭垚鍛樺叕閽?
	Threshold        int               // 闂ㄩ檺 t

	// 鐘舵€?
	Phase           string // COMMITTING | SHARING | RESOLVING | KEY_READY
	TriggerHeight   uint64
	CommitDeadline  uint64
	SharingDeadline uint64
	DisputeDeadline uint64
	CreatedAt       time.Time
}

// ChainAdapterFactory 閾鹃€傞厤鍣ㄥ伐鍘傛帴鍙ｏ紙閬垮厤寰幆渚濊禆锛?
type ChainAdapterFactory interface {
	Adapter(chain string) (ChainAdapter, error)
}

// ChainAdapter 閾鹃€傞厤鍣ㄦ帴鍙ｏ紙閬垮厤寰幆渚濊禆锛?
type ChainAdapter interface {
	BuildWithdrawTemplate(params WithdrawTemplateParams) (*TemplateResult, error)
}

// WithdrawTemplateParams 鎻愮幇妯℃澘鍙傛暟锛堥伩鍏嶅惊鐜緷璧栵級
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

// WithdrawOutput 鎻愮幇杈撳嚭
type WithdrawOutput struct {
	WithdrawID string
	To         string
	Amount     uint64
}

// UTXO UTXO 淇℃伅
type UTXO struct {
	TxID          string
	Vout          uint32
	Amount        uint64
	ScriptPubKey  []byte
	ConfirmHeight uint64
}

// TemplateResult 妯℃澘鏋勫缓缁撴灉
type TemplateResult struct {
	TemplateHash []byte
	TemplateData []byte
	SigHashes    [][]byte
}

// NewTransitionWorker 鍒涘缓 TransitionWorker
func NewTransitionWorker(
	stateReader StateReader,
	txSubmitter TxSubmitter,
	pubKeyProvider MinerPubKeyProvider,
	cryptoFactory CryptoExecutorFactory,
	vaultProvider VaultCommitteeProvider,
	signerProvider SignerSetProvider,
	adapterFactory ChainAdapterFactory,
	localShareStore LocalShareStore,
	localPrivateKey string,
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
		localShareStore:     localShareStore,
		localPrivateKey:     localPrivateKey,
		localAddress:        localAddress,
		Logger:              logger,
		sessions:            make(map[string]*DKGSession),
		commitTimeout:       30 * time.Second,
		shareTimeout:        30 * time.Second,
		signTimeout:         60 * time.Second,
		transitionThreshold: 0.2,   // 榛樿 20% 鍙樺寲瑙﹀彂
		ewmaAlpha:           0.3,   // EWMA 骞虫粦绯绘暟
		epochBlocks:         40000, // 榛樿 Epoch 杈圭晫锛?0000 涓尯鍧楋級
		ewmaHistory:         make(map[string]float64),
		lastIssuedNonce:     0,
	}
}

// StartSession 鍚姩鏂扮殑 DKG 浼氳瘽
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
		w.Logger.Debug("[TransitionWorker] dkg committee member session=%s chain=%s vault=%d epoch=%d member_index=%d member_id=%s pubkey_fp=%s",
			sessionID, chain, vaultID, epochID, i+1, memberID, shareFingerprintForLog(member.PublicKey))
	}
	w.Logger.Debug("[TransitionWorker] dkg start context session=%s chain=%s vault=%d epoch=%d sign_algo=%s threshold=%d committee=%s local=%s my_index=%d",
		sessionID, chain, vaultID, epochID, signAlgo.String(), threshold, committeeMembersForLog(committeeMembers), w.localAddress, myIndex)
	if myIndex == 0 {
		w.Logger.Debug("[TransitionWorker] node %s not in committee for session %s. Committee: %v",
			w.localAddress, sessionID, committeeMembers)
		return nil
	}

	w.Logger.Debug("[TransitionWorker] node %s (index %d) starting DKG session %s", w.localAddress, myIndex, sessionID)

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, exists := w.sessions[sessionID]; exists {
		w.Logger.Debug("[TransitionWorker] session %s already exists", sessionID)
		return nil // 骞傜瓑
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

	// 灏濊瘯浠庨摼涓婅幏鍙栨埅姝㈡棩鏈?
	transitionKey := keys.KeyFrostVaultTransition(chain, vaultID, epochID)
	if data, exists, err := w.stateReader.Get(transitionKey); err == nil && exists {
		var transition pb.VaultTransitionState
		if err := proto.Unmarshal(data, &transition); err == nil {
			session.TriggerHeight = transition.TriggerHeight
			session.CommitDeadline = transition.DkgCommitDeadline
			session.SharingDeadline = transition.DkgSharingDeadline
			session.DisputeDeadline = transition.DkgDisputeDeadline
			w.Logger.Info("[TransitionWorker] session %s deadlines: commit=%d, sharing=%d, dispute=%d",
				sessionID, session.CommitDeadline, session.SharingDeadline, session.DisputeDeadline)
		}
	}

	w.sessions[sessionID] = session
	w.Logger.Info("[TransitionWorker] started DKG session %s", sessionID)

	// 寮傛鎵ц DKG 娴佺▼
	go w.runSession(ctx, session)

	return nil
}

// runSession 杩愯 DKG 浼氳瘽
func (w *TransitionWorker) runSession(ctx context.Context, session *DKGSession) {
	w.Logger.Debug("[TransitionWorker] runSession %s phase=%s", session.SessionID, session.Phase)

	// Phase 1: 鎻愪氦 commitment
	// Commitment 闃舵鍦?Session 鍚姩鍚庣珛鍗虫墽琛岋紙楂樺害搴斿湪 [Trigger, CommitDeadline]锛?
	if err := w.submitCommitment(ctx, session); err != nil {
		w.Logger.Error("[TransitionWorker] submitCommitment failed: %v", err)
		return
	}

	// 鈴?蹇呴』绛?CommitDeadline 鍚屾鎴愬姛涓?> 0 鎵嶈兘缁х画锛屽惁鍒欎細璺宠繃楂樺害妫€鏌?
	if session.CommitDeadline == 0 {
		w.Logger.Info("[TransitionWorker] session %s CommitDeadline is 0, waiting for sync...", session.SessionID)
		for {
			transitionKey := keys.KeyFrostVaultTransition(session.Chain, session.VaultID, session.EpochID)
			if data, exists, err := w.stateReader.Get(transitionKey); err == nil && exists {
				var transition pb.VaultTransitionState
				if err := proto.Unmarshal(data, &transition); err == nil && transition.DkgCommitDeadline > 0 {
					session.CommitDeadline = transition.DkgCommitDeadline
					session.SharingDeadline = transition.DkgSharingDeadline
					session.DisputeDeadline = transition.DkgDisputeDeadline
					w.Logger.Info("[TransitionWorker] session %s deadlines synced: commit=%d", session.SessionID, session.CommitDeadline)
					break
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(1 * time.Second):
			}
		}
	}

	// 绛夊緟鐩村埌杩涘叆 Sharing 闃舵 (height > CommitDeadline)
	w.Logger.Info("[TransitionWorker] session %s waiting for sharing stage: height > %d", session.SessionID, session.CommitDeadline)
	w.waitForHeight(ctx, session.CommitDeadline+1)

	// Phase 2: 绛夊緟鎵€鏈?commitment 骞舵彁浜?share
	// 姝ゆ椂楂樺害搴斿湪 (CommitDeadline, SharingDeadline]
	if err := w.submitShares(ctx, session); err != nil {
		w.Logger.Error("[TransitionWorker] submitShares failed: %v", err)
		return
	}

	// 鈴?绛夊緟 Sharing/Dispute 闃舵缁撴潫 (蹇呴』绛夐珮搴﹁秴杩?Deadline)
	if session.DisputeDeadline > 0 {
		w.Logger.Info("[TransitionWorker] session %s waiting for dispute window to close (waiting for height %d)...", session.SessionID, session.DisputeDeadline+1)
		w.waitForHeight(ctx, session.DisputeDeadline+1)
	} else {
		time.Sleep(20 * time.Second)
	}

	// Phase 3: 鏀堕泦 share 骞剁敓鎴愬瘑閽?
	// 姝ゆ椂楂樺害搴斿湪 DisputeDeadline 涔嬪悗
	if err := w.generateKey(ctx, session); err != nil {
		w.Logger.Error("[TransitionWorker] generateKey failed: %v", err)
		return
	}

	// Phase 4: 鎻愪氦楠岃瘉绛惧悕
	if err := w.submitValidation(ctx, session); err != nil {
		w.Logger.Error("[TransitionWorker] submitValidation failed: %v", err)
		return
	}

	w.Logger.Info("[TransitionWorker] DKG session %s completed", session.SessionID)
}

// GetCurrentHeight 鑾峰彇褰撳墠鍖哄潡楂樺害
func (w *TransitionWorker) GetCurrentHeight() uint64 {
	data, exists, err := w.stateReader.Get(keys.KeyLatestHeight())
	if err != nil || !exists || len(data) == 0 {
		return 0
	}
	// 楂樺害鍦ㄦ暟鎹簱涓互瀛楃涓插舰寮忓瓨鍌?(strconv.FormatUint)
	h, _ := strconv.ParseUint(string(data), 10, 64)
	return h
}

// waitForHeight 闃诲鐩村埌杈惧埌鎸囧畾楂樺害
func (w *TransitionWorker) waitForHeight(ctx context.Context, targetHeight uint64) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		current := w.GetCurrentHeight()
		if current >= targetHeight {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			continue
		}
	}
}

// submitCommitment 鎻愪氦 DKG 鎵胯
func (w *TransitionWorker) submitCommitment(ctx context.Context, session *DKGSession) error {
	w.Logger.Info("[TransitionWorker] submitCommitment session=%s", session.SessionID)

	// 鑾峰彇 DKG 鎵ц鍣?
	dkgExec, err := w.cryptoFactory.NewDKGExecutor(int32(session.SignAlgo))
	if err != nil {
		return fmt.Errorf("create dkg executor: %w", err)
	}

	// 鐢熸垚闅忔満 t-1 闃跺椤瑰紡 f(x) = a_0 + a_1*x + ... + a_(t-1)*x^(t-1)
	// a_0 鏄湰鑺傜偣鐨勭瀵嗚础鐚?
	poly, err := dkgExec.GeneratePolynomial(session.Threshold)
	if err != nil {
		return fmt.Errorf("generate polynomial: %w", err)
	}
	session.Polynomial = poly

	// 璁＄畻 Feldman VSS 鎵胯鐐癸細A_ik = g^{a_k}
	commitmentPoints := dkgExec.ComputeCommitments(poly)

	// AI0 = A_i0 = g^{a_0}锛堢涓€涓壙璇虹偣锛?
	ai0 := commitmentPoints[0]

	// 棰勮绠楀彂閫佺粰姣忎釜鎺ユ敹鑰呯殑 share锛歠(receiver_index)
	session.LocalShares = make(map[int][]byte)
	session.EncRands = make(map[int][]byte)
	for idx := 1; idx <= len(session.Committee); idx++ {
		// 閫氳繃鎺ュ彛璁＄畻 share
		shareBytes := dkgExec.EvaluateShare(poly, idx)
		session.LocalShares[idx] = shareBytes

		// 鐢熸垚鍔犲瘑闅忔満鏁帮紙鐢ㄤ簬 ECIES 鍔犲瘑鍜屾綔鍦ㄧ殑 reveal锛?
		encRand := make([]byte, 32)
		// 浣跨敤 sha256 鍝堝笇鐢熸垚闅忔満鏁帮紙瀹為檯搴旂敤闇€瑕佹洿瀹夊叏鐨勬柟娉曪級
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

// submitShares 鎻愪氦 DKG shares
func (w *TransitionWorker) submitShares(ctx context.Context, session *DKGSession) error {
	w.Logger.Debug("[TransitionWorker] submitShares session=%s", session.SessionID)

	// 涓烘瘡涓鍛樹細鎴愬憳鎻愪氦鍔犲瘑鐨?share
	for idx, receiverID := range session.Committee {
		receiverIndex := idx + 1 // 1-based index

		receiverPubKey, err := w.getReceiverPubKey(session, receiverID)
		if err != nil {
			w.Logger.Warn("[TransitionWorker] cannot get pubkey for %s: %v", receiverID, err)
			continue
		}

		// 鑾峰彇棰勮绠楃殑 share 鍜屽姞瀵嗛殢鏈烘暟
		shareBytes := session.LocalShares[receiverIndex]
		encRand := session.EncRands[receiverIndex]
		w.Logger.Debug("[TransitionWorker] dkg share mapping session=%s chain=%s vault=%d epoch=%d dealer=%s dealer_index=%d receiver=%s receiver_index=%d share_fp=%s rand_fp=%s",
			session.SessionID, session.Chain, session.VaultID, session.EpochID, w.localAddress, session.MyIndex, receiverID, receiverIndex, shareFingerprintForLog(shareBytes), shareFingerprintForLog(encRand))

		// ECIES 鍔犲瘑
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

// generateKey 鏀堕泦 shares 骞剁敓鎴愭湰鍦板瘑閽?
func (w *TransitionWorker) generateKey(ctx context.Context, session *DKGSession) error {
	w.Logger.Debug("[TransitionWorker] generateKey session=%s", session.SessionID)

	// 鑾峰彇 DKG 鎵ц鍣?
	dkgExec, err := w.cryptoFactory.NewDKGExecutor(int32(session.SignAlgo))
	if err != nil {
		return fmt.Errorf("create dkg executor: %w", err)
	}

	// 1) 璁＄畻鏈湴瀵硅嚜宸辩殑澶氶」寮忎唤棰濓紙dealer 涓鸿嚜宸憋紝receiver 涓鸿嚜宸辩储寮曪級
	myShareBytes := dkgExec.EvaluateShare(session.Polynomial, session.MyIndex)
	w.Logger.Debug("[TransitionWorker] dkg local polynomial share session=%s chain=%s vault=%d epoch=%d node=%s my_index=%d share_fp=%s",
		session.SessionID, session.Chain, session.VaultID, session.EpochID, w.localAddress, session.MyIndex, shareFingerprintForLog(myShareBytes))

	// 2) 鏀堕泦骞惰В瀵嗘墍鏈夊彂缁欐湰鑺傜偣鐨?shares锛岄獙璇佸悗鑱氬悎
	decryptedShares, err := w.collectAndVerifyShares(ctx, dkgExec, session)
	if err != nil {
		return fmt.Errorf("collect shares: %w", err)
	}
	for _, dealerID := range session.Committee {
		if dealerID == w.localAddress {
			continue
		}
		shareBytes := decryptedShares[dealerID]
		w.Logger.Debug("[TransitionWorker] dkg decrypted share session=%s chain=%s vault=%d epoch=%d dealer=%s receiver=%s receiver_index=%d share_fp=%s",
			session.SessionID, session.Chain, session.VaultID, session.EpochID, dealerID, w.localAddress, session.MyIndex, shareFingerprintForLog(shareBytes))
	}

	// 鑱氬悎鎵€鏈?share锛歴_i = 危_j f_j(i)
	allShares := make([][]byte, 0, 1+len(decryptedShares))
	allShares = append(allShares, myShareBytes)
	for _, s := range decryptedShares {
		allShares = append(allShares, s)
	}
	aggShare := dkgExec.AggregateShares(allShares)
	w.Logger.Debug("[TransitionWorker] dkg aggregate share input summary session=%s chain=%s vault=%d epoch=%d input_count=%d committee=%s",
		session.SessionID, session.Chain, session.VaultID, session.EpochID, len(allShares), committeeMembersForLog(session.Committee))

	session.LocalShare = new(big.Int).SetBytes(aggShare)
	session.LocalShareBytes = aggShare
	w.Logger.Debug("[TransitionWorker] dkg aggregate share result session=%s chain=%s vault=%d epoch=%d node=%s my_index=%d agg_share_fp=%s",
		session.SessionID, session.Chain, session.VaultID, session.EpochID, w.localAddress, session.MyIndex, shareFingerprintForLog(aggShare))
	if w.localShareStore != nil {
		shareCopy := make([]byte, len(aggShare))
		copy(shareCopy, aggShare)
		if err := w.localShareStore.SaveLocalShare(session.Chain, session.VaultID, session.EpochID, shareCopy); err != nil {
			w.Logger.Warn("[TransitionWorker] failed to persist local share for %s/%d/%d: %v",
				session.Chain, session.VaultID, session.EpochID, err)
		} else {
			w.Logger.Debug("[TransitionWorker] persisted aggregated local share session=%s chain=%s vault=%d epoch=%d key=%s share_fp=%s",
				session.SessionID, session.Chain, session.VaultID, session.EpochID,
				keys.KeyFrostLocalShare(session.Chain, session.VaultID, session.EpochID), shareFingerprintForLog(shareCopy))
		}
	}

	// 3) 鑱氬悎 group_pubkey = 危 A_j0锛堟敹闆嗘墍鏈?dealer 鐨?a_i0锛?
	groupPubkey, err := w.aggregateGroupPubkey(ctx, session)
	if err != nil {
		return fmt.Errorf("aggregate group pubkey: %w", err)
	}
	session.GroupPubkey = groupPubkey
	w.Logger.Debug("[TransitionWorker] dkg group pubkey ready session=%s chain=%s vault=%d epoch=%d group_pubkey_fp=%s",
		session.SessionID, session.Chain, session.VaultID, session.EpochID, shareFingerprintForLog(groupPubkey))

	session.Phase = "KEY_READY"
	w.Logger.Info("[TransitionWorker] generateKey completed: localShare=%x groupPubkey=%x",
		session.LocalShareBytes[:8], session.GroupPubkey[:8])
	return nil
}

// collectAndVerifyShares 鎵弿閾句笂瀵嗘枃 shares锛岃В瀵嗗睘浜庢湰鑺傜偣鐨勪唤棰濆苟鍋氫竴鑷存€ф牎楠?
func (w *TransitionWorker) collectAndVerifyShares(ctx context.Context, dkgExec DKGExecutor, session *DKGSession) (map[string][]byte, error) {
	result := make(map[string][]byte) // dealerID -> share

	commitments, err := w.loadDKGCommitments(session)
	if err != nil {
		return nil, err
	}

	// 1. 鎵弿璇?session 鐨勬墍鏈?DKG share锛堟寜鍓嶇紑锛?
	sharePrefix := fmt.Sprintf("v1_frost_vault_dkg_share_%s_%d_%s_", session.Chain, session.VaultID, padUint(session.EpochID))
	w.Logger.Debug("[TransitionWorker] collect shares context session=%s chain=%s vault=%d epoch=%d receiver=%s receiver_index=%d committee=%s share_prefix=%s",
		session.SessionID, session.Chain, session.VaultID, session.EpochID, w.localAddress, session.MyIndex, committeeMembersForLog(session.Committee), sharePrefix)

	// 瑙ｆ瀽鏈湴 secp256k1 绉侀挜锛堢敤浜?ECIES 瑙ｅ瘑锛?
	localPriv32, err := w.loadLocalDecryptPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("load local decrypt key: %w", err)
	}

	err = w.stateReader.Scan(sharePrefix, func(k string, v []byte) bool {
		// 鍙嶅簭鍒楀寲瀛樺偍鐨?share 璁板綍
		var share pb.FrostVaultDkgShare
		if err := proto.Unmarshal(v, &share); err != nil {
			w.Logger.Warn("[DKG] skip invalid share record: %v", err)
			return true
		}
		// 鍙鐞嗗彂缁欐湰鑺傜偣鐨勪唤棰?
		if share.ReceiverId != w.localAddress {
			return true
		}
		w.Logger.Debug("[TransitionWorker] collect share candidate session=%s chain=%s vault=%d epoch=%d dealer=%s receiver=%s key=%s ciphertext_len=%d",
			session.SessionID, session.Chain, session.VaultID, session.EpochID, share.DealerId, share.ReceiverId, k, len(share.Ciphertext))

		// 瑙ｅ瘑瀵嗘枃
		plain, err := security.ECIESDecrypt(localPriv32, share.Ciphertext)
		if err != nil {
			w.Logger.Warn("[DKG] decrypt share from %s failed: %v", share.DealerId, err)
			return true
		}

		commitment, exists := commitments[share.DealerId]
		if !exists || commitment == nil {
			w.Logger.Warn("[DKG] missing commitment for dealer=%s", share.DealerId)
			return true
		}

		// 楠岃瘉涓?dealer 鎵胯鐐逛竴鑷?
		if ok := dkgExec.VerifyShare(plain, commitment.CommitmentPoints, 0, session.MyIndex); !ok {
			w.Logger.Warn("[DKG] share verification failed for dealer=%s", share.DealerId)
			return true
		}

		// 璁板綍
		if prev, exists := result[share.DealerId]; exists {
			w.Logger.Warn("[TransitionWorker] duplicate share from dealer session=%s chain=%s vault=%d epoch=%d dealer=%s prev_fp=%s new_fp=%s",
				session.SessionID, session.Chain, session.VaultID, session.EpochID, share.DealerId, shareFingerprintForLog(prev), shareFingerprintForLog(plain))
		}
		result[share.DealerId] = plain
		w.Logger.Debug("[TransitionWorker] verified decrypted share session=%s chain=%s vault=%d epoch=%d dealer=%s receiver=%s receiver_index=%d share_fp=%s",
			session.SessionID, session.Chain, session.VaultID, session.EpochID, share.DealerId, w.localAddress, session.MyIndex, shareFingerprintForLog(plain))
		return true
	})
	if err != nil {
		return nil, err
	}

	missing := make([]string, 0)
	for _, dealerID := range session.Committee {
		if dealerID == w.localAddress {
			continue
		}
		if _, ok := result[dealerID]; !ok {
			missing = append(missing, dealerID)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing valid shares from dealers: %v", missing)
	}
	w.Logger.Debug("[TransitionWorker] collect shares completed session=%s chain=%s vault=%d epoch=%d received=%d expected=%d",
		session.SessionID, session.Chain, session.VaultID, session.EpochID, len(result), len(session.Committee)-1)

	return result, nil
}

func shareFingerprintForLog(data []byte) string {
	if len(data) == 0 {
		return "len=0"
	}
	sum := sha256.Sum256(data)
	prefixLen := 8
	if len(data) < prefixLen {
		prefixLen = len(data)
	}
	return fmt.Sprintf("len=%d,prefix=%x,sha256=%x", len(data), data[:prefixLen], sum[:8])
}

func committeeMembersForLog(committee []string) string {
	if len(committee) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(committee))
	for idx, member := range committee {
		parts = append(parts, fmt.Sprintf("%d:%s", idx+1, member))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func (w *TransitionWorker) loadLocalDecryptPrivateKey() ([]byte, error) {
	privStr := strings.TrimSpace(w.localPrivateKey)
	if privStr == "" {
		privStr = strings.TrimSpace(utils.GetKeyManager().GetPrivateKey())
	}
	if privStr == "" {
		return nil, fmt.Errorf("empty local private key")
	}

	privK, err := utils.ParseSecp256k1PrivateKey(privStr)
	if err != nil {
		return nil, fmt.Errorf("parse local private key: %w", err)
	}
	return privK.Serialize(), nil
}

func (w *TransitionWorker) loadDKGCommitments(session *DKGSession) (map[string]*pb.FrostVaultDkgCommitment, error) {
	commitPrefix := fmt.Sprintf("v1_frost_vault_dkg_commit_%s_%d_%s_", session.Chain, session.VaultID, padUint(session.EpochID))
	commitments := make(map[string]*pb.FrostVaultDkgCommitment, len(session.Committee))

	err := w.stateReader.Scan(commitPrefix, func(k string, v []byte) bool {
		dealerID := strings.TrimPrefix(k, commitPrefix)
		if dealerID == "" {
			return true
		}
		var commit pb.FrostVaultDkgCommitment
		if err := proto.Unmarshal(v, &commit); err != nil {
			w.Logger.Warn("[DKG] skip invalid commitment record for dealer=%s: %v", dealerID, err)
			return true
		}
		commitments[dealerID] = &commit
		return true
	})
	if err != nil {
		return nil, err
	}
	return commitments, nil
}

// aggregateGroupPubkey 鏀堕泦鎵€鏈?dealer 鐨?a_i0 鑱氬悎寰楀埌 group_pubkey
func (w *TransitionWorker) aggregateGroupPubkey(ctx context.Context, session *DKGSession) ([]byte, error) {
	dkgExec, err := w.cryptoFactory.NewDKGExecutor(int32(session.SignAlgo))
	if err != nil {
		return nil, err
	}
	commitments, err := w.loadDKGCommitments(session)
	if err != nil {
		return nil, err
	}
	w.Logger.Debug("[TransitionWorker] aggregate group pubkey context session=%s chain=%s vault=%d epoch=%d committee=%s commitment_count=%d",
		session.SessionID, session.Chain, session.VaultID, session.EpochID, committeeMembersForLog(session.Committee), len(commitments))
	ai0Points := make([][]byte, 0, len(session.Committee))
	missing := make([]string, 0)
	for _, dealerID := range session.Committee {
		commit, ok := commitments[dealerID]
		if !ok || commit == nil || len(commit.AI0) == 0 {
			missing = append(missing, dealerID)
			continue
		}
		ai0Points = append(ai0Points, commit.AI0)
		w.Logger.Debug("[TransitionWorker] aggregate group pubkey contributor session=%s dealer=%s ai0_fp=%s",
			session.SessionID, dealerID, shareFingerprintForLog(commit.AI0))
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing DKG commitments (AI0) from dealers: %v", missing)
	}
	if len(ai0Points) == 0 {
		return nil, fmt.Errorf("no AI0 commitments found")
	}
	groupPubkey := dkgExec.ComputeGroupPubkey(ai0Points)
	w.Logger.Debug("[TransitionWorker] aggregate group pubkey result session=%s group_pubkey_fp=%s", session.SessionID, shareFingerprintForLog(groupPubkey))
	return groupPubkey, nil
}

// submitValidation 鎻愪氦楠岃瘉绛惧悕
func (w *TransitionWorker) submitValidation(ctx context.Context, session *DKGSession) error {
	w.Logger.Debug("[TransitionWorker] submitValidation session=%s", session.SessionID)

	// 鏋勯€犻獙璇佹秷鎭細chain || vault_id || epoch_id || group_pubkey
	msgHash := computeValidationMsgHash(session.Chain, session.VaultID, session.EpochID, session.SignAlgo, session.GroupPubkey)

	// 鑾峰彇 DKG 鎵ц鍣?
	dkgExec, err := w.cryptoFactory.NewDKGExecutor(int32(session.SignAlgo))
	if err != nil {
		return fmt.Errorf("create dkg executor: %w", err)
	}

	// 浣跨敤鏈湴 share 鐢熸垚 Schnorr 绛惧悕
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

// computeValidationMsgHash 璁＄畻 DKG 楠岃瘉娑堟伅鍝堝笇锛堥渶涓?VM 瀹炵幇涓€鑷达級
func computeValidationMsgHash(chain string, vaultID uint32, epochID uint64, signAlgo pb.SignAlgo, groupPubkey []byte) []byte {
	h := sha256.New()
	h.Write([]byte("frost_vault_dkg_validation"))
	h.Write([]byte(chain))
	h.Write([]byte{byte(vaultID >> 24), byte(vaultID >> 16), byte(vaultID >> 8), byte(vaultID)})
	h.Write([]byte{byte(epochID >> 56), byte(epochID >> 48), byte(epochID >> 40), byte(epochID >> 32),
		byte(epochID >> 24), byte(epochID >> 16), byte(epochID >> 8), byte(epochID)})
	h.Write([]byte{byte(signAlgo)})
	h.Write(groupPubkey)
	return h.Sum(nil)
}

func (w *TransitionWorker) newBaseMessage() *pb.BaseMessage {
	// 鑾峰彇褰撳墠璐︽埛鐨?Nonce (浠庣姸鎬佹満璇诲彇)
	var currentNonce uint64
	accountKey := fmt.Sprintf("v1_account_%s", w.localAddress)
	if data, exists, err := w.stateReader.Get(accountKey); err == nil && exists {
		var acc pb.Account
		if err := proto.Unmarshal(data, &acc); err == nil {
			currentNonce = acc.Nonce
		}
	}

	// 绛栫暐锛氫娇鐢ㄧ姸鎬?Nonce + 杩涚▼鍐?Nonce 杩借釜锛岄槻姝㈠悓涓€楂樺害澶氱瑪浜ゆ槗鍐茬獊
	w.mu.Lock()
	defer w.mu.Unlock()

	nonce := currentNonce + 1
	if nonce <= w.lastIssuedNonce {
		nonce = w.lastIssuedNonce + 1
	}
	w.lastIssuedNonce = nonce

	w.Logger.Info("[TransitionWorker] newBaseMessage: sender=%s, base_nonce=%d, final_nonce=%d",
		w.localAddress, currentNonce, nonce)

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

// GetSession 鑾峰彇浼氳瘽鐘舵€?
func (w *TransitionWorker) GetSession(sessionID string) *DKGSession {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.sessions[sessionID]
}

// CheckTriggerConditions 妫€鏌ュ悇 Vault 鐨勮Е鍙戞潯浠讹紙鎸?Vault 鐙珛妫€娴嬶級
// 鎵弿 Top10000 鍙樺寲锛岃绠?change_ratio锛岃揪鍒伴槇鍊兼椂鍒涘缓 VaultTransitionState
func (w *TransitionWorker) CheckTriggerConditions(ctx context.Context, height uint64) error {
	w.Logger.Debug("[TransitionWorker] CheckTriggerConditions height=%d", height)

	// 1. 鑾峰彇褰撳墠 Top10000
	currentSigners, err := w.signerProvider.Top10000(height)
	if err != nil {
		return fmt.Errorf("get top10000: %w", err)
	}

	// 2. 閬嶅巻鎵€鏈夐摼鍜?Vault锛屾鏌ユ瘡涓?Vault 鐨勫鍛樹細鍙樺寲
	// 杩欓噷绠€鍖栧鐞嗭紝鍋囪鏀寔鐨勯摼浠庨厤缃鍙?
	chains := []string{"btc", "eth", "bnb"} // TODO: 浠庨厤缃鍙?

	for _, chain := range chains {
		// 鑾峰彇璇ラ摼鐨?Vault 閰嶇疆
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

		// 閬嶅巻璇ラ摼鐨勬墍鏈?Vault
		for vaultID := uint32(0); vaultID < vaultCfg.VaultCount; vaultID++ {
			// 妫€鏌?DKG 澶辫触骞惰Е鍙戦噸鍚?
			if err := w.checkAndRestartDKG(ctx, chain, vaultID, height); err != nil {
				w.Logger.Warn("[TransitionWorker] check DKG restart failed: chain=%s vault=%d: %v", chain, vaultID, err)
			}

			// 妫€鏌ヨЕ鍙戞潯浠?
			if err := w.checkVaultTrigger(ctx, chain, vaultID, height, currentSigners); err != nil {
				w.Logger.Warn("[TransitionWorker] check vault trigger failed: chain=%s vault=%d: %v", chain, vaultID, err)
				// 缁х画妫€鏌ュ叾浠?Vault
			}
		}
	}

	return nil
}

// StartPendingSessions scans transition states and starts DKG sessions when needed.
func (w *TransitionWorker) StartPendingSessions(ctx context.Context) error {
	// 浠呮壂鎻忔椿璺?transition 绱㈠紩锛岄伩鍏嶅叏閲忔壂鍘嗗彶 transition
	prefix := keys.KeyFrostVaultTransitionActivePrefix()

	count := 0
	err := w.stateReader.Scan(prefix, func(k string, v []byte) bool {
		count++
		transitionKey := string(v)
		if transitionKey == "" {
			w.Logger.Debug("[TransitionWorker] skip active transition index with empty value: %s", k)
			return true
		}

		transitionData, exists, err := w.stateReader.Get(transitionKey)
		if err != nil {
			w.Logger.Warn("[TransitionWorker] failed to load transition by index %s -> %s: %v", k, transitionKey, err)
			return true
		}
		if !exists || len(transitionData) == 0 {
			w.Logger.Debug("[TransitionWorker] transition missing for index %s -> %s", k, transitionKey)
			return true
		}

		var state pb.VaultTransitionState
		if err := proto.Unmarshal(transitionData, &state); err != nil {
			w.Logger.Error("[TransitionWorker] Failed to unmarshal transition state for key %s: %v", transitionKey, err)
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
	if count > 0 {
		w.Logger.Debug("[TransitionWorker] StartPendingSessions scanned %d transition keys", count)
	}
	return err
}

// checkVaultTrigger 妫€鏌ュ崟涓?Vault 鐨勮Е鍙戞潯浠?
func (w *TransitionWorker) checkVaultTrigger(ctx context.Context, chain string, vaultID uint32, height uint64, currentSigners []SignerInfo) error {
	// 1. 鍥哄畾杈圭晫妫€鏌ワ細杞崲鍙湪 epochBlocks 杈圭晫鐢熸晥
	// 璁＄畻褰撳墠楂樺害鏄惁鍦?Epoch 杈圭晫
	epochBoundary := (height / w.epochBlocks) * w.epochBlocks
	if height != epochBoundary {
		// 涓嶅湪杈圭晫锛屼笉瑙﹀彂杞崲
		return nil
	}

	// 2. 鑾峰彇褰撳墠 Vault 鐨?epoch
	currentEpoch := w.vaultProvider.VaultCurrentEpoch(chain, vaultID)

	// 3. 鑾峰彇褰撳墠鍜屼笂涓€涓?epoch 鐨勫鍛樹細
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
		// 濡傛灉涓婁竴涓?epoch 涓嶅瓨鍦紝璇存槑鏄娆★紝涓嶉渶瑕佽Е鍙?
		return nil
	}

	// 4. 璁＄畻 change_ratio锛堝鍛樹細鎴愬憳鍙樺寲姣斾緥锛?
	changeRatio := w.calculateChangeRatio(prevCommittee, currentCommittee)

	// 5. 搴旂敤 EWMA 骞虫粦锛堢淮鎶ゅ巻鍙茬姸鎬侊級
	ewmaKey := fmt.Sprintf("%s_%d", chain, vaultID)
	w.mu.Lock()
	prevEWMA, exists := w.ewmaHistory[ewmaKey]
	if !exists {
		// 棣栨璁＄畻锛岀洿鎺ヤ娇鐢ㄥ綋鍓嶅€?
		w.ewmaHistory[ewmaKey] = changeRatio
		prevEWMA = changeRatio
	} else {
		// EWMA: new_value = alpha * current + (1 - alpha) * prev
		newEWMA := w.ewmaAlpha*changeRatio + (1-w.ewmaAlpha)*prevEWMA
		w.ewmaHistory[ewmaKey] = newEWMA
		changeRatio = newEWMA // 浣跨敤骞虫粦鍚庣殑鍊?
	}
	w.mu.Unlock()

	w.Logger.Debug("[TransitionWorker] chain=%s vault=%d height=%d change_ratio=%.4f ewma=%.4f threshold=%.4f",
		chain, vaultID, height, changeRatio, prevEWMA, w.transitionThreshold)

	// 6. 妫€鏌ユ槸鍚﹁揪鍒伴槇鍊?
	if changeRatio >= w.transitionThreshold {
		w.Logger.Info("[TransitionWorker] trigger condition met: chain=%s vault=%d epoch=%d height=%d change_ratio=%.4f (ewma)",
			chain, vaultID, currentEpoch+1, height, changeRatio)

		// 鍒涘缓鏂扮殑 VaultTransitionState
		return w.createTransitionState(ctx, chain, vaultID, currentEpoch+1, height, prevCommittee, currentCommittee)
	}

	return nil
}

// checkAndRestartDKG 妫€鏌?DKG 澶辫触鐘舵€佸苟瑙﹀彂閲嶅惎
// 褰撴娴嬪埌 DkgStatusFailed 鏃讹紝鍒涘缓鏂扮殑 transition state 骞堕噸鍚?DKG
func (w *TransitionWorker) checkAndRestartDKG(ctx context.Context, chain string, vaultID uint32, height uint64) error {
	// 鑾峰彇褰撳墠 epoch
	currentEpoch := w.vaultProvider.VaultCurrentEpoch(chain, vaultID)

	// 妫€鏌ュ綋鍓?epoch 鐨?transition state
	transitionKey := fmt.Sprintf("v1_frost_vault_transition_%s_%d_%s", chain, vaultID, padUint(currentEpoch))
	transitionData, exists, err := w.stateReader.Get(transitionKey)
	if err != nil {
		return fmt.Errorf("get transition state: %w", err)
	}
	if !exists || len(transitionData) == 0 {
		return nil // 娌℃湁 transition state锛屾棤闇€閲嶅惎
	}

	var transition pb.VaultTransitionState
	if err := proto.Unmarshal(transitionData, &transition); err != nil {
		return fmt.Errorf("unmarshal transition: %w", err)
	}

	// 妫€鏌?DKG 鐘舵€佹槸鍚︿负 FAILED
	if transition.DkgStatus != "FAILED" {
		return nil // 涓嶆槸澶辫触鐘舵€侊紝鏃犻渶閲嶅惎
	}

	w.Logger.Info("[TransitionWorker] DKG failed detected: chain=%s vault=%d epoch=%d, triggering restart", chain, vaultID, currentEpoch)

	// 鑾峰彇瀹屾暣濮斿憳浼氾紙浠?Top10000 閲嶆柊鍒嗛厤锛?
	signers, err := w.signerProvider.Top10000(height)
	if err != nil {
		return fmt.Errorf("get top10000: %w", err)
	}

	// 鑾峰彇 VaultConfig
	vaultCfgKey := fmt.Sprintf("v1_frost_vault_cfg_%s_%d", chain, vaultID)
	vaultCfgData, exists, err := w.stateReader.Get(vaultCfgKey)
	if err != nil || !exists {
		return fmt.Errorf("vault config not found")
	}

	var vaultCfg pb.FrostVaultConfig
	if err := proto.Unmarshal(vaultCfgData, &vaultCfg); err != nil {
		return fmt.Errorf("unmarshal vault config: %w", err)
	}

	// 閲嶆柊璁＄畻濮斿憳浼氾紙浣跨敤鏂扮殑 epoch锛?
	newEpoch := currentEpoch + 1
	// 浣跨敤纭畾鎬у垎閰嶇畻娉曢噸鏂板垎閰嶅鍛樹細
	// 杩欓噷绠€鍖栧鐞嗭紝瀹為檯搴旇璋冪敤 committee.AssignToVaults
	// 涓轰簡绠€鍖栵紝鎴戜滑浣跨敤褰撳墠 Top10000 鐨勫墠 N 涓綔涓哄鍛樹細
	committeeSize := int(vaultCfg.CommitteeSize)
	if len(signers) < committeeSize {
		return fmt.Errorf("insufficient signers: have %d, need %d", len(signers), committeeSize)
	}

	// 鏋勫缓鏂扮殑濮斿憳浼氭垚鍛樺垪琛?
	newCommitteeMembers := make([]string, 0, committeeSize)
	for i := 0; i < committeeSize && i < len(signers); i++ {
		newCommitteeMembers = append(newCommitteeMembers, string(signers[i].ID))
	}

	// 璁＄畻鏂扮殑闂ㄩ檺
	thresholdRatio := float64(vaultCfg.ThresholdRatio)
	if thresholdRatio <= 0 || thresholdRatio > 1 {
		thresholdRatio = 0.67 // 榛樿 2/3
	}
	threshold := int(float64(len(newCommitteeMembers)) * thresholdRatio)
	if threshold < 1 {
		threshold = 1
	}

	// 鎻愪氦鏂扮殑 transition state 浜ゆ槗
	// 娉ㄦ剰锛氳繖閲岄渶瑕侀€氳繃 txSubmitter 鎻愪氦 transition state
	// 涓轰簡绠€鍖栵紝鎴戜滑鐩存帴鍚姩鏂扮殑 DKG 浼氳瘽锛宼ransition state 浼氬湪 DKG 娴佺▼涓垱寤?
	// TODO: 瀹炵幇 SubmitTransitionStateTx 鏂规硶浠ユ樉寮忓垱寤?transition state
	w.Logger.Info("[TransitionWorker] DKG restart: chain=%s vault=%d new_epoch=%d n=%d t=%d",
		chain, vaultID, newEpoch, len(newCommitteeMembers), threshold)

	// 鍚姩鏂扮殑 DKG 浼氳瘽
	if err := w.StartSession(ctx, chain, vaultID, newEpoch, vaultCfg.SignAlgo); err != nil {
		return fmt.Errorf("start new DKG session: %w", err)
	}

	return nil
}

// calculateChangeRatio 璁＄畻濮斿憳浼氬彉鍖栨瘮渚?
func (w *TransitionWorker) calculateChangeRatio(oldCommittee, newCommittee []SignerInfo) float64 {
	if len(oldCommittee) == 0 {
		return 1.0 // 鍏ㄦ柊濮斿憳浼氾紝100% 鍙樺寲
	}

	// 鏋勫缓鏃у鍛樹細鐨勫湴鍧€闆嗗悎
	oldSet := make(map[string]bool)
	for _, member := range oldCommittee {
		oldSet[string(member.ID)] = true
	}

	// 璁＄畻鏂板鍛樹細涓笉鍦ㄦ棫濮斿憳浼氫腑鐨勬垚鍛樻暟閲?
	changedCount := 0
	for _, member := range newCommittee {
		if !oldSet[string(member.ID)] {
			changedCount++
		}
	}

	// change_ratio = 鍙樺寲鏁伴噺 / 鎬绘暟閲?
	return float64(changedCount) / float64(len(oldCommittee))
}

// createTransitionState 鍒涘缓 VaultTransitionState
func (w *TransitionWorker) createTransitionState(ctx context.Context, chain string, vaultID uint32, epochID uint64, triggerHeight uint64, oldCommittee, newCommittee []SignerInfo) error {
	// 妫€鏌ユ槸鍚﹀凡瀛樺湪 transition state
	transitionKey := fmt.Sprintf("v1_frost_vault_transition_%s_%d_%s", chain, vaultID, padUint(epochID))
	existing, exists, err := w.stateReader.Get(transitionKey)
	if err != nil {
		return fmt.Errorf("check existing transition: %w", err)
	}
	if exists && len(existing) > 0 {
		w.Logger.Debug("[TransitionWorker] transition state already exists: %s", transitionKey)
		return nil // 宸插瓨鍦紝骞傜瓑
	}

	// 鑾峰彇 VaultConfig 浠ョ‘瀹?sign_algo 鍜?threshold
	vaultCfgKey := fmt.Sprintf("v1_frost_vault_cfg_%s_%d", chain, vaultID)
	vaultCfgData, exists, err := w.stateReader.Get(vaultCfgKey)
	if err != nil || !exists {
		return fmt.Errorf("vault config not found")
	}

	var vaultCfg pb.FrostVaultConfig
	if err := proto.Unmarshal(vaultCfgData, &vaultCfg); err != nil {
		return fmt.Errorf("unmarshal vault config: %w", err)
	}

	// 璁＄畻闂ㄩ檺 t = ceil(K * threshold_ratio)
	threshold := int(float64(len(newCommittee)) * float64(vaultCfg.ThresholdRatio))
	if threshold < 1 {
		threshold = 1
	}

	// 鑾峰彇鏃?group_pubkey
	oldGroupPubkey, err := w.vaultProvider.VaultGroupPubkey(chain, vaultID, epochID-1)
	if err != nil {
		w.Logger.Warn("[TransitionWorker] failed to get old group pubkey: %v", err)
		oldGroupPubkey = nil
	}

	// 鏋勫缓鏃?鏂板鍛樹細鍦板潃鍒楄〃
	oldMembers := make([]string, len(oldCommittee))
	for i, m := range oldCommittee {
		oldMembers[i] = string(m.ID)
	}
	newMembers := make([]string, len(newCommittee))
	for i, m := range newCommittee {
		newMembers[i] = string(m.ID)
	}

	// 鍒涘缓 transition state
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
		DkgCommitDeadline:   triggerHeight + 100, // TODO: 浠庨厤缃鍙?
		DkgDisputeDeadline:  triggerHeight + 200, // TODO: 浠庨厤缃鍙?
		OldGroupPubkey:      oldGroupPubkey,
		ValidationStatus:    "NOT_STARTED",
		Lifecycle:           "ACTIVE",
	}

	// TODO: 杩欓噷搴旇閫氳繃 VM 浜ゆ槗鍒涘缓 transition state
	// 鐩墠 transition state 鐨勫垱寤哄簲璇ョ敱 VM 澶勭悊锛岃繖閲屽彧鏄Е鍙?DKG 浼氳瘽
	logs.Info("[TransitionWorker] transition state should be created by VM: chain=%s vault=%d epoch=%d (sign_algo=%v)",
		chain, vaultID, epochID, transition.SignAlgo)

	// 鍚姩 DKG 浼氳瘽
	return w.StartSession(ctx, chain, vaultID, epochID, vaultCfg.SignAlgo)
}

// padUint 灏?uint64 鏍煎紡鍖栦负鍥哄畾闀垮害鐨勫瓧绗︿覆锛堢敤浜?key锛?
func padUint(n uint64) string {
	return fmt.Sprintf("%020d", n)
}

// PlanMigrationJobs 瑙勫垝杩佺Щ Job锛堟壂鎻忚 Vault 鐨勮祫閲戯紝鐢熸垚杩佺Щ妯℃澘锛?
func (w *TransitionWorker) PlanMigrationJobs(ctx context.Context, chain string, vaultID uint32, epochID uint64) error {
	logs.Info("[TransitionWorker] PlanMigrationJobs: chain=%s vault=%d epoch=%d", chain, vaultID, epochID)

	// 1. 鑾峰彇 transition state锛岀‘璁?DKG 宸插畬鎴愶紙KEY_READY锛?
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

	// 2. 鎵弿璇?Vault 鐨勮祫閲戯紙FundsLedger锛?
	// 鏍规嵁閾剧被鍨嬮€夋嫨涓嶅悓鐨勮縼绉荤瓥鐣?
	switch chain {
	case "btc":
		return w.planBTCMigrationJobs(ctx, chain, vaultID, epochID, &transition)
	case "eth", "bnb", "trx", "sol":
		return w.planContractMigrationJobs(ctx, chain, vaultID, epochID, &transition)
	default:
		return fmt.Errorf("unsupported chain: %s", chain)
	}
}

// planBTCMigrationJobs 瑙勫垝 BTC 杩佺Щ Job锛坰weep 浜ゆ槗锛?
// 鎵弿璇?Vault 鐨勬墍鏈?UTXO锛岀敓鎴?sweep 妯℃澘锛堟墍鏈?UTXO -> 鏂板湴鍧€锛?
func (w *TransitionWorker) planBTCMigrationJobs(ctx context.Context, chain string, vaultID uint32, epochID uint64, transition *pb.VaultTransitionState) error {
	// 1. 鎵弿璇?Vault 鐨勬墍鏈?UTXO锛堟湭閿佸畾鐨勶級
	utxoPrefix := fmt.Sprintf("v1_frost_btc_utxo_%d_", vaultID)
	utxos := make([]UTXO, 0)

	err := w.stateReader.Scan(utxoPrefix, func(k string, v []byte) bool {
		// 瑙ｆ瀽 UTXO key: v1_frost_btc_utxo_<vault_id>_<txid>_<vout>
		parts := strings.Split(k, "_")
		if len(parts) < 6 {
			return true // 缁х画鎵弿
		}
		txid := parts[4]
		voutStr := parts[5]
		vout, err := strconv.ParseUint(voutStr, 10, 32)
		if err != nil {
			return true
		}

		// 妫€鏌ユ槸鍚﹀凡閿佸畾锛堟鍦ㄦ彁鐜颁腑锛?
		lockKey := fmt.Sprintf("v1_frost_btc_locked_utxo_%d_%s_%d", vaultID, txid, vout)
		_, locked, _ := w.stateReader.Get(lockKey)
		if locked {
			return true // 宸查攣瀹氾紝璺宠繃
		}

		// 瑙ｆ瀽 UTXO 淇℃伅锛堜粠 v 鎴栦粠 RechargeRequest 鑾峰彇锛?
		// TODO: 浠?v 涓В鏋愬畬鏁寸殑 UTXO 淇℃伅锛坅mount, scriptPubKey, confirmHeight锛?
		// 杩欓噷绠€鍖栧鐞嗭紝鍋囪鍙互浠庡叾浠栧湴鏂硅幏鍙?
		utxo := UTXO{
			TxID:          txid,
			Vout:          uint32(vout),
			Amount:        0, // TODO: 浠庡疄闄呮暟鎹В鏋?
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

	// 2. 鑾峰彇鏂板湴鍧€锛堜粠鏂?group_pubkey 娲剧敓锛?
	newGroupPubkey := transition.NewGroupPubkey
	if len(newGroupPubkey) == 0 {
		return fmt.Errorf("new group pubkey not ready")
	}

	// TODO: 浠?group_pubkey 娲剧敓 BTC Taproot 鍦板潃
	// newAddress := deriveBTCTaprootAddress(newGroupPubkey)
	// 杩欓噷绠€鍖栧鐞嗭紝鍋囪鍙互浠?VaultState 鑾峰彇鏂板湴鍧€
	newAddress := "" // TODO: 浠庢柊 VaultState 鑾峰彇鍦板潃

	// 3. 璁＄畻鎬婚噾棰濆拰鎵嬬画璐?
	var totalAmount uint64
	for _, utxo := range utxos {
		totalAmount += utxo.Amount
	}
	// 浼扮畻鎵嬬画璐癸紙绠€鍖栵細鍩轰簬 input/output 鏁伴噺锛?
	fee := estimateBTCMigrationFee(len(utxos), 1) // 1 涓緭鍑猴紙鏂板湴鍧€锛?
	changeAmount := uint64(0)
	if totalAmount > fee {
		changeAmount = totalAmount - fee
		// 濡傛灉鎵鹃浂灏忎簬绮夊皹闄愬埗锛屼笉鍒涘缓鎵鹃浂杈撳嚭
		if changeAmount < 546 { // DustLimit
			changeAmount = 0
		}
	}

	// 4. 鑾峰彇閾鹃€傞厤鍣ㄥ苟鏋勫缓杩佺Щ妯℃澘
	// 浣跨敤 WithdrawTemplateParams锛屼絾鐢ㄤ簬杩佺Щ鍦烘櫙
	adapter, err := w.getChainAdapter(chain)
	if err != nil {
		return fmt.Errorf("get chain adapter: %w", err)
	}

	params := WithdrawTemplateParams{
		Chain:       chain,
		Asset:       "BTC",
		VaultID:     vaultID,
		KeyEpoch:    epochID - 1, // 浣跨敤鏃?key_epoch锛堣縼绉绘椂浣跨敤鏃у瘑閽ョ鍚嶏級
		WithdrawIDs: []string{},  // 杩佺Щ娌℃湁 withdraw_id
		Inputs:      utxos,
		Outputs: []WithdrawOutput{
			{
				WithdrawID: fmt.Sprintf("migration_%d_%d", vaultID, epochID),
				To:         newAddress,
				Amount:     changeAmount,
			},
		},
		Fee:          fee,
		ChangeAmount: 0, // 涓嶅垱寤烘壘闆讹紙鍏ㄩ儴杩佺Щ鍒版柊鍦板潃锛?
	}

	result, err := adapter.BuildWithdrawTemplate(params)
	if err != nil {
		return fmt.Errorf("build migration template: %w", err)
	}

	// 5. 鍒涘缓 MigrationJob 璁板綍锛堥€氳繃 VM 浜ゆ槗鎴栫洿鎺ュ瓨鍌級
	// TODO: 鍒涘缓 MigrationJob 骞跺惎鍔?ROAST 绛惧悕浼氳瘽
	// 杩欓噷绠€鍖栧鐞嗭紝璁板綍鏃ュ織
	logs.Info("[TransitionWorker] planned BTC migration: vault=%d epoch=%d utxos=%d template_hash=%x",
		vaultID, epochID, len(utxos), result.TemplateHash)

	return nil
}

// estimateBTCMigrationFee 浼扮畻 BTC 杩佺Щ鎵嬬画璐?
func estimateBTCMigrationFee(inputCount, outputCount int) uint64 {
	// 绠€鍖栦及绠楋細姣忎釜 input 绾?148 bytes锛屾瘡涓?output 绾?34 bytes
	baseSize := 10
	inputSize := inputCount * 148
	outputSize := outputCount * 34
	witnessSize := inputCount * 64 // Taproot witness
	totalVBytes := baseSize + inputSize + outputSize + witnessSize/4
	feeRate := uint64(10) // sat/vbyte
	return uint64(totalVBytes) * feeRate
}

// getChainAdapter 鑾峰彇閾鹃€傞厤鍣?
func (w *TransitionWorker) getChainAdapter(chain string) (ChainAdapter, error) {
	if w.adapterFactory == nil {
		return nil, fmt.Errorf("chain adapter factory not available")
	}
	return w.adapterFactory.Adapter(chain)
}

// planContractMigrationJobs 瑙勫垝鍚堢害閾捐縼绉?Job锛坲pdatePubkey锛?
// 鐢熸垚 updatePubkey(new_pubkey, vault_id, epoch_id) 妯℃澘
func (w *TransitionWorker) planContractMigrationJobs(ctx context.Context, chain string, vaultID uint32, epochID uint64, transition *pb.VaultTransitionState) error {
	// 1. 鑾峰彇鏂?group_pubkey
	newGroupPubkey := transition.NewGroupPubkey
	if len(newGroupPubkey) == 0 {
		return fmt.Errorf("new group pubkey not ready")
	}

	// 2. 鑾峰彇鍚堢害鍦板潃锛堜粠 VaultConfig 鎴?VaultState 鑾峰彇锛?
	// TODO: 浠?VaultConfig 鑾峰彇鍚堢害鍦板潃
	contractAddr := "" // TODO: 浠庨厤缃幏鍙?

	// 3. 鏋勫缓 updatePubkey 鏂规硶璋冪敤
	// 鏂规硶绛惧悕锛歶pdatePubkey(bytes32 newPubkey, uint32 vaultId, uint64 epochId)
	// 鏂规硶 ID锛歬eccak256("updatePubkey(bytes32,uint32,uint64)")[:4]
	methodID := []byte{0x12, 0x34, 0x56, 0x78} // TODO: 璁＄畻瀹為檯鐨勬柟娉?ID

	// 4. 鏋勫缓 calldata锛圓BI 缂栫爜锛?
	// calldata = methodID + encode(newPubkey) + encode(vaultId) + encode(epochId)
	// TODO: 瀹炵幇 ABI 缂栫爜
	calldata := make([]byte, 0)
	calldata = append(calldata, methodID...)
	// 杩欓噷绠€鍖栧鐞嗭紝瀹為檯闇€瑕佸畬鏁寸殑 ABI 缂栫爜

	// 5. 鑾峰彇閾鹃€傞厤鍣ㄥ苟鏋勫缓杩佺Щ妯℃澘
	adapter, err := w.getChainAdapter(chain)
	if err != nil {
		return fmt.Errorf("get chain adapter: %w", err)
	}

	params := WithdrawTemplateParams{
		Chain:        chain,
		Asset:        "NATIVE", // 鍘熺敓甯?
		VaultID:      vaultID,
		KeyEpoch:     epochID - 1, // 浣跨敤鏃?key_epoch
		WithdrawIDs:  []string{},
		Outputs:      []WithdrawOutput{}, // updatePubkey 娌℃湁杈撳嚭
		ContractAddr: contractAddr,
		MethodID:     methodID,
	}

	result, err := adapter.BuildWithdrawTemplate(params)
	if err != nil {
		return fmt.Errorf("build migration template: %w", err)
	}

	// 6. 鍒涘缓 MigrationJob 璁板綍
	// TODO: 鍒涘缓 MigrationJob 骞跺惎鍔?ROAST 绛惧悕浼氳瘽
	logs.Info("[TransitionWorker] planned contract migration: chain=%s vault=%d epoch=%d template_hash=%x",
		chain, vaultID, epochID, result.TemplateHash)

	return nil
}
