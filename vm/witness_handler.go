// vm/witness_handler.go
// 见证者相关交易处理器
package vm

import (
	"crypto/sha256"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"dex/witness"
	"encoding/binary"
	"fmt"
	"strconv"

	"google.golang.org/protobuf/proto"
)

// DefaultVaultCount 榛樿姣忔潯閾剧殑 Vault 鏁伴噺
const DefaultVaultCount = 100

// allocateVaultID 纭畾鎬у垎閰?vault_id
// 浣跨敤 H(request_id) % vault_count 纭繚鐩稿悓 request_id 鎬绘槸鍒嗛厤鍒扮浉鍚?vault
func allocateVaultID(requestID string, vaultCount uint32) uint32 {
	if vaultCount == 0 {
		vaultCount = DefaultVaultCount
	}
	hash := sha256.Sum256([]byte(requestID))
	// 浣跨敤鍓?4 瀛楄妭浣滀负 uint32
	n := binary.BigEndian.Uint32(hash[:4])
	return n % vaultCount
}

// allocateVaultIDWithLifecycleCheck 纭畾鎬у垎閰?vault_id锛屽苟妫€鏌?Vault lifecycle
// 濡傛灉鍒嗛厤鐨?Vault 澶勪簬 DRAINING 鐘舵€侊紝灏濊瘯涓嬩竴涓彲鐢ㄧ殑 ACTIVE Vault
func allocateVaultIDWithLifecycleCheck(sv StateView, chain, requestID string, vaultCount uint32) (uint32, error) {
	if vaultCount == 0 {
		vaultCount = DefaultVaultCount
	}

	// 璁＄畻鍒濆 vault_id锛堢‘瀹氭€э級
	initialVaultID := allocateVaultID(requestID, vaultCount)

	// 妫€鏌ュ垵濮?Vault 鐨?lifecycle
	vaultStateKey := keys.KeyFrostVaultState(chain, initialVaultID)
	vaultStateData, exists, _ := sv.Get(vaultStateKey)
	if exists && len(vaultStateData) > 0 {
		var vaultState pb.FrostVaultState
		if err := unmarshalProtoCompat(vaultStateData, &vaultState); err == nil {
			// 妫€鏌?lifecycle锛堜粠 VaultTransitionState 鑾峰彇锛屾垨浠?VaultState.Status 鎺ㄦ柇锛?
			// 濡傛灉 Vault 澶勪簬 DRAINING 鐘舵€侊紝灏濊瘯涓嬩竴涓?ACTIVE Vault
			if vaultState.Status == VaultLifecycleDraining {
			// 如果 Vault 处于 DRAINING 状态，尝试下一个 ACTIVE Vault
				for offset := uint32(1); offset < vaultCount; offset++ {
					candidateID := (initialVaultID + offset) % vaultCount
					candidateKey := keys.KeyFrostVaultState(chain, candidateID)
					candidateData, candidateExists, _ := sv.Get(candidateKey)
					if candidateExists && len(candidateData) > 0 {
						var candidateState pb.FrostVaultState
						if err := unmarshalProtoCompat(candidateData, &candidateState); err == nil {
							if candidateState.Status == "ACTIVE" {
								return candidateID, nil
							}
						}
					} else {
						// 濡傛灉 Vault 涓嶅瓨鍦紝榛樿璁や负鏄?ACTIVE锛堟柊鍒涘缓鐨?Vault锛?
						return candidateID, nil
					}
				}
				// 濡傛灉鎵€鏈?Vault 閮芥槸 DRAINING锛岃繑鍥為敊璇?
				return 0, fmt.Errorf("no ACTIVE vault available for chain %s", chain)
			}
		}
	}

	// 鍒濆 Vault 鏄?ACTIVE 鎴栦笉瀛樺湪锛堥粯璁?ACTIVE锛?
	return initialVaultID, nil
}

// WitnessServiceAware 瑙佽瘉鑰呮湇鍔℃劅鐭ユ帴鍙?
// 瀹炵幇姝ゆ帴鍙ｇ殑 handler 鍙互鎺ユ敹 WitnessService 鐨勫紩鐢?
type WitnessServiceAware interface {
	SetWitnessService(svc *witness.Service)
}

// ==================== WitnessStakeTxHandler ====================

