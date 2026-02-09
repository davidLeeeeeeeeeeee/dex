// vm/frost_withdraw_request.go
// Frost 鎻愮幇璇锋眰浜ゆ槗澶勭悊鍣?
package vm

import (
	"crypto/sha256"
	"dex/keys"
	"dex/pb"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
)

// WithdrawRequestStatus 鎻愮幇璇锋眰鐘舵€?
type WithdrawRequestStatus string

const (
	WithdrawStatusQueued WithdrawRequestStatus = "QUEUED"
	WithdrawStatusSigned WithdrawRequestStatus = "SIGNED"
)

// FrostWithdrawRequest 鎻愮幇璇锋眰缁撴瀯锛堥摼涓婂瓨鍌級
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
	JobID         string                `json:"job_id,omitempty"` // 褰?status=SIGNED 鏃跺瓨鍦?
	VaultID       uint32                `json:"vault_id,omitempty"`
}

// FrostWithdrawRequestTxHandler Frost 鎻愮幇璇锋眰浜ゆ槗澶勭悊鍣?
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
	chain := strings.ToLower(req.Chain)
	asset := strings.ToUpper(req.Asset)
	to := req.To
	amount := req.Amount
	requestHeight := req.Base.ExecutedHeight

	// 鍩烘湰鍙傛暟鏍￠獙
	if chain == "" || asset == "" || to == "" || amount == "" {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "missing required fields"}, errors.New("missing required fields")
	}

	// 鏍￠獙閲戦
	amountBI, err := ParseBalance(amount)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "invalid amount format"}, err
	}
	amount = amountBI.String()

	// 骞傜瓑妫€鏌ワ細浣跨敤 tx_id 浣滀负鍞竴鏍囪瘑
	txRefKey := keys.KeyFrostWithdrawTxRef(txID)
	existingWithdrawID, exists, _ := sv.Get(txRefKey)
	if exists && len(existingWithdrawID) > 0 {
		// 宸茬粡澶勭悊杩囷紝杩斿洖鎴愬姛浣嗕笉鍋氫换浣曚慨鏀癸紙骞傜瓑锛?
		return nil, &Receipt{TxID: txID, Status: "SUCCEED", Error: "", WriteCount: 0}, nil
	}

	// 鑾峰彇褰撳墠 (chain, asset) 闃熷垪鐨?seq
	seqKey := keys.KeyFrostWithdrawFIFOSeq(chain, asset)
	currentSeq := readUint64FromState(sv, seqKey)
	newSeq := currentSeq + 1

	// 鐢熸垚 withdraw_id = H(chain || asset || seq || request_height)
	withdrawID := generateWithdrawID(chain, asset, newSeq, requestHeight)

	// 鍒涘缓 WithdrawRequest
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

	// 搴忓垪鍖?WithdrawRequest
	withdrawData, err := marshalWithdrawRequest(withdrawReq)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to marshal withdraw request"}, err
	}

	// 鍑嗗鍐欏叆鎿嶄綔
	ops := make([]WriteOp, 0, 4)

	// 1. 鍐欏叆 WithdrawRequest 鏈韩
	withdrawKey := keys.KeyFrostWithdraw(withdrawID)
	ops = append(ops, WriteOp{
		Key:         withdrawKey,
		Value:       withdrawData,
		SyncStateDB: true,
		Category:    "frost_withdraw",
	})

	// 2. 鍐欏叆 FIFO 绱㈠紩锛歴eq -> withdraw_id
	fifoKey := keys.KeyFrostWithdrawFIFOIndex(chain, asset, newSeq)
	ops = append(ops, WriteOp{
		Key:         fifoKey,
		Value:       []byte(withdrawID),
		SyncStateDB: true,
		Category:    "frost_withdraw_fifo",
	})

	// 3. 鏇存柊 seq 璁℃暟鍣?
	ops = append(ops, WriteOp{
		Key:         seqKey,
		Value:       []byte(strconv.FormatUint(newSeq, 10)),
		SyncStateDB: true,
		Category:    "frost_withdraw_seq",
	})

	// 4. 鍐欏叆 tx_id -> withdraw_id 寮曠敤锛堢敤浜庡箓绛夋鏌ワ級
	ops = append(ops, WriteOp{
		Key:         txRefKey,
		Value:       []byte(withdrawID),
		SyncStateDB: true,
		Category:    "frost_withdraw_ref",
	})

	// 灏?ops 搴旂敤鍒?StateView
	for _, op := range ops {
		sv.Set(op.Key, op.Value)
	}

	return ops, &Receipt{TxID: txID, Status: "SUCCEED", WriteCount: len(ops)}, nil
}

func (h *FrostWithdrawRequestTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}

// generateWithdrawID 鐢熸垚鍏ㄧ綉鍞竴鐨?withdraw_id
// withdraw_id = H(chain || asset || seq || request_height)
func generateWithdrawID(chain, asset string, seq, requestHeight uint64) string {
	data := chain + "|" + asset + "|" + strconv.FormatUint(seq, 10) + "|" + strconv.FormatUint(requestHeight, 10)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// readUint64FromState 浠?StateView 璇诲彇 uint64 鍊?
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

// marshalWithdrawRequest 搴忓垪鍖?WithdrawRequest 涓?protobuf
// 浣跨敤 pb.FrostWithdrawState 杩涜瀛樺偍
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

// unmarshalWithdrawRequest 浠?protobuf 鍙嶅簭鍒楀寲 WithdrawRequest
func unmarshalWithdrawRequest(data []byte) (*FrostWithdrawRequest, error) {
	state := &pb.FrostWithdrawState{}
	if err := unmarshalProtoCompat(data, state); err != nil {
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
