// vm/witness_handler.go
// Witness transaction handlers.
package vm

import (
	"bytes"
	"crypto/sha256"
	"dex/frost/chain"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"dex/utils"
	"dex/witness"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
)

const DefaultVaultCount = 100

// allocateVaultID 通过对 request_id 做哈希后取模，确定性地分配一个 vault_id。
// 算法：H(request_id) % vault_count，保证同一 request_id 始终映射到同一个 vault。
func allocateVaultID(requestID string, vaultCount uint32) uint32 {
	hash := sha256.Sum256([]byte(requestID))
	// 取哈希前 4 字节转为 uint32
	n := binary.BigEndian.Uint32(hash[:4])
	return n % vaultCount
}

// allocateVaultIDWithLifecycleCheck 在分配 vault_id 时额外检查 Vault lifecycle。
// 若初始分配到的 Vault 处于 DRAINING 状态，则线性探测，找到第一个 ACTIVE Vault 返回。
func allocateVaultIDWithLifecycleCheck(sv StateView, chain, requestID string, vaultCount uint32) (uint32, error) {
	if vaultCount == 0 {
		vaultCount = DefaultVaultCount
	}

	initialVaultID := allocateVaultID(requestID, vaultCount)
	// 查询初始分配的 Vault 的 lifecycle 状态
	vaultStateKey := keys.KeyFrostVaultState(chain, initialVaultID)
	vaultStateData, exists, _ := sv.Get(vaultStateKey)
	if exists && len(vaultStateData) > 0 {
		var vaultState pb.FrostVaultState
		if err := unmarshalProtoCompat(vaultStateData, &vaultState); err == nil {
			// 注意：这里读的是 VaultState.Status，不是 VaultTransitionState
			// 若该 Vault 处于 DRAINING，则线性探测找到第一个 ACTIVE Vault
			if vaultState.Status == VaultLifecycleDraining {
				// 当前 Vault 处于 DRAINING，线性探测寻找 ACTIVE Vault
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
						// 该 Vault 数据不存在，视为未初始化，可分配（等同 ACTIVE）
						return candidateID, nil
					}
				}
				// 探测完所有 Vault 仍未找到 ACTIVE，所有 Vault 均处于 DRAINING
				return 0, fmt.Errorf("no ACTIVE vault available for chain %s", chain)
			}
		}
	}

	// 初始分配的 Vault 不处于 DRAINING，直接返回（假定为 ACTIVE）
	return initialVaultID, nil
}