// ==================== WitnessStakeTxHandler ====================
type WitnessStakeTxHandler struct {
	witnessSvc *witness.Service
}

// SetWitnessService 璁剧疆瑙佽瘉鑰呮湇鍔?
func (h *WitnessStakeTxHandler) SetWitnessService(svc *witness.Service) {
	h.witnessSvc = svc
}

func (h *WitnessStakeTxHandler) Kind() string {
	return "witness_stake"
}

func (h *WitnessStakeTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	stakeTx, ok := tx.GetContent().(*pb.AnyTx_WitnessStakeTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a witness stake transaction"}, fmt.Errorf("not a witness stake transaction")
	}

	stake := stakeTx.WitnessStakeTx
	if stake == nil || stake.Base == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid witness stake transaction"}, fmt.Errorf("invalid witness stake transaction")
	}

	ws := make([]WriteOp, 0)
	address := stake.Base.FromAddress

	// 璇诲彇璐︽埛
	accountKey := keys.KeyAccount(address)
	accountData, accountExists, err := sv.Get(accountKey)
	if err != nil {
		return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "failed to read account"}, err
	}

	var account pb.Account
	if accountExists {
		if err := unmarshalProtoCompat(accountData, &account); err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "failed to parse account"}, err
		}
	} else {
		return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "account not found"}, fmt.Errorf("account not found")
	}

	// 璇诲彇瑙佽瘉鑰呬俊鎭?
	witnessKey := keys.KeyWitnessInfo(address)
	witnessData, witnessExists, _ := sv.Get(witnessKey)

	var witnessInfo pb.WitnessInfo
	if witnessExists {
		if err := unmarshalProtoCompat(witnessData, &witnessInfo); err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "failed to parse witness info"}, err
		}
	} else {
		witnessInfo = pb.WitnessInfo{Address: address, StakeAmount: "0", Status: pb.WitnessStatus_WITNESS_CANDIDATE}
	}

	amount, err := parsePositiveBalanceStrict("witness stake amount", stake.Amount)
	if err != nil {
		return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "invalid amount"}, fmt.Errorf("invalid amount")
	}

	// 浣跨敤鍒嗙瀛樺偍璇诲彇浣欓
	fbBalance := GetBalance(sv, address, "FB")

	if stake.Op == pb.OrderOp_ADD {
		// 浣跨敤 WitnessService 杩涜楠岃瘉锛堝鏋滃彲鐢級
		if h.witnessSvc != nil {
			if _, err := h.witnessSvc.ProcessStake(address, balanceToDecimal(amount)); err != nil {
				if err == witness.ErrWitnessAlreadyActive {
					logs.Warn("[WitnessStake] witness %s already active, treating as success (idempotent)", address)
				} else {
					return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: err.Error()}, err
				}
			}
		}

		balance, err := parseBalanceStrict("balance", fbBalance.Balance)
		if err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "invalid balance state"}, err
		}
		if balance.Cmp(amount) < 0 {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "insufficient balance"}, fmt.Errorf("insufficient balance")
		}
		// 浣跨敤瀹夊叏鍑忔硶
		newBalance, err := SafeSub(balance, amount)
		if err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "balance underflow"}, fmt.Errorf("balance underflow: %w", err)
		}
		lockedBalance, err := parseBalanceStrict("witness locked balance", fbBalance.WitnessLockedBalance)
		if err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "invalid locked balance state"}, err
		}
		// 浣跨敤瀹夊叏鍔犳硶妫€鏌ラ攣瀹氫綑棰濇孩鍑?
		newLocked, err := SafeAdd(lockedBalance, amount)
		if err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "locked balance overflow"}, fmt.Errorf("locked balance overflow: %w", err)
		}
		fbBalance.Balance = newBalance.String()
		fbBalance.WitnessLockedBalance = newLocked.String()

		currentStake, err := parseBalanceStrict("witness stake amount", witnessInfo.StakeAmount)
		if err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "invalid witness stake state"}, err
		}
		// 浣跨敤瀹夊叏鍔犳硶妫€鏌ヨ川鎶奸噾棰濇孩鍑?
		newStake, err := SafeAdd(currentStake, amount)
		if err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "stake amount overflow"}, fmt.Errorf("stake amount overflow: %w", err)
		}
		witnessInfo.StakeAmount = newStake.String()
		witnessInfo.Status = pb.WitnessStatus_WITNESS_ACTIVE
	} else {
		// 浣跨敤 WitnessService 杩涜楠岃瘉锛堝鏋滃彲鐢級
		if h.witnessSvc != nil {
			if _, err := h.witnessSvc.ProcessUnstake(address); err != nil {
				return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: err.Error()}, err
			}
		}

		if witnessInfo.Status != pb.WitnessStatus_WITNESS_ACTIVE {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "witness is not active"}, fmt.Errorf("witness is not active")
		}
		if len(witnessInfo.PendingTasks) > 0 {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "witness has pending tasks"}, fmt.Errorf("witness has pending tasks")
		}
		witnessInfo.Status = pb.WitnessStatus_WITNESS_UNSTAKING
		witnessInfo.UnstakeHeight = stake.Base.ExecutedHeight
	}

	// 淇濆瓨浣欓鏇存柊
	SetBalance(sv, address, "FB", fbBalance)
	balanceKey := keys.KeyBalance(address, "FB")
	balanceData, _, _ := sv.Get(balanceKey)
	ws = append(ws, WriteOp{Key: balanceKey, Value: balanceData, Category: "balance"})

	updatedAccountData, err := proto.Marshal(&account)
	if err != nil {
		return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "failed to marshal account"}, err
	}
	ws = append(ws, WriteOp{Key: accountKey, Value: updatedAccountData, Del: false, Category: "account"})

	witnessInfoData, err := proto.Marshal(&witnessInfo)
	if err != nil {
		return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "failed to marshal witness info"}, err
	}
	ws = append(ws, WriteOp{Key: witnessKey, Value: witnessInfoData, Del: false, Category: "witness"})

	historyKey := keys.KeyWitnessHistory(stake.Base.TxId)
	historyData, _ := proto.Marshal(stake)
	ws = append(ws, WriteOp{Key: historyKey, Value: historyData, Del: false, Category: "history"})

	// 鍚屾鍒?WitnessService 鍐呭瓨鐘舵€?
	if h.witnessSvc != nil {
		h.witnessSvc.LoadWitness(&witnessInfo)
	}

	return ws, &Receipt{TxID: stake.Base.TxId, Status: "SUCCEED", WriteCount: len(ws)}, nil
}

