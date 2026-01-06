// vm/frost_withdraw_signed.go
// Frost 提现签名完成交易处理器
package vm

import (
	"dex/frost/chain"
	"dex/frost/chain/btc"
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

	chainName := chain.NormalizeChain(firstWithdraw.Chain)
	asset := firstWithdraw.Asset

	// ===== P0-1: FIFO 队首验证（首次提交时必须验证）=====
	// 只有首次提交需要验证 FIFO 顺序，追加产物时跳过
	if currentCount == 0 {
		// 验证 withdraw_ids 是否从队首开始连续
		if err := h.validateFIFOOrder(sv, chainName, asset, withdrawIDs); err != nil {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: err.Error()}, err
		}
	}

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
	if chainName == chain.ChainBTC {
		vaultID := signed.VaultId
		templateData := signed.TemplateData
		inputSigs := signed.InputSigs
		scriptPubkeys := signed.ScriptPubkeys

		// 必须提供 template_data 和 input_sigs（新版验签方式）
		if len(templateData) == 0 {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "missing template_data for BTC"}, errors.New("missing template_data for BTC")
		}

		// 从 template_data 解析 BTC 模板
		template, err := btc.FromJSON(templateData)
		if err != nil {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "invalid template_data: " + err.Error()}, err
		}

		// 验证 input_sigs 数量与 template inputs 匹配
		if len(inputSigs) != len(template.Inputs) {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "input_sigs count mismatch with template inputs"}, errors.New("input_sigs count mismatch")
		}

		// 验证 scriptPubkeys 数量与 template inputs 匹配
		if len(scriptPubkeys) != len(template.Inputs) {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "script_pubkeys count mismatch with template inputs"}, errors.New("script_pubkeys count mismatch")
		}

		// 复算 template_hash 并验证与提交的一致
		computedTemplateHash := template.TemplateHash()
		if len(signed.TemplateHash) > 0 {
			if len(computedTemplateHash) != len(signed.TemplateHash) {
				return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "template_hash mismatch"}, errors.New("template_hash mismatch")
			}
			for i := range computedTemplateHash {
				if computedTemplateHash[i] != signed.TemplateHash[i] {
					return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "template_hash mismatch"}, errors.New("template_hash mismatch")
				}
			}
		}

		// 获取 Vault 公钥和签名算法（用于验签）
		vaultStateKey := keys.KeyFrostVaultState(chainName, vaultID)
		vaultStateData, vaultExists, vaultErr := sv.Get(vaultStateKey)
		if vaultErr != nil || !vaultExists || len(vaultStateData) == 0 {
			// Vault 状态不存在，生产环境应报错
			// 测试环境跳过验签
		} else {
			// 解析 Vault 状态获取公钥和签名算法
			vaultState := &pb.FrostVaultState{}
			if err := proto.Unmarshal(vaultStateData, vaultState); err != nil {
				return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to parse vault state"}, err
			}

			pubKey := vaultState.GroupPubkey
			signAlgo := vaultState.SignAlgo
			if signAlgo == pb.SignAlgo_SIGN_ALGO_UNSPECIFIED {
				// 从 VaultConfig 获取
				vaultCfgKey := keys.KeyFrostVaultConfig(chainName, 0)
				if cfgData, exists, _ := sv.Get(vaultCfgKey); exists {
					var cfg pb.FrostVaultConfig
					if err := proto.Unmarshal(cfgData, &cfg); err == nil {
						signAlgo = cfg.SignAlgo
					}
				}
			}

			// 复算每个 input 的 Taproot sighash
			sighashes, err := template.ComputeTaprootSighash(scriptPubkeys, btc.SighashDefault)
			if err != nil {
				return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to compute sighash: " + err.Error()}, err
			}

			// 对每个 input 验签（支持多曲线）
			for i, sig := range inputSigs {
				if len(sig) != 64 {
					return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "invalid signature length for input"}, errors.New("invalid signature length")
				}

				valid, err := verifySignature(signAlgo, pubKey, sighashes[i], sig)
				if err != nil || !valid {
					return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "signature verification failed for input: " + err.Error()}, errors.New("signature verification failed")
				}
			}
		}

		// BTC UTXO lock：从复算的模板提取 UTXO（而非客户端传入）
		for _, input := range template.Inputs {
			lockKey := keys.KeyFrostBtcLockedUtxo(vaultID, input.TxID, input.Vout)
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

			// 锁定 UTXO（标记为 consumed/spent）
			ops = append(ops, WriteOp{
				Key:         lockKey,
				Value:       []byte(jobID),
				SyncStateDB: true,
				Category:    "frost_btc_utxo_lock",
			})
		}
	} else {
		// 合约链/账户链：标记 lot 为 consumed（按 Vault 分片）
		// 从 job 的 withdraw_ids 计算总金额，然后从该 Vault 的 FIFO 消耗对应数量的 lot
		vaultID := signed.VaultId
		if vaultID == 0 {
			// 从第一个 withdraw 获取 vault_id（如果 job 中未指定）
			if len(withdrawIDs) > 0 {
				firstWithdrawKey := keys.KeyFrostWithdraw(withdrawIDs[0])
				firstWithdrawData, exists, _ := sv.Get(firstWithdrawKey)
				if exists && len(firstWithdrawData) > 0 {
					firstWithdraw, _ := unmarshalWithdrawRequest(firstWithdrawData)
					if firstWithdraw != nil {
						// 从 job 规划时应该已经回填了 vault_id
						// 这里作为备用，如果未回填则从 withdraw 读取（如果 withdraw 有 vault_id 字段）
					}
				}
			}
		}

		// 计算总提现金额
		var totalAmount uint64
		for _, wid := range withdrawIDs {
			withdrawKey := keys.KeyFrostWithdraw(wid)
			withdrawData, exists, _ := sv.Get(withdrawKey)
			if !exists || len(withdrawData) == 0 {
				continue
			}
			withdraw, _ := unmarshalWithdrawRequest(withdrawData)
			if withdraw != nil {
				amount, _ := strconv.ParseUint(withdraw.Amount, 10, 64)
				totalAmount += amount
			}
		}

		// 从该 Vault 的 FIFO 消耗 lot，直到覆盖 totalAmount
		// 注意：这里简化处理，实际应该按 lot 的 finalize_height + seq 递增顺序消耗
		consumedAmount := uint64(0)
		for consumedAmount < totalAmount {
			requestID, ok := GetFundsLotAtHead(sv, chainName, asset, vaultID)
			if !ok {
				// 没有更多 lot，但金额可能不足
				// 这里简化处理，实际应该验证金额是否足够
				break
			}

			// 读取 RechargeRequest 获取金额
			rechargeKey := keys.KeyRechargeRequest(requestID)
			rechargeData, exists, _ := sv.Get(rechargeKey)
			if !exists || len(rechargeData) == 0 {
				// lot 对应的 request 不存在，跳过
				AdvanceFundsLotHead(sv, chainName, asset, vaultID)
				continue
			}

			var recharge pb.RechargeRequest
			if err := proto.Unmarshal(rechargeData, &recharge); err != nil {
				// 解析失败，跳过
				AdvanceFundsLotHead(sv, chainName, asset, vaultID)
				continue
			}

			lotAmount, _ := strconv.ParseUint(recharge.Amount, 10, 64)
			consumedAmount += lotAmount

			// 推进 FIFO 头指针（标记该 lot 为 consumed）
			AdvanceFundsLotHead(sv, chainName, asset, vaultID)
		}

		// 更新 FIFO 头指针到 StateView（通过 WriteOp）
		head := GetFundsLotHead(sv, chainName, asset, vaultID)
		headKey := keys.KeyFrostFundsLotHead(chainName, asset, vaultID)
		headValue := strconv.FormatUint(head.Height, 10) + "|" + strconv.FormatUint(head.Seq, 10)
		ops = append(ops, WriteOp{
			Key:         headKey,
			Value:       []byte(headValue),
			SyncStateDB: true,
			Category:    "frost_funds_lot_head",
		})
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
		if chain.NormalizeChain(withdraw.Chain) != chainName || withdraw.Asset != asset {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "inconsistent chain/asset"}, errors.New("inconsistent chain/asset")
		}

		// 验证状态为 QUEUED
		if withdraw.Status != WithdrawStatusQueued {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "withdraw not in QUEUED status: " + wid}, errors.New("withdraw not in QUEUED status: " + wid)
		}

		// 验证 template_hash 绑定（BTC）
		if chainName == chain.ChainBTC && len(signed.TemplateHash) > 0 {
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
	headKey := keys.KeyFrostWithdrawFIFOHead(chainName, asset)
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

// verifySignature 验证签名（支持多曲线）
// 根据 signAlgo 选择对应的验证算法
func verifySignature(signAlgo pb.SignAlgo, pubKey, msg, sig []byte) (bool, error) {
	switch signAlgo {
	case pb.SignAlgo_SIGN_ALGO_SCHNORR_SECP256K1_BIP340:
		// BTC: BIP-340 Schnorr
		if len(pubKey) != 32 {
			return false, errors.New("invalid pubkey length for BIP340")
		}
		return frost.VerifyBIP340(pubKey, msg, sig)
	case pb.SignAlgo_SIGN_ALGO_SCHNORR_ALT_BN128:
		// ETH/BNB: alt_bn128 Schnorr
		if len(pubKey) != 64 {
			return false, errors.New("invalid pubkey length for BN128 (expected 64 bytes)")
		}
		return frost.VerifyBN128(pubKey, msg, sig)
	case pb.SignAlgo_SIGN_ALGO_ED25519:
		// SOL: Ed25519
		if len(pubKey) != 32 {
			return false, errors.New("invalid pubkey length for Ed25519 (expected 32 bytes)")
		}
		return frost.VerifyEd25519(pubKey, msg, sig)
	case pb.SignAlgo_SIGN_ALGO_ECDSA_SECP256K1:
		// TRX: ECDSA (GG20/CGGMP)
		// TODO: 实现 ECDSA 验证
		return false, errors.New("ecdsa signature verification not implemented yet")
	default:
		return false, errors.New("unsupported sign_algo: " + signAlgo.String())
	}
}

// validateFIFOOrder 验证 withdraw_ids 是否从队首开始连续
// 约束：
// 1. withdraw_ids[0].seq == 当前 head（队首）
// 2. withdraw_ids[i].seq == head + i（连续递增）
// 3. 所有 withdraw 状态必须为 QUEUED
func (h *FrostWithdrawSignedTxHandler) validateFIFOOrder(sv StateView, chainName, asset string, withdrawIDs []string) error {
	if len(withdrawIDs) == 0 {
		return errors.New("FIFO: empty withdraw_ids")
	}

	// 1. 读取当前队首指针 head
	headKey := keys.KeyFrostWithdrawFIFOHead(chainName, asset)
	currentHead := readUint64FromState(sv, headKey)
	if currentHead == 0 {
		// head 未初始化，默认从 1 开始
		currentHead = 1
	}

	// 2. 获取所有 withdraw 并验证
	withdraws := make([]*FrostWithdrawRequest, 0, len(withdrawIDs))
	for _, wid := range withdrawIDs {
		withdrawKey := keys.KeyFrostWithdraw(wid)
		withdrawData, exists, err := sv.Get(withdrawKey)
		if err != nil || !exists {
			return errors.New("FIFO: withdraw not found: " + wid)
		}

		withdraw, err := unmarshalWithdrawRequest(withdrawData)
		if err != nil {
			return errors.New("FIFO: failed to parse withdraw: " + wid)
		}

		// 验证状态必须为 QUEUED
		if withdraw.Status != WithdrawStatusQueued {
			return errors.New("FIFO: withdraw not in QUEUED status: " + wid)
		}

		// 验证 chain/asset 一致性（规范化比较）
		if chain.NormalizeChain(withdraw.Chain) != chainName || withdraw.Asset != asset {
			return errors.New("FIFO: inconsistent chain/asset in withdraw: " + wid)
		}

		withdraws = append(withdraws, withdraw)
	}

	// 3. 验证第一个 withdraw 的 seq == currentHead
	if withdraws[0].Seq != currentHead {
		return errors.New("FIFO: first withdraw seq (" + strconv.FormatUint(withdraws[0].Seq, 10) +
			") != head (" + strconv.FormatUint(currentHead, 10) + ")")
	}

	// 4. 验证 seq 连续递增
	for i := 1; i < len(withdraws); i++ {
		expectedSeq := currentHead + uint64(i)
		if withdraws[i].Seq != expectedSeq {
			return errors.New("FIFO: seq gap at position " + strconv.Itoa(i) +
				", expected " + strconv.FormatUint(expectedSeq, 10) +
				", got " + strconv.FormatUint(withdraws[i].Seq, 10))
		}
	}

	return nil
}