// WitnessServiceAware 是需要依赖 WitnessService 的 handler 接口。
// 实现该接口的 handler 可通过 SetWitnessService 注入 WitnessService 实例。
func appendUniqueNonEmpty(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func resolveVaultChainAndCount(sv StateView, chainName string) (string, uint32, error) {
	raw := strings.TrimSpace(chainName)
	candidates := make([]string, 0, 3)
	candidates = appendUniqueNonEmpty(candidates, chain.NormalizeChain(raw))
	candidates = appendUniqueNonEmpty(candidates, raw)
	candidates = appendUniqueNonEmpty(candidates, strings.ToUpper(raw))

	for _, candidate := range candidates {
		cfgKey := keys.KeyFrostVaultConfig(candidate, 0)
		cfgData, exists, err := sv.Get(cfgKey)
		if err != nil {
			return "", 0, fmt.Errorf("failed to read vault config for chain %s: %w", candidate, err)
		}
		if !exists || len(cfgData) == 0 {
			continue
		}

		var cfg pb.FrostVaultConfig
		if err := unmarshalProtoCompat(cfgData, &cfg); err != nil {
			return "", 0, fmt.Errorf("failed to parse vault config for chain %s: %w", candidate, err)
		}
		if cfg.VaultCount == 0 {
			return "", 0, fmt.Errorf("invalid vault count=0 for chain %s", candidate)
		}
		return candidate, cfg.VaultCount, nil
	}

	return "", 0, fmt.Errorf("vault config not found for chain %s", raw)
}

func isAllocatableVaultStatus(status string) bool {
	return status == VaultStatusKeyReady || status == VaultStatusActive
}

func isBTCChainName(chainName string) bool {
	c := strings.ToLower(strings.TrimSpace(chainName))
	return c == "btc" || strings.HasPrefix(c, "btc_") || strings.HasPrefix(c, "btc-")
}

func lookupVaultIDByScriptPubKey(sv StateView, chainName string, vaultCount uint32, scriptPubKey []byte, receiverAddress string) (uint32, error) {
	if vaultCount == 0 {
		return 0, fmt.Errorf("invalid vault count=0 for chain %s", chainName)
	}

	xOnly, err := utils.ParseTaprootScriptPubKeyXOnly(scriptPubKey)
	if err != nil {
		return 0, fmt.Errorf("parse taproot script_pubkey failed: %w", err)
	}

	matches := make([]uint32, 0, 1)
	nonAllocatableMatches := make([]uint32, 0, 1)

	for candidateID := uint32(0); candidateID < vaultCount; candidateID++ {
		candidateKey := keys.KeyFrostVaultState(chainName, candidateID)
		candidateData, candidateExists, err := sv.Get(candidateKey)
		if err != nil {
			return 0, fmt.Errorf("failed to read vault state for chain %s vault %d: %w", chainName, candidateID, err)
		}
		if !candidateExists || len(candidateData) == 0 {
			continue
		}

		var candidateState pb.FrostVaultState
		if err := unmarshalProtoCompat(candidateData, &candidateState); err != nil {
			return 0, fmt.Errorf("failed to parse vault state for chain %s vault %d: %w", chainName, candidateID, err)
		}

		candidateXOnly, err := utils.NormalizeSecp256k1XOnlyPubKey(candidateState.GroupPubkey)
		if err != nil {
			continue
		}

		// 计算 tweaked pubkey: P' = P + H_TapTweak(P_x || receiverAddress)·G
		tweak := utils.ComputeUserTweak(candidateXOnly, receiverAddress)
		tweakedXOnly, err := utils.ComputeTweakedXOnlyPubkey(candidateState.GroupPubkey, tweak)
		if err != nil {
			continue
		}

		if !bytes.Equal(tweakedXOnly, xOnly) {
			continue
		}

		if isAllocatableVaultStatus(candidateState.Status) {
			matches = append(matches, candidateID)
		} else {
			nonAllocatableMatches = append(nonAllocatableMatches, candidateID)
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		if len(nonAllocatableMatches) > 0 {
			return 0, fmt.Errorf(
				"script_pubkey=%s maps only to non-allocatable vault(s) %v on chain %s",
				hex.EncodeToString(scriptPubKey),
				nonAllocatableMatches,
				chainName,
			)
		}
		return 0, fmt.Errorf(
			"no allocatable vault matches script_pubkey=%s on chain %s (receiver=%s)",
			hex.EncodeToString(scriptPubKey),
			chainName,
			receiverAddress,
		)
	default:
		return 0, fmt.Errorf(
			"ambiguous script_pubkey=%s maps to multiple allocatable vaults %v on chain %s",
			hex.EncodeToString(scriptPubKey),
			matches,
			chainName,
		)
	}
}

func allocateVaultIDWithStateCheck(sv StateView, chainName, requestID string, vaultCount uint32) (uint32, error) {
	if vaultCount == 0 {
		return 0, fmt.Errorf("invalid vault count=0 for chain %s", chainName)
	}

	initialVaultID := allocateVaultID(requestID, vaultCount)
	for offset := uint32(0); offset < vaultCount; offset++ {
		candidateID := (initialVaultID + offset) % vaultCount
		candidateKey := keys.KeyFrostVaultState(chainName, candidateID)
		candidateData, candidateExists, err := sv.Get(candidateKey)
		if err != nil {
			return 0, fmt.Errorf("failed to read vault state for chain %s vault %d: %w", chainName, candidateID, err)
		}
		if !candidateExists || len(candidateData) == 0 {
			continue
		}

		var candidateState pb.FrostVaultState
		if err := unmarshalProtoCompat(candidateData, &candidateState); err != nil {
			return 0, fmt.Errorf("failed to parse vault state for chain %s vault %d: %w", chainName, candidateID, err)
		}
		if isAllocatableVaultStatus(candidateState.Status) {
			return candidateID, nil
		}
	}

	return 0, fmt.Errorf("no allocatable vault (KEY_READY/ACTIVE) for chain %s", chainName)
}

type WitnessServiceAware interface {
	SetWitnessService(svc *witness.Service)
}

// ==================== WitnessStakeTxHandler ====================

// ==================== WitnessStakeTxHandler ====================
type WitnessStakeTxHandler struct {
	witnessSvc *witness.Service
}

// SetWitnessService 注入 WitnessService 实例。
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

	// 读取见证人信息（不存在则初始化默认值）
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

	fbBalance := GetBalance(sv, address, "FB")
	if stake.Op == pb.OrderOp_ADD {
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
		newBalance, err := SafeSub(balance, amount)
		if err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "balance underflow"}, fmt.Errorf("balance underflow: %w", err)
		}
		lockedBalance, err := parseBalanceStrict("witness locked balance", fbBalance.WitnessLockedBalance)
		if err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "invalid locked balance state"}, err
		}
		// 质押：从可用余额扣除，转入 witness_locked_balance
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
		// 累加质押金额到 witnessInfo.StakeAmount，并将状态设为 ACTIVE
		newStake, err := SafeAdd(currentStake, amount)
		if err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "stake amount overflow"}, fmt.Errorf("stake amount overflow: %w", err)
		}
		witnessInfo.StakeAmount = newStake.String()
		witnessInfo.Status = pb.WitnessStatus_WITNESS_ACTIVE
	} else {
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

	// 将最新的 witnessInfo 热更新到 WitnessService 内存中
	if h.witnessSvc != nil {
		h.witnessSvc.LoadWitness(&witnessInfo)
	}

	return ws, &Receipt{TxID: stake.Base.TxId, Status: "SUCCEED", WriteCount: len(ws)}, nil
}