func (h *WitnessStakeTxHandler) Apply(tx *pb.AnyTx) error {
	return nil
}

// ==================== WitnessRequestTxHandler ====================

// WitnessRequestTxHandler 鍏ヨ处瑙佽瘉璇锋眰澶勭悊鍣?
type WitnessRequestTxHandler struct {
	witnessSvc *witness.Service
	VaultCount uint32
}

// SetWitnessService 璁剧疆瑙佽瘉鑰呮湇鍔?
func (h *WitnessRequestTxHandler) SetWitnessService(svc *witness.Service) {
	h.witnessSvc = svc
}

func (h *WitnessRequestTxHandler) Kind() string {
	return "witness_request"
}

func (h *WitnessRequestTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	requestTx, ok := tx.GetContent().(*pb.AnyTx_WitnessRequestTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a witness request transaction"}, fmt.Errorf("not a witness request transaction")
	}

	request := requestTx.WitnessRequestTx
	if request == nil || request.Base == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid witness request transaction"}, fmt.Errorf("invalid witness request transaction")
	}

	ws := make([]WriteOp, 0)
	requestID := request.Base.TxId

	existingKey := keys.KeyRechargeRequest(requestID)
	requestData, exists, _ := sv.Get(existingKey)
	if exists {
		// 瀹归敊澶勭悊锛氬鏋滆姹傚凡瀛樺湪锛屼笖鐘舵€佹槸娲昏穬鐨勬垨鑰呮槸宸插畬鎴愮殑锛屽厑璁歌繖绗斾氦鏄撲綔涓衡€滅┖鎿嶄綔鈥濇垚鍔燂紝鎴栬€呰繑鍥?FAILED 鍑瘉浣嗕笉鎶ラ敊
		// 鍦ㄥ尯鍧楅摼涓紝閲嶅鐨勪氦鏄?hash 鏈韩涓嶅簲璇ヨ兘杩涘叆姹犲瓙锛屼絾濡傛灉鐢变簬鍒嗗弶绛夊師鍥犺繘鏉ヤ簡锛屾垜浠繑鍥?FAILED 鍑嵁鑰屼笉鏄?error
		return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: "request already exists"}, nil
	}

	nativeTxKey := keys.KeyRechargeRequestByNativeTx(request.NativeChain, request.NativeTxHash)
	_, nativeExists, _ := sv.Get(nativeTxKey)
	if nativeExists {
		return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: "native tx already used"}, nil
	}

	tokenKey := keys.KeyToken(request.TokenAddress)
	_, tokenExists, _ := sv.Get(tokenKey)
	if !tokenExists {
		return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: "token not found"}, fmt.Errorf("token not found")
	}

	var rechargeRequest *pb.RechargeRequest

	// 浣跨敤 WitnessService 鍒涘缓璇锋眰锛堝寘鍚璇佽€呴€夋嫨锛?
	if h.witnessSvc != nil {
		var err error
		rechargeRequest, err = h.witnessSvc.CreateRechargeRequest(request)
		if err != nil {
			return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: err.Error()}, err
		}
	} else {
		// 闄嶇骇妯″紡锛氫笉浣跨敤 WitnessService
		rechargeRequest = &pb.RechargeRequest{
			RequestId:        requestID,
			NativeChain:      request.NativeChain,
			NativeTxHash:     request.NativeTxHash,
			NativeVout:       request.NativeVout,
			NativeScript:     request.NativeScript,
			TokenAddress:     request.TokenAddress,
			Amount:           request.Amount,
			ReceiverAddress:  request.ReceiverAddress,
			RequesterAddress: request.Base.FromAddress,
			Status:           pb.RechargeRequestStatus_RECHARGE_PENDING,
			CreateHeight:     request.Base.ExecutedHeight,
			RechargeFee:      request.RechargeFee,
		}
	}

	// 纭畾鎬у垎閰?vault_id锛岄伩鍏嶈法 Vault 娣风敤璧勯噾
	// 娉ㄦ剰锛氶渶瑕佹鏌?Vault lifecycle锛孌RAINING 鐨?Vault 涓嶅啀鍒嗛厤鏂板叆璐?
	vCount := h.VaultCount
	if vCount == 0 {
		vCount = DefaultVaultCount
	}
	vaultID, err := allocateVaultIDWithLifecycleCheck(sv, request.NativeChain, requestID, vCount)
	if err != nil {
		return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: err.Error()}, err
	}
	rechargeRequest.VaultId = vaultID

	// 搴忓垪鍖栨渶缁堢殑璇锋眰鏁版嵁
	requestData, err = proto.Marshal(rechargeRequest)
	if err != nil {
		return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: "failed to marshal request"}, err
	}
	ws = append(ws, WriteOp{Key: existingKey, Value: requestData, Del: false, Category: "witness_request"})
	ws = append(ws, WriteOp{Key: nativeTxKey, Value: []byte(requestID), Del: false, Category: "index"})

	pendingHeight := rechargeRequest.CreateHeight
	if pendingHeight == 0 {
		pendingHeight = request.Base.ExecutedHeight
	}

	pendingSeqKey := keys.KeyFrostFundsPendingLotSeq(request.NativeChain, request.TokenAddress, vaultID, pendingHeight)
	pendingSeq := readUintSeq(sv, pendingSeqKey)
	pendingIndexKey := keys.KeyFrostFundsPendingLotIndex(request.NativeChain, request.TokenAddress, vaultID, pendingHeight, pendingSeq)
	pendingRefKey := keys.KeyFrostFundsPendingLotRef(requestID)

	ws = append(ws, WriteOp{Key: pendingIndexKey, Value: []byte(requestID), Del: false, Category: "frost_funds_pending"})
	ws = append(ws, WriteOp{Key: pendingSeqKey, Value: []byte(strconv.FormatUint(pendingSeq+1, 10)), Del: false, Category: "frost_funds_pending"})
	ws = append(ws, WriteOp{Key: pendingRefKey, Value: []byte(pendingIndexKey), Del: false, Category: "frost_funds_pending"})

	return ws, &Receipt{TxID: requestID, Status: "SUCCEED", WriteCount: len(ws)}, nil
}

