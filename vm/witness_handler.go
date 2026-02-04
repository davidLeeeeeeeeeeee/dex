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
	"math/big"
	"strconv"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
)

// DefaultVaultCount 默认每条链的 Vault 数量
const DefaultVaultCount = 100

// allocateVaultID 确定性分配 vault_id
// 使用 H(request_id) % vault_count 确保相同 request_id 总是分配到相同 vault
func allocateVaultID(requestID string, vaultCount uint32) uint32 {
	if vaultCount == 0 {
		vaultCount = DefaultVaultCount
	}
	hash := sha256.Sum256([]byte(requestID))
	// 使用前 4 字节作为 uint32
	n := binary.BigEndian.Uint32(hash[:4])
	return n % vaultCount
}

// allocateVaultIDWithLifecycleCheck 确定性分配 vault_id，并检查 Vault lifecycle
// 如果分配的 Vault 处于 DRAINING 状态，尝试下一个可用的 ACTIVE Vault
func allocateVaultIDWithLifecycleCheck(sv StateView, chain, requestID string, vaultCount uint32) (uint32, error) {
	if vaultCount == 0 {
		vaultCount = DefaultVaultCount
	}

	// 计算初始 vault_id（确定性）
	initialVaultID := allocateVaultID(requestID, vaultCount)

	// 检查初始 Vault 的 lifecycle
	vaultStateKey := keys.KeyFrostVaultState(chain, initialVaultID)
	vaultStateData, exists, _ := sv.Get(vaultStateKey)
	if exists && len(vaultStateData) > 0 {
		var vaultState pb.FrostVaultState
		if err := proto.Unmarshal(vaultStateData, &vaultState); err == nil {
			// 检查 lifecycle（从 VaultTransitionState 获取，或从 VaultState.Status 推断）
			// 如果 Vault 处于 DRAINING 状态，尝试下一个 ACTIVE Vault
			if vaultState.Status == VaultLifecycleDraining {
				// 查找下一个 ACTIVE 的 Vault
				for offset := uint32(1); offset < vaultCount; offset++ {
					candidateID := (initialVaultID + offset) % vaultCount
					candidateKey := keys.KeyFrostVaultState(chain, candidateID)
					candidateData, candidateExists, _ := sv.Get(candidateKey)
					if candidateExists && len(candidateData) > 0 {
						var candidateState pb.FrostVaultState
						if err := proto.Unmarshal(candidateData, &candidateState); err == nil {
							if candidateState.Status == "ACTIVE" {
								return candidateID, nil
							}
						}
					} else {
						// 如果 Vault 不存在，默认认为是 ACTIVE（新创建的 Vault）
						return candidateID, nil
					}
				}
				// 如果所有 Vault 都是 DRAINING，返回错误
				return 0, fmt.Errorf("no ACTIVE vault available for chain %s", chain)
			}
		}
	}

	// 初始 Vault 是 ACTIVE 或不存在（默认 ACTIVE）
	return initialVaultID, nil
}

// WitnessServiceAware 见证者服务感知接口
// 实现此接口的 handler 可以接收 WitnessService 的引用
type WitnessServiceAware interface {
	SetWitnessService(svc *witness.Service)
}

// ==================== WitnessStakeTxHandler ====================

// WitnessStakeTxHandler 见证者质押/解质押交易处理器
type WitnessStakeTxHandler struct {
	witnessSvc *witness.Service
}