func (h *WitnessStakeTxHandler) Apply(tx *pb.AnyTx) error {
	return nil
}

// ==================== WitnessRequestTxHandler ====================

// WitnessRequestTxHandler 处理见证入账请求交易，负责验证并记录充值请求。
type WitnessRequestTxHandler struct {
	witnessSvc *witness.Service
	VaultCount uint32
}

// SetWitnessService 注入 WitnessService 实例。
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
		// Duplicate request is treated as failed receipt without VM error.
		return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: "request already exists"}, nil
	}

	chainName, vaultCount, err := resolveVaultChainAndCount(sv, request.NativeChain)
	if err != nil {
		return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: err.Error()}, err
	}

	nativeTxKey := keys.KeyRechargeRequestByNativeTx(chainName, request.NativeTxHash)
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

	// 通过 WitnessService 创建充值请求（含见证轮次等业务逻辑）；
	// 若未注入则直接构造基础结构体（测试/简易节点模式）
	if h.witnessSvc != nil {
		var err error
		rechargeRequest, err = h.witnessSvc.CreateRechargeRequest(request)
		if err != nil {
			return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: err.Error()}, err
		}
	} else {
		// 未注入 WitnessService 时的降级逻辑：直接构造最小充值请求结构体
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

	// Assign vault:
	// - BTC: derive vault from native taproot script_pubkey.
	// - Others: keep deterministic request_id allocation.
	var vaultID uint32
	if isBTCChainName(chainName) {
		vaultID, err = lookupVaultIDByScriptPubKey(sv, chainName, vaultCount, rechargeRequest.NativeScript, rechargeRequest.ReceiverAddress)
	} else {
		vaultID, err = allocateVaultIDWithStateCheck(sv, chainName, requestID, vaultCount)
	}
	if err != nil {
		return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: err.Error()}, err
	}
	rechargeRequest.NativeChain = chainName
	rechargeRequest.VaultId = vaultID

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

	pendingSeqKey := keys.KeyFrostFundsPendingLotSeq(chainName, request.TokenAddress, vaultID, pendingHeight)
	pendingSeq := readUintSeq(sv, pendingSeqKey)
	pendingIndexKey := keys.KeyFrostFundsPendingLotIndex(chainName, request.TokenAddress, vaultID, pendingHeight, pendingSeq)
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

// WitnessVoteTxHandler 处理见证人投票交易，负责记录投票并更新充值请求状态。
type WitnessVoteTxHandler struct {
	witnessSvc *witness.Service
}

// SetWitnessService 注入 WitnessService 实例。
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
		// 请求不存在：直接返回 FAILED receipt 而不返回 VM error，
		// 防止整块交易 abort 影响后续交易。链上采用宽容策略：投票对应的请求若已不存在，视为冗余投票静默丢弃。
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

	// 通过 WitnessService 处理投票业务逻辑（含阈值判断、请求状态流转等），
	// 若未注入则手动累计计数（简易模式）
	var finalRequest *pb.RechargeRequest
	if h.witnessSvc != nil {
		vote.Vote.TxId = vote.Base.TxId
		updatedRequest, err := h.witnessSvc.ProcessVote(vote.Vote)
		if err != nil {
			// WitnessService 处理投票失败：视为业务拒绝，返回 FAILED receipt（非 VM error）
			return nil, &Receipt{TxID: vote.Base.TxId, Status: "FAILED", Error: err.Error()}, nil
		}
		finalRequest = updatedRequest
	} else {

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

// WitnessChallengeTxHandler 处理见证挑战交易，在挑战期内对充值请求提起挑战。
type WitnessChallengeTxHandler struct {
	witnessSvc *witness.Service
}

// SetWitnessService 注入 WitnessService 实例。
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

	fbBalance := GetBalance(sv, challengerAddr, "FB")
	balance, err := parseBalanceStrict("balance", fbBalance.Balance)
	if err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "invalid balance state"}, err
	}
	if balance.Cmp(stakeAmount) < 0 {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "insufficient balance"}, fmt.Errorf("insufficient balance")
	}

	newBalance, err := SafeSub(balance, stakeAmount)
	if err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "balance underflow"}, fmt.Errorf("balance underflow: %w", err)
	}
	fbBalance.Balance = newBalance.String()
	SetBalance(sv, challengerAddr, "FB", fbBalance)

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

// ArbitrationVoteTxHandler handles arbitration vote transactions.
type ArbitrationVoteTxHandler struct {
	witnessSvc *witness.Service
}

// SetWitnessService sets witness service.
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

// WitnessClaimRewardTxHandler handles witness reward claim transactions.
type WitnessClaimRewardTxHandler struct {
	witnessSvc *witness.Service
}

// SetWitnessService sets witness service.
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