func (h *WitnessRequestTxHandler) Apply(tx *pb.AnyTx) error {
	return nil
}

// ==================== WitnessVoteTxHandler ====================

// WitnessVoteTxHandler 瑙佽瘉鎶曠エ澶勭悊鍣?
type WitnessVoteTxHandler struct {
	witnessSvc *witness.Service
}

// SetWitnessService 璁剧疆瑙佽瘉鑰呮湇鍔?
func (h *WitnessVoteTxHandler) SetWitnessService(svc *witness.Service) {
	h.witnessSvc = svc
}

func (h *WitnessVoteTxHandler) Kind() string {
	return "witness_vote"
}

func (h *WitnessVoteTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	voteTx, ok := tx.GetContent().(*pb.AnyTx_WitnessVoteTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a witness vote transaction"}, fmt.Errorf("not a witness vote transaction")
	}

	vote := voteTx.WitnessVoteTx
	if vote == nil || vote.Base == nil || vote.Vote == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid witness vote transaction"}, fmt.Errorf("invalid witness vote transaction")
	}

	ws := make([]WriteOp, 0)
	requestID := vote.Vote.RequestId
	witnessAddr := vote.Vote.WitnessAddress

	requestKey := keys.KeyRechargeRequest(requestID)
	requestData, requestExists, err := sv.Get(requestKey)
	if err != nil || !requestExists {
		// 濡傛灉璇锋眰鎵句笉鍒帮紝杩斿洖 FAILED 鍑嵁锛屼絾涓嶈繑鍥?error
		// 杩欐牱鍙互閬垮厤鏁寸瑪浜ゆ槗瀵艰嚧鍖哄潡楠岃瘉澶辫触锛屼粠鑰岃В鍐冲叡璇嗗仠婊為棶棰?
		return nil, &Receipt{TxID: vote.Base.TxId, Status: "FAILED", Error: "request not found"}, nil
	}

	var request pb.RechargeRequest
	if err := unmarshalProtoCompat(requestData, &request); err != nil {
		return nil, &Receipt{TxID: vote.Base.TxId, Status: "FAILED", Error: "failed to parse request"}, err
	}

	voteKey := keys.KeyWitnessVote(requestID, witnessAddr)
	_, voteExists, _ := sv.Get(voteKey)
	if voteExists {
		return nil, &Receipt{TxID: vote.Base.TxId, Status: "FAILED", Error: "duplicate vote"}, nil
	}

	// 4. 璋冪敤 WitnessService 鏇存柊鐘舵€侊紙鍐呭瓨锛?
	var finalRequest *pb.RechargeRequest
	if h.witnessSvc != nil {
	// 4. 调用 WitnessService 更新状态（内存）
		vote.Vote.TxId = vote.Base.TxId
		updatedRequest, err := h.witnessSvc.ProcessVote(vote.Vote)
		if err != nil {
			// 涓氬姟閫昏緫閿欒锛堜緥濡傜姸鎬佷笉瀵癸級涓嶅簲璇ュ簾鎺夋暣涓尯鍧?
			return nil, &Receipt{TxID: vote.Base.TxId, Status: "FAILED", Error: err.Error()}, nil
		}
		finalRequest = updatedRequest
	} else {

		// 闄嶇骇锛氭墜鍔ㄦ洿鏂帮紙浠呯敤浜庢祴璇曪級
		request.Votes = append(request.Votes, vote.Vote)
		switch vote.Vote.VoteType {
		case pb.WitnessVoteType_VOTE_PASS:
			request.PassCount++
		case pb.WitnessVoteType_VOTE_FAIL:
			request.FailCount++
		case pb.WitnessVoteType_VOTE_ABSTAIN:
			request.AbstainCount++
		}
		finalRequest = &request
	}

	voteData, err := proto.Marshal(vote.Vote)
	if err != nil {
		return nil, &Receipt{TxID: vote.Base.TxId, Status: "FAILED", Error: "failed to marshal vote"}, err
	}
	ws = append(ws, WriteOp{Key: voteKey, Value: voteData, Del: false, Category: "witness_vote"})

	updatedRequestData, err := proto.Marshal(finalRequest)
	if err != nil {
		return nil, &Receipt{TxID: vote.Base.TxId, Status: "FAILED", Error: "failed to marshal request"}, err
	}
	ws = append(ws, WriteOp{Key: requestKey, Value: updatedRequestData, Del: false, Category: "witness_request"})

	return ws, &Receipt{TxID: vote.Base.TxId, Status: "SUCCEED", WriteCount: len(ws)}, nil
}