// SetWitnessService 设置见证者服务
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

	// 读取账户
	accountKey := keys.KeyAccount(address)
	accountData, accountExists, err := sv.Get(accountKey)
	if err != nil {
		return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "failed to read account"}, err
	}

	var account pb.Account
	if accountExists {
		if err := proto.Unmarshal(accountData, &account); err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "failed to parse account"}, err
		}
	} else {
		return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "account not found"}, fmt.Errorf("account not found")
	}

	// 读取见证者信息
	witnessKey := keys.KeyWitnessInfo(address)
	witnessData, witnessExists, _ := sv.Get(witnessKey)

	var witnessInfo pb.WitnessInfo
	if witnessExists {
		if err := proto.Unmarshal(witnessData, &witnessInfo); err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "failed to parse witness info"}, err
		}
	} else {
		witnessInfo = pb.WitnessInfo{Address: address, StakeAmount: "0", Status: pb.WitnessStatus_WITNESS_CANDIDATE}
	}

	amount, ok := new(big.Int).SetString(stake.Amount, 10)
	if !ok {
		return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "invalid amount"}, fmt.Errorf("invalid amount")
	}

	// 使用分离存储读取余额
	fbBalance := GetBalance(sv, address, "FB")

	if stake.Op == pb.OrderOp_ADD {
		// 使用 WitnessService 进行验证（如果可用）
		if h.witnessSvc != nil {
			amountDec, _ := decimal.NewFromString(stake.Amount)
			if _, err := h.witnessSvc.ProcessStake(address, amountDec); err != nil {
				if err == witness.ErrWitnessAlreadyActive {
					logs.Warn("[WitnessStake] witness %s already active, treating as success (idempotent)", address)
				} else {
					return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: err.Error()}, err
				}
			}
		}

		balance, _ := new(big.Int).SetString(fbBalance.Balance, 10)
		if balance == nil {
			balance = big.NewInt(0)
		}
		if balance.Cmp(amount) < 0 {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "insufficient balance"}, fmt.Errorf("insufficient balance")
		}
		// 使用安全减法
		newBalance, err := SafeSub(balance, amount)
		if err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "balance underflow"}, fmt.Errorf("balance underflow: %w", err)
		}
		lockedBalance, _ := new(big.Int).SetString(fbBalance.WitnessLockedBalance, 10)
		if lockedBalance == nil {
			lockedBalance = big.NewInt(0)
		}
		// 使用安全加法检查锁定余额溢出
		newLocked, err := SafeAdd(lockedBalance, amount)
		if err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "locked balance overflow"}, fmt.Errorf("locked balance overflow: %w", err)
		}
		fbBalance.Balance = newBalance.String()
		fbBalance.WitnessLockedBalance = newLocked.String()

		currentStake, _ := new(big.Int).SetString(witnessInfo.StakeAmount, 10)
		if currentStake == nil {
			currentStake = big.NewInt(0)
		}
		// 使用安全加法检查质押金额溢出
		newStake, err := SafeAdd(currentStake, amount)
		if err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "stake amount overflow"}, fmt.Errorf("stake amount overflow: %w", err)
		}
		witnessInfo.StakeAmount = newStake.String()
		witnessInfo.Status = pb.WitnessStatus_WITNESS_ACTIVE
	} else {
		// 使用 WitnessService 进行验证（如果可用）
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

	// 保存余额更新
	SetBalance(sv, address, "FB", fbBalance)
	balanceKey := keys.KeyBalance(address, "FB")
	balanceData, _, _ := sv.Get(balanceKey)
	ws = append(ws, WriteOp{Key: balanceKey, Value: balanceData, SyncStateDB: true, Category: "balance"})

	updatedAccountData, err := proto.Marshal(&account)
	if err != nil {
		return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "failed to marshal account"}, err
	}
	ws = append(ws, WriteOp{Key: accountKey, Value: updatedAccountData, Del: false, SyncStateDB: true, Category: "account"})

	witnessInfoData, err := proto.Marshal(&witnessInfo)
	if err != nil {
		return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "failed to marshal witness info"}, err
	}
	ws = append(ws, WriteOp{Key: witnessKey, Value: witnessInfoData, Del: false, SyncStateDB: true, Category: "witness"})

	historyKey := keys.KeyWitnessHistory(stake.Base.TxId)
	historyData, _ := proto.Marshal(stake)
	ws = append(ws, WriteOp{Key: historyKey, Value: historyData, Del: false, SyncStateDB: false, Category: "history"})

	// 同步到 WitnessService 内存状态
	if h.witnessSvc != nil {
		h.witnessSvc.LoadWitness(&witnessInfo)
	}

	return ws, &Receipt{TxID: stake.Base.TxId, Status: "SUCCEED", WriteCount: len(ws)}, nil
}

func (h *WitnessStakeTxHandler) Apply(tx *pb.AnyTx) error {
	return nil
}

// ==================== WitnessRequestTxHandler ====================

// WitnessRequestTxHandler 入账见证请求处理器
type WitnessRequestTxHandler struct {
	witnessSvc *witness.Service
	VaultCount uint32
}

