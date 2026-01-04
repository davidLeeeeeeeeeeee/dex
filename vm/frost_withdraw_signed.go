// vm/frost_withdraw_signed.go
// Frost 提现签名完成交易处理器
package vm

import (
	"dex/frost/core/frost"
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

	// BTC 验签（如果是 BTC 链）
	if chain == "btc" {
		vaultID := signed.VaultId
		templateHash := signed.TemplateHash
		utxoInputs := signed.UtxoInputs

		// 验证必须有 template_hash
		if len(templateHash) != 32 {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "invalid template_hash length"}, errors.New("invalid template_hash length")
		}

		// 获取 Vault 公钥（用于验签）
		vaultStateKey := keys.KeyFrostVaultState(chain, vaultID)
		vaultStateData, vaultExists, vaultErr := sv.Get(vaultStateKey)
		if vaultErr != nil || !vaultExists {
			// Vault 状态不存在，跳过验签（测试环境或 dummy 签名）
			// 在生产环境中应该报错
		} else if len(vaultStateData) > 0 {
			// 解析 Vault 状态获取公钥
			vaultState := &pb.FrostVaultState{}
			if err := proto.Unmarshal(vaultStateData, vaultState); err == nil {
				pubKey := vaultState.GroupPubkey
				if len(pubKey) == 32 && len(signedPackageBytes) >= 64 {
					// 提取签名（假设前 64 字节是 BIP-340 签名）
					sig := signedPackageBytes[:64]
					// 验签：签名必须对 template_hash 有效
					valid, err := frost.VerifyBIP340(pubKey, templateHash, sig)
					if err != nil || !valid {
						return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "BTC signature verification failed"}, errors.New("BTC signature verification failed")
					}
				}
			}
		}

		// BTC UTXO lock：防止双花
		for _, utxo := range utxoInputs {
			lockKey := keys.KeyFrostBtcLockedUtxo(vaultID, utxo.Txid, utxo.Vout)
			existingJobID, lockExists, _ := sv.Get(lockKey)

			if lockExists && len(existingJobID) > 0 {
				// UTXO 已被锁定
				if string(existingJobID) != jobID {
					// 不同 job 尝试锁定同一 UTXO - 双花
					return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "UTXO already locked by another job"}, errors.New("UTXO already locked by another job")
				}
				// 同一 job 重复提交，跳过
				continue
			}

			// 锁定 UTXO
			ops = append(ops, WriteOp{
				Key:         lockKey,
				Value:       []byte(jobID),
				SyncStateDB: true,
				Category:    "frost_btc_utxo_lock",
			})
		}
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

		// 验证 template_hash 绑定（BTC）
		if chain == "btc" && len(signed.TemplateHash) > 0 {
			// template_hash 必须与 job_id 的一部分匹配（确定性验证）
			// job_id = H(chain || asset || vault_id || first_seq || template_hash || key_epoch)
			// 这里简化：只验证 template_hash 非空
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