func (h *WitnessVoteTxHandler) Apply(tx *pb.AnyTx) error {
	return nil
}

// ==================== WitnessChallengeTxHandler ====================

// WitnessChallengeTxHandler 鎸戞垬浜ゆ槗澶勭悊鍣?
type WitnessChallengeTxHandler struct {
	witnessSvc *witness.Service
}

// SetWitnessService 璁剧疆瑙佽瘉鑰呮湇鍔?
func (h *WitnessChallengeTxHandler) SetWitnessService(svc *witness.Service) {
	h.witnessSvc = svc
}

func (h *WitnessChallengeTxHandler) Kind() string {
	return "witness_challenge"
}

func (h *WitnessChallengeTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	challengeTx, ok := tx.GetContent().(*pb.AnyTx_WitnessChallengeTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a witness challenge transaction"}, fmt.Errorf("not a witness challenge transaction")
	}

	challenge := challengeTx.WitnessChallengeTx
	if challenge == nil || challenge.Base == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid witness challenge transaction"}, fmt.Errorf("invalid witness challenge transaction")
	}

	ws := make([]WriteOp, 0)
	challengeID := challenge.Base.TxId
	requestID := challenge.RequestId

	requestKey := keys.KeyRechargeRequest(requestID)
	requestData, requestExists, err := sv.Get(requestKey)
	if err != nil || !requestExists {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "request not found"}, nil
	}

	var request pb.RechargeRequest
	if err := unmarshalProtoCompat(requestData, &request); err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "failed to parse request"}, err
	}

	if request.Status != pb.RechargeRequestStatus_RECHARGE_CHALLENGE_PERIOD {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "request is not in challenge period"}, nil
	}

	if request.ChallengeId != "" {
		existingChallengeKey := keys.KeyChallengeRecord(request.ChallengeId)
		_, exists, _ := sv.Get(existingChallengeKey)
		if exists {
			return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "challenge already exists"}, fmt.Errorf("challenge already exists")
		}
	}

	challengerAddr := challenge.Base.FromAddress
	accountKey := keys.KeyAccount(challengerAddr)
	accountData, accountExists, _ := sv.Get(accountKey)
	if !accountExists {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "challenger account not found"}, fmt.Errorf("challenger account not found")
	}

	var account pb.Account
	if err := unmarshalProtoCompat(accountData, &account); err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "failed to parse account"}, err
	}

	stakeAmount, err := parsePositiveBalanceStrict("challenge stake amount", challenge.StakeAmount)
	if err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "invalid stake amount"}, fmt.Errorf("invalid stake amount")
	}

	// 使用分离存储读取余额
	fbBalance := GetBalance(sv, challengerAddr, "FB")

	balance, err := parseBalanceStrict("balance", fbBalance.Balance)
	if err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "invalid balance state"}, err
	}
	if balance.Cmp(stakeAmount) < 0 {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "insufficient balance"}, fmt.Errorf("insufficient balance")
	}

	// 使用安全减法
	newBalance, err := SafeSub(balance, stakeAmount)
	if err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "balance underflow"}, fmt.Errorf("balance underflow: %w", err)
	}
	fbBalance.Balance = newBalance.String()
	SetBalance(sv, challengerAddr, "FB", fbBalance)

	// 保存余额更新
	balanceKey := keys.KeyBalance(challengerAddr, "FB")
	balanceData, _, _ := sv.Get(balanceKey)
	ws = append(ws, WriteOp{Key: balanceKey, Value: balanceData, Category: "balance"})

	updatedAccountData, err := proto.Marshal(&account)
	if err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "failed to marshal account"}, err
	}
	ws = append(ws, WriteOp{Key: accountKey, Value: updatedAccountData, Del: false, Category: "account"})

	challengeRecord := &pb.ChallengeRecord{
		ChallengeId:       challengeID,
		RequestId:         requestID,
		ChallengerAddress: challengerAddr,
		StakeAmount:       challenge.StakeAmount,
		Reason:            challenge.Reason,
		CreateHeight:      challenge.Base.ExecutedHeight,
		Finalized:         false,
	}

	challengeData, err := proto.Marshal(challengeRecord)
	if err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "failed to marshal challenge"}, err
	}

	challengeKey := keys.KeyChallengeRecord(challengeID)
	ws = append(ws, WriteOp{Key: challengeKey, Value: challengeData, Del: false, Category: "challenge"})

	request.Status = pb.RechargeRequestStatus_RECHARGE_CHALLENGED
	request.ChallengeId = challengeID

	updatedRequestData, err := proto.Marshal(&request)
	if err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "failed to marshal request"}, err
	}
	ws = append(ws, WriteOp{Key: requestKey, Value: updatedRequestData, Del: false, Category: "witness_request"})

	return ws, &Receipt{TxID: challengeID, Status: "SUCCEED", WriteCount: len(ws)}, nil
}

