// vm/frost_withdraw_request.go
// Frost 提现请求交易处理器
package vm

import (
	"crypto/sha256"
	"dex/keys"
	"dex/pb"
	"encoding/hex"
	"errors"
	"strconv"

	"google.golang.org/protobuf/proto"
)

// WithdrawRequestStatus 提现请求状态
type WithdrawRequestStatus string

const (
	WithdrawStatusQueued WithdrawRequestStatus = "QUEUED"
	WithdrawStatusSigned WithdrawRequestStatus = "SIGNED"
)

// FrostWithdrawRequest 提现请求结构（链上存储）
type FrostWithdrawRequest struct {
	WithdrawID    string                `json:"withdraw_id"`
	TxID          string                `json:"tx_id"`
	Seq           uint64                `json:"seq"`
	Chain         string                `json:"chain"`
	Asset         string                `json:"asset"`
	To            string                `json:"to"`
	Amount        string                `json:"amount"`
	RequestHeight uint64                `json:"request_height"`
	Status        WithdrawRequestStatus `json:"status"`
	JobID         string                `json:"job_id,omitempty"` // 当 status=SIGNED 时存在
	VaultID       uint32                `json:"vault_id,omitempty"`
}

// FrostWithdrawRequestTxHandler Frost 提现请求交易处理器
type FrostWithdrawRequestTxHandler struct{}

func (h *FrostWithdrawRequestTxHandler) Kind() string {
	return "frost_withdraw_request"
}

func (h *FrostWithdrawRequestTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	reqTx, ok := tx.GetContent().(*pb.AnyTx_FrostWithdrawRequestTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a frost withdraw request transaction"}, errors.New("not a frost withdraw request transaction")
	}

	req := reqTx.FrostWithdrawRequestTx
	if req == nil || req.Base == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid frost withdraw request transaction"}, errors.New("invalid frost withdraw request transaction")
	}

	txID := req.Base.TxId
	chain := req.Chain
	asset := req.Asset
	to := req.To
	amount := req.Amount
	requestHeight := req.Base.ExecutedHeight

	// 基本参数校验
	if chain == "" || asset == "" || to == "" || amount == "" {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "missing required fields"}, errors.New("missing required fields")
	}

	// 幂等检查：使用 tx_id 作为唯一标识
	txRefKey := keys.KeyFrostWithdrawTxRef(txID)
	existingWithdrawID, exists, _ := sv.Get(txRefKey)
	if exists && len(existingWithdrawID) > 0 {
		// 已经处理过，返回成功但不做任何修改（幂等）
		return nil, &Receipt{TxID: txID, Status: "SUCCEED", Error: "", WriteCount: 0}, nil
	}

	// 获取当前 (chain, asset) 队列的 seq
	seqKey := keys.KeyFrostWithdrawFIFOSeq(chain, asset)
	currentSeq := readUint64FromState(sv, seqKey)
	newSeq := currentSeq + 1

	// 生成 withdraw_id = H(chain || asset || seq || request_height)
	withdrawID := generateWithdrawID(chain, asset, newSeq, requestHeight)

	// 创建 WithdrawRequest
	withdrawReq := &FrostWithdrawRequest{
		WithdrawID:    withdrawID,
		TxID:          txID,
		Seq:           newSeq,
		Chain:         chain,
		Asset:         asset,
		To:            to,
		Amount:        amount,
		RequestHeight: requestHeight,
		Status:        WithdrawStatusQueued,
	}

	// 序列化 WithdrawRequest
	withdrawData, err := marshalWithdrawRequest(withdrawReq)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to marshal withdraw request"}, err
	}

	// 准备写入操作
	ops := make([]WriteOp, 0, 4)

	// 1. 写入 WithdrawRequest 本身
	withdrawKey := keys.KeyFrostWithdraw(withdrawID)
	ops = append(ops, WriteOp{
		Key:         withdrawKey,
		Value:       withdrawData,
		SyncStateDB: true,
		Category:    "frost_withdraw",
	})

	// 2. 写入 FIFO 索引：seq -> withdraw_id
	fifoKey := keys.KeyFrostWithdrawFIFOIndex(chain, asset, newSeq)
	ops = append(ops, WriteOp{
		Key:         fifoKey,
		Value:       []byte(withdrawID),
		SyncStateDB: true,
		Category:    "frost_withdraw_fifo",
	})

	// 3. 更新 seq 计数器
	ops = append(ops, WriteOp{
		Key:         seqKey,
		Value:       []byte(strconv.FormatUint(newSeq, 10)),
		SyncStateDB: true,
		Category:    "frost_withdraw_seq",
	})

	// 4. 写入 tx_id -> withdraw_id 引用（用于幂等检查）
	ops = append(ops, WriteOp{
		Key:         txRefKey,
		Value:       []byte(withdrawID),
		SyncStateDB: true,
		Category:    "frost_withdraw_ref",
	})

	// 将 ops 应用到 StateView
	for _, op := range ops {
		sv.Set(op.Key, op.Value)
	}

	return ops, &Receipt{TxID: txID, Status: "SUCCEED", WriteCount: len(ops)}, nil
}

func (h *FrostWithdrawRequestTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}

// generateWithdrawID 生成全网唯一的 withdraw_id
// withdraw_id = H(chain || asset || seq || request_height)
func generateWithdrawID(chain, asset string, seq, requestHeight uint64) string {
	data := chain + "|" + asset + "|" + strconv.FormatUint(seq, 10) + "|" + strconv.FormatUint(requestHeight, 10)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// readUint64FromState 从 StateView 读取 uint64 值
func readUint64FromState(sv StateView, key string) uint64 {
	data, exists, err := sv.Get(key)
	if err != nil || !exists || len(data) == 0 {
		return 0
	}
	n, err := strconv.ParseUint(string(data), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// marshalWithdrawRequest 序列化 WithdrawRequest 为 protobuf
// 使用 pb.FrostWithdrawState 进行存储
func marshalWithdrawRequest(req *FrostWithdrawRequest) ([]byte, error) {
	state := &pb.FrostWithdrawState{
		WithdrawId:    req.WithdrawID,
		TxId:          req.TxID,
		Seq:           req.Seq,
		Chain:         req.Chain,
		Asset:         req.Asset,
		To:            req.To,
		Amount:        req.Amount,
		RequestHeight: req.RequestHeight,
		Status:        string(req.Status),
		JobId:         req.JobID,
		VaultId:       req.VaultID,
	}
	return proto.Marshal(state)
}

// unmarshalWithdrawRequest 从 protobuf 反序列化 WithdrawRequest
func unmarshalWithdrawRequest(data []byte) (*FrostWithdrawRequest, error) {
	state := &pb.FrostWithdrawState{}
	if err := proto.Unmarshal(data, state); err != nil {
		return nil, err
	}
	return &FrostWithdrawRequest{
		WithdrawID:    state.WithdrawId,
		TxID:          state.TxId,
		Seq:           state.Seq,
		Chain:         state.Chain,
		Asset:         state.Asset,
		To:            state.To,
		Amount:        state.Amount,
		RequestHeight: state.RequestHeight,
		Status:        WithdrawRequestStatus(state.Status),
		JobID:         state.JobId,
		VaultID:       state.VaultId,
	}, nil
}
