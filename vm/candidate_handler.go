package vm

import (
	"dex/pb"
	"encoding/json"
	"fmt"
	"math/big"
)

// CandidateTxHandler 委托人投票交易处理器
type CandidateTxHandler struct{}

func (h *CandidateTxHandler) Kind() string {
	return "candidate"
}

func (h *CandidateTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 1. 提取CandidateTx
	candidateTx, ok := tx.GetContent().(*pb.AnyTx_CandidateTx)
	if !ok {
		return nil, &Receipt{
			TxID:   tx.GetTxId(),
			Status: "FAILED",
			Error:  "not a candidate transaction",
		}, fmt.Errorf("not a candidate transaction")
	}

	candidate := candidateTx.CandidateTx
	if candidate == nil || candidate.Base == nil {
		return nil, &Receipt{
			TxID:   tx.GetTxId(),
			Status: "FAILED",
			Error:  "invalid candidate transaction",
		}, fmt.Errorf("invalid candidate transaction")
	}

	// 2. 根据操作类型分发处理
	switch candidate.Op {
	case pb.OrderOp_ADD:
		return h.handleAddVote(candidate, sv)
	case pb.OrderOp_REMOVE:
		return h.handleRemoveVote(candidate, sv)
	default:
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "unknown candidate operation",
		}, fmt.Errorf("unknown candidate operation: %v", candidate.Op)
	}
}

// handleAddVote 处理投票（委托）
func (h *CandidateTxHandler) handleAddVote(candidate *pb.CandidateTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 验证投票金额
	amount, ok := new(big.Int).SetString(candidate.Amount, 10)
	if !ok || amount.Sign() <= 0 {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "invalid vote amount",
		}, fmt.Errorf("invalid vote amount: %s", candidate.Amount)
	}

	// 读取投票者账户
	voterAccountKey := fmt.Sprintf("account_%s", candidate.Base.FromAddress)
	voterAccountData, voterExists, err := sv.Get(voterAccountKey)
	if err != nil || !voterExists {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "voter account not found",
		}, fmt.Errorf("voter account not found: %s", candidate.Base.FromAddress)
	}

	var voterAccount pb.Account
	if err := json.Unmarshal(voterAccountData, &voterAccount); err != nil {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse voter account",
		}, err
	}

	// 读取候选人账户（验证候选人是否存在）
	candidateAccountKey := fmt.Sprintf("account_%s", candidate.CandidateAddress)
	candidateAccountData, candidateExists, err := sv.Get(candidateAccountKey)
	if err != nil || !candidateExists {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "candidate account not found",
		}, fmt.Errorf("candidate account not found: %s", candidate.CandidateAddress)
	}

	var candidateAccount pb.Account
	if err := json.Unmarshal(candidateAccountData, &candidateAccount); err != nil {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse candidate account",
		}, err
	}

	// 假设使用原生代币进行投票（需要根据实际情况调整token地址）
	nativeTokenAddr := "native_token" // 这应该是系统定义的原生代币地址

	// 检查投票者余额
	if voterAccount.Balances == nil || voterAccount.Balances[nativeTokenAddr] == nil {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "insufficient balance for voting",
		}, fmt.Errorf("no balance for native token")
	}

	voterBalance, _ := new(big.Int).SetString(voterAccount.Balances[nativeTokenAddr].Balance, 10)
	if voterBalance == nil || voterBalance.Cmp(amount) < 0 {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "insufficient balance for voting",
		}, fmt.Errorf("insufficient balance: has %s, need %s", voterBalance.String(), amount.String())
	}

	// 检查是否已经投票给其他候选人
	if voterAccount.Candidate != "" && voterAccount.Candidate != candidate.CandidateAddress {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "already voted for another candidate, please remove vote first",
		}, fmt.Errorf("already voted for: %s", voterAccount.Candidate)
	}

	ws := make([]WriteOp, 0)

	// 从可用余额转移到锁定余额
	newVoterBalance := new(big.Int).Sub(voterBalance, amount)
	voterAccount.Balances[nativeTokenAddr].Balance = newVoterBalance.String()

	currentLockedBalance, _ := new(big.Int).SetString(voterAccount.Balances[nativeTokenAddr].CandidateLockedBalance, 10)
	if currentLockedBalance == nil {
		currentLockedBalance = big.NewInt(0)
	}
	newLockedBalance := new(big.Int).Add(currentLockedBalance, amount)
	voterAccount.Balances[nativeTokenAddr].CandidateLockedBalance = newLockedBalance.String()

	// 设置投票的候选人
	voterAccount.Candidate = candidate.CandidateAddress

	// 保存投票者账户
	updatedVoterData, err := json.Marshal(&voterAccount)
	if err != nil {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "failed to marshal voter account",
		}, err
	}

	ws = append(ws, WriteOp{
		Key:   voterAccountKey,
		Value: updatedVoterData,
		Del:   false,
	})

	// 更新候选人收到的投票数
	candidateVotes, _ := new(big.Int).SetString(candidateAccount.ReceiveVotes, 10)
	if candidateVotes == nil {
		candidateVotes = big.NewInt(0)
	}
	newCandidateVotes := new(big.Int).Add(candidateVotes, amount)
	candidateAccount.ReceiveVotes = newCandidateVotes.String()

	// 保存候选人账户
	updatedCandidateData, err := json.Marshal(&candidateAccount)
	if err != nil {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "failed to marshal candidate account",
		}, err
	}

	ws = append(ws, WriteOp{
		Key:   candidateAccountKey,
		Value: updatedCandidateData,
		Del:   false,
	})

	// 创建候选人索引，用于快速查询候选人的所有委托人
	// key格式: candidate:xxx|user:xxx
	candidateIndexKey := fmt.Sprintf("candidate:%s|user:%s", candidate.CandidateAddress, candidate.Base.FromAddress)
	candidateIndex := &pb.CandidateIndex{Ok: true}
	candidateIndexData, _ := json.Marshal(candidateIndex)
	
	ws = append(ws, WriteOp{
		Key:   candidateIndexKey,
		Value: candidateIndexData,
		Del:   false,
	})

	// 记录投票历史
	historyKey := fmt.Sprintf("candidate_history_%s", candidate.Base.TxId)
	historyData, _ := json.Marshal(candidate)
	ws = append(ws, WriteOp{
		Key:   historyKey,
		Value: historyData,
		Del:   false,
	})

	return ws, &Receipt{
		TxID:       candidate.Base.TxId,
		Status:     "SUCCEED",
		WriteCount: len(ws),
	}, nil
}