func (h *WitnessChallengeTxHandler) Apply(tx *pb.AnyTx) error {
	return nil
}

// ==================== ArbitrationVoteTxHandler ====================

// ArbitrationVoteTxHandler 仲裁投票处理器
type ArbitrationVoteTxHandler struct {
	witnessSvc *witness.Service
}

// SetWitnessService 设置见证者服务
func (h *ArbitrationVoteTxHandler) SetWitnessService(svc *witness.Service) {
	h.witnessSvc = svc
}

func (h *ArbitrationVoteTxHandler) Kind() string {
	return "arbitration_vote"
}

func (h *ArbitrationVoteTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	arbVoteTx, ok := tx.GetContent().(*pb.AnyTx_ArbitrationVoteTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not an arbitration vote transaction"}, fmt.Errorf("not an arbitration vote transaction")
	}

	arbVote := arbVoteTx.ArbitrationVoteTx
	if arbVote == nil || arbVote.Base == nil || arbVote.Vote == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid arbitration vote transaction"}, fmt.Errorf("invalid arbitration vote transaction")
	}

	ws := make([]WriteOp, 0)
	challengeID := arbVote.ChallengeId
	arbitratorAddr := arbVote.Vote.WitnessAddress

	challengeKey := keys.KeyChallengeRecord(challengeID)
	challengeData, challengeExists, err := sv.Get(challengeKey)
	if err != nil || !challengeExists {
		return nil, &Receipt{TxID: arbVote.Base.TxId, Status: "FAILED", Error: "challenge not found"}, nil
	}

	var challenge pb.ChallengeRecord
	if err := unmarshalProtoCompat(challengeData, &challenge); err != nil {
		return nil, &Receipt{TxID: arbVote.Base.TxId, Status: "FAILED", Error: "failed to parse challenge"}, err
	}

	arbVoteKey := keys.KeyArbitrationVote(challengeID, arbitratorAddr)
	_, voteExists, _ := sv.Get(arbVoteKey)
	if voteExists {
		return nil, &Receipt{TxID: arbVote.Base.TxId, Status: "FAILED", Error: "duplicate vote"}, fmt.Errorf("duplicate vote")
	}

	voteData, err := proto.Marshal(arbVote.Vote)
	if err != nil {
		return nil, &Receipt{TxID: arbVote.Base.TxId, Status: "FAILED", Error: "failed to marshal vote"}, err
	}
	ws = append(ws, WriteOp{Key: arbVoteKey, Value: voteData, Del: false, Category: "arbitration_vote"})

	challenge.ArbitrationVotes = append(challenge.ArbitrationVotes, arbVote.Vote)
	switch arbVote.Vote.VoteType {
	case pb.WitnessVoteType_VOTE_PASS:
		challenge.PassCount++
	case pb.WitnessVoteType_VOTE_FAIL:
		challenge.FailCount++
	}

	updatedChallengeData, err := proto.Marshal(&challenge)
	if err != nil {
		return nil, &Receipt{TxID: arbVote.Base.TxId, Status: "FAILED", Error: "failed to marshal challenge"}, err
	}
	ws = append(ws, WriteOp{Key: challengeKey, Value: updatedChallengeData, Del: false, Category: "challenge"})

	return ws, &Receipt{TxID: arbVote.Base.TxId, Status: "SUCCEED", WriteCount: len(ws)}, nil
}

