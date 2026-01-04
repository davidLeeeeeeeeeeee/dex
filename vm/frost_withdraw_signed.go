// vm/frost_withdraw_signed.go
// Frost 提现签名完成交易处理器
package vm

import (
	"dex/keys"
	"dex/pb"
	"errors"
	"strconv"

	"google.golang.org/protobuf/proto"
)

// FrostWithdrawSignedTxHandler Frost 提现签名完成交易处理器
type FrostWithdrawSignedTxHandler struct{}

func (h *FrostWithdrawSignedTxHandler) Kind() string {
	return "frost_withdraw_signed"
}

func (h *FrostWithdrawSignedTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	signedTx, ok := tx.GetContent().(*pb.AnyTx_FrostWithdrawSignedTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a frost withdraw signed transaction"}, errors.New("not a frost withdraw signed transaction")
	}

	signed := signedTx.FrostWithdrawSignedTx
	if signed == nil || signed.Base == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid frost withdraw signed transaction"}, errors.New("invalid frost withdraw signed transaction")
	}

	txID := signed.Base.TxId
	jobID := signed.JobId
	signedPackageBytes := signed.SignedPackageBytes
	withdrawIDs := signed.WithdrawIds
	submitHeight := signed.Base.ExecutedHeight
	submitter := signed.Base.FromAddress

	// 基本参数校验
	if jobID == "" {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "missing job_id"}, errors.New("missing job_id")
	}
	if len(signedPackageBytes) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "empty signed_package_bytes"}, errors.New("empty signed_package_bytes")
	}
	if len(withdrawIDs) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "empty withdraw_ids"}, errors.New("empty withdraw_ids")
	}

	// 获取 SignedPackage 计数器
	countKey := keys.KeyFrostSignedPackageCount(jobID)
	currentCount := readUint64FromState(sv, countKey)

	ops := make([]WriteOp, 0)

	// 检查第一个 withdraw 获取 chain/asset（用于验证其他 withdraw 一致性）
	firstWithdrawID := withdrawIDs[0]
	firstWithdrawKey := keys.KeyFrostWithdraw(firstWithdrawID)
	firstWithdrawData, exists, err := sv.Get(firstWithdrawKey)
	if err != nil || !exists {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "withdraw not found: " + firstWithdrawID}, errors.New("withdraw not found: " + firstWithdrawID)
	}

	firstWithdraw, err := unmarshalWithdrawRequest(firstWithdrawData)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to parse withdraw"}, err
	}

	chain := firstWithdraw.Chain
	asset := firstWithdraw.Asset

	// 如果已有产物，只追加 receipt/history
	if currentCount > 0 {
		// 追加新的 SignedPackage
		pkgIdx := currentCount
		pkg := &pb.FrostSignedPackage{
			JobId:              jobID,
			Idx:                pkgIdx,
			SignedPackageBytes: signedPackageBytes,
			SubmitHeight:       submitHeight,
			Submitter:          submitter,
		}
		pkgData, err := proto.Marshal(pkg)
		if err != nil {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to marshal signed package"}, err
		}
		pkgKey := keys.KeyFrostSignedPackage(jobID, pkgIdx)
		ops = append(ops, WriteOp{
			Key:         pkgKey,
			Value:       pkgData,
			SyncStateDB: true,
			Category:    "frost_signed_pkg",
		})

		// 更新计数器
		ops = append(ops, WriteOp{
			Key:         countKey,
			Value:       []byte(strconv.FormatUint(pkgIdx+1, 10)),
			SyncStateDB: true,
			Category:    "frost_signed_pkg_count",
		})

		// 应用到 StateView
		for _, op := range ops {
			sv.Set(op.Key, op.Value)
		}

		return ops, &Receipt{TxID: txID, Status: "SUCCEED", WriteCount: len(ops)}, nil
	}

	// 首次提交：验证并更新 withdraw 状态 QUEUED -> SIGNED
	for _, wid := range withdrawIDs {
		withdrawKey := keys.KeyFrostWithdraw(wid)
		withdrawData, exists, err := sv.Get(withdrawKey)
		if err != nil || !exists {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "withdraw not found: " + wid}, errors.New("withdraw not found: " + wid)
		}

		withdraw, err := unmarshalWithdrawRequest(withdrawData)
		if err != nil {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to parse withdraw"}, err
		}

		// 验证 chain/asset 一致性
		if withdraw.Chain != chain || withdraw.Asset != asset {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "inconsistent chain/asset"}, errors.New("inconsistent chain/asset")
		}

		// 验证状态为 QUEUED
		if withdraw.Status != WithdrawStatusQueued {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "withdraw not in QUEUED status: " + wid}, errors.New("withdraw not in QUEUED status: " + wid)
		}

		// 更新状态为 SIGNED
		withdraw.Status = WithdrawStatusSigned
		withdraw.JobID = jobID

		updatedData, err := marshalWithdrawRequest(withdraw)
		if err != nil {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to marshal withdraw"}, err
		}

		ops = append(ops, WriteOp{
			Key:         withdrawKey,
			Value:       updatedData,
			SyncStateDB: true,
			Category:    "frost_withdraw",
		})
	}

	// 写入 SignedPackage
	pkg := &pb.FrostSignedPackage{
		JobId:              jobID,
		Idx:                0,
		SignedPackageBytes: signedPackageBytes,
		SubmitHeight:       submitHeight,
		Submitter:          submitter,
	}
	pkgData, err := proto.Marshal(pkg)
	if err != nil {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to marshal signed package"}, err
	}
	pkgKey := keys.KeyFrostSignedPackage(jobID, 0)
	ops = append(ops, WriteOp{
		Key:         pkgKey,
		Value:       pkgData,
		SyncStateDB: true,
		Category:    "frost_signed_pkg",
	})

	// 写入计数器
	ops = append(ops, WriteOp{
		Key:         countKey,
		Value:       []byte("1"),
		SyncStateDB: true,
		Category:    "frost_signed_pkg_count",
	})

	// 更新 FIFO head（推进到已签名 withdraw 之后）
	// 找到最大的 seq 并更新 head
	var maxSeq uint64
	for _, wid := range withdrawIDs {
		withdrawKey := keys.KeyFrostWithdraw(wid)
		withdrawData, _, _ := sv.Get(withdrawKey)
		if len(withdrawData) > 0 {
			withdraw, _ := unmarshalWithdrawRequest(withdrawData)
			if withdraw != nil && withdraw.Seq > maxSeq {
				maxSeq = withdraw.Seq
			}
		}
	}

	// head 指向下一个待处理的 seq（maxSeq + 1）
	headKey := keys.KeyFrostWithdrawFIFOHead(chain, asset)
	ops = append(ops, WriteOp{
		Key:         headKey,
		Value:       []byte(strconv.FormatUint(maxSeq+1, 10)),
		SyncStateDB: true,
		Category:    "frost_withdraw_head",
	})

	// 应用到 StateView
	for _, op := range ops {
		sv.Set(op.Key, op.Value)
	}

	return ops, &Receipt{TxID: txID, Status: "SUCCEED", WriteCount: len(ops)}, nil
}

func (h *FrostWithdrawSignedTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}