// handleRemoveVote 处理取消投票
func (h *CandidateTxHandler) handleRemoveVote(candidate *pb.CandidateTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 读取投票者账户
	voterAccountKey := fmt.Sprintf("account_%s", candidate.Base.FromAddress)
	voterAccountData, voterExists, err := sv.Get(voterAccountKey)
	if err != nil || !voterExists {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "voter account not found",
		}, fmt.Errorf("voter account not found")
	}

	var voterAccount pb.Account
	if err := json.Unmarshal(voterAccountData, &voterAccount); err != nil {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse voter account",
		}, err
	}

	// 检查是否有投票记录
	if voterAccount.Candidate == "" {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "no active vote found",
		}, fmt.Errorf("no active vote")
	}

	// 如果指定了候选人地址，验证是否匹配
	if candidate.CandidateAddress != "" && voterAccount.Candidate != candidate.CandidateAddress {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "candidate address mismatch",
		}, fmt.Errorf("voted for %s, not %s", voterAccount.Candidate, candidate.CandidateAddress)
	}

	targetCandidateAddr := voterAccount.Candidate

	// 读取候选人账户
	candidateAccountKey := fmt.Sprintf("account_%s", targetCandidateAddr)
	candidateAccountData, candidateExists, err := sv.Get(candidateAccountKey)
	if err != nil || !candidateExists {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "candidate account not found",
		}, fmt.Errorf("candidate account not found")
	}

	var candidateAccount pb.Account
	if err := json.Unmarshal(candidateAccountData, &candidateAccount); err != nil {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse candidate account",
		}, err
	}

	nativeTokenAddr := "native_token"

	// 获取锁定的投票金额
	if voterAccount.Balances == nil || voterAccount.Balances[nativeTokenAddr] == nil {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "no locked balance found",
		}, fmt.Errorf("no locked balance")
	}

	lockedBalance, _ := new(big.Int).SetString(voterAccount.Balances[nativeTokenAddr].CandidateLockedBalance, 10)
	if lockedBalance == nil || lockedBalance.Sign() <= 0 {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "no locked balance to release",
		}, fmt.Errorf("no locked balance")
	}

	ws := make([]WriteOp, 0)

	// 从锁定余额转回可用余额
	voterAccount.Balances[nativeTokenAddr].CandidateLockedBalance = "0"
	
	currentBalance, _ := new(big.Int).SetString(voterAccount.Balances[nativeTokenAddr].Balance, 10)
	if currentBalance == nil {
		currentBalance = big.NewInt(0)
	}
	newBalance := new(big.Int).Add(currentBalance, lockedBalance)
	voterAccount.Balances[nativeTokenAddr].Balance = newBalance.String()

	// 清除投票记录
	voterAccount.Candidate = ""

	// 保存投票者账户
	updatedVoterData, err := json.Marshal(&voterAccount)
	if err != nil {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "failed to marshal voter account",
		}, err
	}

	ws = append(ws, WriteOp{
		Key:   voterAccountKey,
		Value: updatedVoterData,
		Del:   false,
	})

	// 减少候选人收到的投票数
	candidateVotes, _ := new(big.Int).SetString(candidateAccount.ReceiveVotes, 10)
	if candidateVotes == nil {
		candidateVotes = big.NewInt(0)
	}
	newCandidateVotes := new(big.Int).Sub(candidateVotes, lockedBalance)
	if newCandidateVotes.Sign() < 0 {
		newCandidateVotes = big.NewInt(0)
	}
	candidateAccount.ReceiveVotes = newCandidateVotes.String()

	// 保存候选人账户
	updatedCandidateData, err := json.Marshal(&candidateAccount)
	if err != nil {
		return nil, &Receipt{
			TxID:   candidate.Base.TxId,
			Status: "FAILED",
			Error:  "failed to marshal candidate account",
		}, err
	}

	ws = append(ws, WriteOp{
		Key:   candidateAccountKey,
		Value: updatedCandidateData,
		Del:   false,
	})

	// 删除候选人索引
	candidateIndexKey := fmt.Sprintf("candidate:%s|user:%s", targetCandidateAddr, candidate.Base.FromAddress)
	ws = append(ws, WriteOp{
		Key:   candidateIndexKey,
		Value: nil,
		Del:   true,
	})

	// 记录取消投票历史
	historyKey := fmt.Sprintf("candidate_history_%s", candidate.Base.TxId)
	historyData, _ := json.Marshal(candidate)
	ws = append(ws, WriteOp{
		Key:   historyKey,
		Value: historyData,
		Del:   false,
	})

	return ws, &Receipt{
		TxID:       candidate.Base.TxId,
		Status:     "SUCCEED",
		WriteCount: len(ws),
	}, nil
}

func (h *CandidateTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}