func (h *ArbitrationVoteTxHandler) Apply(tx *pb.AnyTx) error {
	return nil
}

// ==================== WitnessClaimRewardTxHandler ====================

// WitnessClaimRewardTxHandler 领取奖励处理器
type WitnessClaimRewardTxHandler struct {
	witnessSvc *witness.Service
}

// SetWitnessService 设置见证者服务
func (h *WitnessClaimRewardTxHandler) SetWitnessService(svc *witness.Service) {
	h.witnessSvc = svc
}

func (h *WitnessClaimRewardTxHandler) Kind() string {
	return "witness_claim_reward"
}

func (h *WitnessClaimRewardTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	claimTx, ok := tx.GetContent().(*pb.AnyTx_WitnessClaimRewardTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a witness claim reward transaction"}, fmt.Errorf("not a witness claim reward transaction")
	}

	claim := claimTx.WitnessClaimRewardTx
	if claim == nil || claim.Base == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid witness claim reward transaction"}, fmt.Errorf("invalid witness claim reward transaction")
	}

	ws := make([]WriteOp, 0)
	witnessAddr := claim.Base.FromAddress

	witnessKey := keys.KeyWitnessInfo(witnessAddr)
	witnessData, witnessExists, err := sv.Get(witnessKey)
	if err != nil || !witnessExists {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "witness not found"}, fmt.Errorf("witness not found")
	}

	var witnessInfo pb.WitnessInfo
	if err := unmarshalProtoCompat(witnessData, &witnessInfo); err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "failed to parse witness info"}, err
	}

	pendingReward, err := parseBalanceStrict("pending reward", witnessInfo.PendingReward)
	if err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "invalid pending reward state"}, err
	}
	if pendingReward.Sign() <= 0 {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "no pending reward"}, fmt.Errorf("no pending reward")
	}

	accountKey := keys.KeyAccount(witnessAddr)
	accountData, accountExists, _ := sv.Get(accountKey)

	var account pb.Account
	if accountExists {
		if err := unmarshalProtoCompat(accountData, &account); err != nil {
			return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "failed to parse account"}, err
		}
	} else {
		account = pb.Account{Address: witnessAddr}
	}

	fbBalance := GetBalance(sv, witnessAddr, "FB")

	currentBalance, err := parseBalanceStrict("balance", fbBalance.Balance)
	if err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "invalid balance state"}, err
	}
	newBalance, err := SafeAdd(currentBalance, pendingReward)
	if err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "balance overflow"}, fmt.Errorf("balance overflow: %w", err)
	}
	fbBalance.Balance = newBalance.String()
	SetBalance(sv, witnessAddr, "FB", fbBalance)

	totalReward, err := parseBalanceStrict("total reward", witnessInfo.TotalReward)
	if err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "invalid total reward state"}, err
	}
	newTotalReward, err := SafeAdd(totalReward, pendingReward)
	if err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "total reward overflow"}, fmt.Errorf("total reward overflow: %w", err)
	}
	witnessInfo.TotalReward = newTotalReward.String()
	witnessInfo.PendingReward = "0"

	// 淇濆瓨浣欓鏇存柊
	balanceKey := keys.KeyBalance(witnessAddr, "FB")
	balanceData, _, _ := sv.Get(balanceKey)
	ws = append(ws, WriteOp{Key: balanceKey, Value: balanceData, Category: "balance"})

	updatedAccountData, err := proto.Marshal(&account)
	if err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "failed to marshal account"}, err
	}
	ws = append(ws, WriteOp{Key: accountKey, Value: updatedAccountData, Del: false, Category: "account"})

	updatedWitnessData, err := proto.Marshal(&witnessInfo)
	if err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "failed to marshal witness info"}, err
	}
	ws = append(ws, WriteOp{Key: witnessKey, Value: updatedWitnessData, Del: false, Category: "witness"})

	return ws, &Receipt{TxID: claim.Base.TxId, Status: "SUCCEED", WriteCount: len(ws)}, nil
}

func (h *WitnessClaimRewardTxHandler) Apply(tx *pb.AnyTx) error {
	return nil
}