// SetWitnessService 设置见证者服务
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
		// 容错处理：如果请求已存在，且状态是活跃的或者是已完成的，允许这笔交易作为“空操作”成功，或者返回 FAILED 凭证但不报错
		// 在区块链中，重复的交易 hash 本身不应该能进入池子，但如果由于分叉等原因进来了，我们返回 FAILED 凭据而不是 error
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

	// 使用 WitnessService 创建请求（包含见证者选择）
	if h.witnessSvc != nil {
		var err error
		rechargeRequest, err = h.witnessSvc.CreateRechargeRequest(request)
		if err != nil {
			return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: err.Error()}, err
		}
	} else {
		// 降级模式：不使用 WitnessService
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

	// 确定性分配 vault_id，避免跨 Vault 混用资金
	// 注意：需要检查 Vault lifecycle，DRAINING 的 Vault 不再分配新入账
	vCount := h.VaultCount
	if vCount == 0 {
		vCount = DefaultVaultCount
	}
	vaultID, err := allocateVaultIDWithLifecycleCheck(sv, request.NativeChain, requestID, vCount)
	if err != nil {
		return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: err.Error()}, err
	}
	rechargeRequest.VaultId = vaultID

	// 序列化最终的请求数据
	requestData, err = proto.Marshal(rechargeRequest)
	if err != nil {
		return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: "failed to marshal request"}, err
	}
	ws = append(ws, WriteOp{Key: existingKey, Value: requestData, Del: false, SyncStateDB: true, Category: "witness_request"})
	ws = append(ws, WriteOp{Key: nativeTxKey, Value: []byte(requestID), Del: false, SyncStateDB: false, Category: "index"})

	pendingHeight := rechargeRequest.CreateHeight
	if pendingHeight == 0 {
		pendingHeight = request.Base.ExecutedHeight
	}

	pendingSeqKey := keys.KeyFrostFundsPendingLotSeq(request.NativeChain, request.TokenAddress, vaultID, pendingHeight)
	pendingSeq := readUintSeq(sv, pendingSeqKey)
	pendingIndexKey := keys.KeyFrostFundsPendingLotIndex(request.NativeChain, request.TokenAddress, vaultID, pendingHeight, pendingSeq)
	pendingRefKey := keys.KeyFrostFundsPendingLotRef(requestID)

	ws = append(ws, WriteOp{Key: pendingIndexKey, Value: []byte(requestID), Del: false, SyncStateDB: true, Category: "frost_funds_pending"})
	ws = append(ws, WriteOp{Key: pendingSeqKey, Value: []byte(strconv.FormatUint(pendingSeq+1, 10)), Del: false, SyncStateDB: true, Category: "frost_funds_pending"})
	ws = append(ws, WriteOp{Key: pendingRefKey, Value: []byte(pendingIndexKey), Del: false, SyncStateDB: true, Category: "frost_funds_pending"})

	return ws, &Receipt{TxID: requestID, Status: "SUCCEED", WriteCount: len(ws)}, nil
}

func (h *WitnessRequestTxHandler) Apply(tx *pb.AnyTx) error {
	return nil
}

// ==================== WitnessVoteTxHandler ====================

// WitnessVoteTxHandler 见证投票处理器
type WitnessVoteTxHandler struct {
	witnessSvc *witness.Service
}

// SetWitnessService 设置见证者服务
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
		// 如果请求找不到，返回 FAILED 凭据，但不返回 error
		// 这样可以避免整笔交易导致区块验证失败，从而解决共识停滞问题
		return nil, &Receipt{TxID: vote.Base.TxId, Status: "FAILED", Error: "request not found"}, nil
	}

	var request pb.RechargeRequest
	if err := proto.Unmarshal(requestData, &request); err != nil {
		return nil, &Receipt{TxID: vote.Base.TxId, Status: "FAILED", Error: "failed to parse request"}, err
	}

	voteKey := keys.KeyWitnessVote(requestID, witnessAddr)
	_, voteExists, _ := sv.Get(voteKey)
	if voteExists {
		return nil, &Receipt{TxID: vote.Base.TxId, Status: "FAILED", Error: "duplicate vote"}, nil
	}

	// 4. 调用 WitnessService 更新状态（内存）
	var finalRequest *pb.RechargeRequest
	if h.witnessSvc != nil {
		// 设置投票交易 ID
		vote.Vote.TxId = vote.Base.TxId
		updatedRequest, err := h.witnessSvc.ProcessVote(vote.Vote)
		if err != nil {
			// 业务逻辑错误（例如状态不对）不应该废掉整个区块
			return nil, &Receipt{TxID: vote.Base.TxId, Status: "FAILED", Error: err.Error()}, nil
		}
		finalRequest = updatedRequest
	} else {

		// 降级：手动更新（仅用于测试）
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
	ws = append(ws, WriteOp{Key: voteKey, Value: voteData, Del: false, SyncStateDB: false, Category: "witness_vote"})

	updatedRequestData, err := proto.Marshal(finalRequest)
	if err != nil {
		return nil, &Receipt{TxID: vote.Base.TxId, Status: "FAILED", Error: "failed to marshal request"}, err
	}
	ws = append(ws, WriteOp{Key: requestKey, Value: updatedRequestData, Del: false, SyncStateDB: true, Category: "witness_request"})

	return ws, &Receipt{TxID: vote.Base.TxId, Status: "SUCCEED", WriteCount: len(ws)}, nil
}

func (h *WitnessVoteTxHandler) Apply(tx *pb.AnyTx) error {
	return nil
}

// ==================== WitnessChallengeTxHandler ====================

// WitnessChallengeTxHandler 挑战交易处理器
type WitnessChallengeTxHandler struct {
	witnessSvc *witness.Service
}

// SetWitnessService 设置见证者服务
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
	if err := proto.Unmarshal(requestData, &request); err != nil {
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
	if err := proto.Unmarshal(accountData, &account); err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "failed to parse account"}, err
	}

	stakeAmount, ok := new(big.Int).SetString(challenge.StakeAmount, 10)
	if !ok {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "invalid stake amount"}, fmt.Errorf("invalid stake amount")
	}

	// 使用分离存储读取余额
	fbBalance := GetBalance(sv, challengerAddr, "FB")

	balance, _ := new(big.Int).SetString(fbBalance.Balance, 10)
	if balance == nil || balance.Cmp(stakeAmount) < 0 {
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
	ws = append(ws, WriteOp{Key: balanceKey, Value: balanceData, SyncStateDB: true, Category: "balance"})

	updatedAccountData, err := proto.Marshal(&account)
	if err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "failed to marshal account"}, err
	}
	ws = append(ws, WriteOp{Key: accountKey, Value: updatedAccountData, Del: false, SyncStateDB: true, Category: "account"})

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
	ws = append(ws, WriteOp{Key: challengeKey, Value: challengeData, Del: false, SyncStateDB: true, Category: "challenge"})

	request.Status = pb.RechargeRequestStatus_RECHARGE_CHALLENGED
	request.ChallengeId = challengeID

	updatedRequestData, err := proto.Marshal(&request)
	if err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "failed to marshal request"}, err
	}
	ws = append(ws, WriteOp{Key: requestKey, Value: updatedRequestData, Del: false, SyncStateDB: true, Category: "witness_request"})

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
	if err := proto.Unmarshal(challengeData, &challenge); err != nil {
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
	ws = append(ws, WriteOp{Key: arbVoteKey, Value: voteData, Del: false, SyncStateDB: false, Category: "arbitration_vote"})

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
	ws = append(ws, WriteOp{Key: challengeKey, Value: updatedChallengeData, Del: false, SyncStateDB: true, Category: "challenge"})

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
	if err := proto.Unmarshal(witnessData, &witnessInfo); err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "failed to parse witness info"}, err
	}

	pendingReward, _ := new(big.Int).SetString(witnessInfo.PendingReward, 10)
	if pendingReward == nil || pendingReward.Sign() <= 0 {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "no pending reward"}, fmt.Errorf("no pending reward")
	}

	accountKey := keys.KeyAccount(witnessAddr)
	accountData, accountExists, _ := sv.Get(accountKey)

	var account pb.Account
	if accountExists {
		if err := proto.Unmarshal(accountData, &account); err != nil {
			return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "failed to parse account"}, err
		}
	} else {
		account = pb.Account{Address: witnessAddr}
	}

	// 使用分离存储读取余额
	fbBalance := GetBalance(sv, witnessAddr, "FB")

	currentBalance, _ := new(big.Int).SetString(fbBalance.Balance, 10)
	if currentBalance == nil {
		currentBalance = big.NewInt(0)
	}
	newBalance, err := SafeAdd(currentBalance, pendingReward)
	if err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "balance overflow"}, fmt.Errorf("balance overflow: %w", err)
	}
	fbBalance.Balance = newBalance.String()
	SetBalance(sv, witnessAddr, "FB", fbBalance)

	totalReward, _ := new(big.Int).SetString(witnessInfo.TotalReward, 10)
	if totalReward == nil {
		totalReward = big.NewInt(0)
	}
	newTotalReward, err := SafeAdd(totalReward, pendingReward)
	if err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "total reward overflow"}, fmt.Errorf("total reward overflow: %w", err)
	}
	witnessInfo.TotalReward = newTotalReward.String()
	witnessInfo.PendingReward = "0"

	// 保存余额更新
	balanceKey := keys.KeyBalance(witnessAddr, "FB")
	balanceData, _, _ := sv.Get(balanceKey)
	ws = append(ws, WriteOp{Key: balanceKey, Value: balanceData, SyncStateDB: true, Category: "balance"})

	updatedAccountData, err := proto.Marshal(&account)
	if err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "failed to marshal account"}, err
	}
	ws = append(ws, WriteOp{Key: accountKey, Value: updatedAccountData, Del: false, SyncStateDB: true, Category: "account"})

	updatedWitnessData, err := proto.Marshal(&witnessInfo)
	if err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "failed to marshal witness info"}, err
	}
	ws = append(ws, WriteOp{Key: witnessKey, Value: updatedWitnessData, Del: false, SyncStateDB: true, Category: "witness"})

	return ws, &Receipt{TxID: claim.Base.TxId, Status: "SUCCEED", WriteCount: len(ws)}, nil
}

func (h *WitnessClaimRewardTxHandler) Apply(tx *pb.AnyTx) error {
	return nil
}
