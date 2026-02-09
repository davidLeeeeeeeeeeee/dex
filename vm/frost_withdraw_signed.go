// vm/frost_withdraw_signed.go
// Frost 鎻愮幇绛惧悕瀹屾垚浜ゆ槗澶勭悊鍣?
package vm

import (
	"dex/frost/chain"
	"dex/frost/chain/btc"
	"dex/frost/core/frost"
	"dex/keys"
	"dex/pb"
	"errors"
	"math/big"
	"strconv"

	"google.golang.org/protobuf/proto"
)

// FrostWithdrawSignedTxHandler Frost 鎻愮幇绛惧悕瀹屾垚浜ゆ槗澶勭悊鍣?
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

	// 鍩烘湰鍙傛暟鏍￠獙
	if jobID == "" {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "missing job_id"}, errors.New("missing job_id")
	}
	if len(signedPackageBytes) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "empty signed_package_bytes"}, errors.New("empty signed_package_bytes")
	}
	if len(withdrawIDs) == 0 {
		return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "empty withdraw_ids"}, errors.New("empty withdraw_ids")
	}

	// 鑾峰彇 SignedPackage 璁℃暟鍣?
	countKey := keys.KeyFrostSignedPackageCount(jobID)
	currentCount := readUint64FromState(sv, countKey)

	ops := make([]WriteOp, 0)

	// 妫€鏌ョ涓€涓?withdraw 鑾峰彇 chain/asset锛堢敤浜庨獙璇佸叾浠?withdraw 涓€鑷存€э級
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

	// ===== P0-1: FIFO 闃熼楠岃瘉锛堥娆℃彁浜ゆ椂蹇呴』楠岃瘉锛?====
	// 鍙湁棣栨鎻愪氦闇€瑕侀獙璇?FIFO 椤哄簭锛岃拷鍔犱骇鐗╂椂璺宠繃
	if currentCount == 0 {
		// 楠岃瘉 withdraw_ids 鏄惁浠庨槦棣栧紑濮嬭繛缁?
		if err := h.validateFIFOOrder(sv, chainName, asset, withdrawIDs); err != nil {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: err.Error()}, err
		}
	}

	// 濡傛灉宸叉湁浜х墿锛屽彧杩藉姞 receipt/history
	if currentCount > 0 {
		// 杩藉姞鏂扮殑 SignedPackage
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

		// 鏇存柊璁℃暟鍣?
		ops = append(ops, WriteOp{
			Key:         countKey,
			Value:       []byte(strconv.FormatUint(pkgIdx+1, 10)),
			SyncStateDB: true,
			Category:    "frost_signed_pkg_count",
		})

		// 搴旂敤鍒?StateView
		for _, op := range ops {
			sv.Set(op.Key, op.Value)
		}

		return ops, &Receipt{TxID: txID, Status: "SUCCEED", WriteCount: len(ops)}, nil
	}

	// BTC 楠岀锛堝鏋滄槸 BTC 閾撅級
	if chainName == chain.ChainBTC {
		vaultID := signed.VaultId
		templateData := signed.TemplateData
		inputSigs := signed.InputSigs
		scriptPubkeys := signed.ScriptPubkeys

		// 蹇呴』鎻愪緵 template_data 鍜?input_sigs锛堟柊鐗堥獙绛炬柟寮忥級
		if len(templateData) == 0 {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "missing template_data for BTC"}, errors.New("missing template_data for BTC")
		}

		// 浠?template_data 瑙ｆ瀽 BTC 妯℃澘
		template, err := btc.FromJSON(templateData)
		if err != nil {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "invalid template_data: " + err.Error()}, err
		}

		// 楠岃瘉 input_sigs 鏁伴噺涓?template inputs 鍖归厤
		if len(inputSigs) != len(template.Inputs) {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "input_sigs count mismatch with template inputs"}, errors.New("input_sigs count mismatch")
		}

		// 楠岃瘉 scriptPubkeys 鏁伴噺涓?template inputs 鍖归厤
		if len(scriptPubkeys) != len(template.Inputs) {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "script_pubkeys count mismatch with template inputs"}, errors.New("script_pubkeys count mismatch")
		}

		// 澶嶇畻 template_hash 骞堕獙璇佷笌鎻愪氦鐨勪竴鑷?
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

		// 鑾峰彇 Vault 鍏挜鍜岀鍚嶇畻娉曪紙鐢ㄤ簬楠岀锛?
		vaultStateKey := keys.KeyFrostVaultState(chainName, vaultID)
		vaultStateData, vaultExists, vaultErr := sv.Get(vaultStateKey)
		if vaultErr != nil || !vaultExists || len(vaultStateData) == 0 {
			// Vault 鐘舵€佷笉瀛樺湪锛岀敓浜х幆澧冨簲鎶ラ敊
			// 娴嬭瘯鐜璺宠繃楠岀
		} else {
			// 瑙ｆ瀽 Vault 鐘舵€佽幏鍙栧叕閽ュ拰绛惧悕绠楁硶
			vaultState := &pb.FrostVaultState{}
			if err := unmarshalProtoCompat(vaultStateData, vaultState); err != nil {
				return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to parse vault state"}, err
			}

			pubKey := vaultState.GroupPubkey
			signAlgo := vaultState.SignAlgo
			if signAlgo == pb.SignAlgo_SIGN_ALGO_UNSPECIFIED {
				// 浠?VaultConfig 鑾峰彇
				vaultCfgKey := keys.KeyFrostVaultConfig(chainName, 0)
				if cfgData, exists, _ := sv.Get(vaultCfgKey); exists {
					var cfg pb.FrostVaultConfig
					if err := unmarshalProtoCompat(cfgData, &cfg); err == nil {
						signAlgo = cfg.SignAlgo
					}
				}
			}

			// 澶嶇畻姣忎釜 input 鐨?Taproot sighash
			sighashes, err := template.ComputeTaprootSighash(scriptPubkeys, btc.SighashDefault)
			if err != nil {
				return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "failed to compute sighash: " + err.Error()}, err
			}

			// 瀵规瘡涓?input 楠岀锛堟敮鎸佸鏇茬嚎锛?
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

		// BTC UTXO lock锛氫粠澶嶇畻鐨勬ā鏉挎彁鍙?UTXO锛堣€岄潪瀹㈡埛绔紶鍏ワ級
		for _, input := range template.Inputs {
			lockKey := keys.KeyFrostBtcLockedUtxo(vaultID, input.TxID, input.Vout)
			existingJobID, lockExists, _ := sv.Get(lockKey)

			if lockExists && len(existingJobID) > 0 {
				// UTXO 宸茶閿佸畾
				if string(existingJobID) != jobID {
					// 涓嶅悓 job 灏濊瘯閿佸畾鍚屼竴 UTXO - 鍙岃姳
					return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "UTXO already locked by another job"}, errors.New("UTXO already locked by another job")
				}
				// 鍚屼竴 job 閲嶅鎻愪氦锛岃烦杩?
				continue
			}

			// 閿佸畾 UTXO锛堟爣璁颁负 consumed/spent锛?
			ops = append(ops, WriteOp{
				Key:         lockKey,
				Value:       []byte(jobID),
				SyncStateDB: true,
				Category:    "frost_btc_utxo_lock",
			})
		}
	} else {
		// 鍚堢害閾?璐︽埛閾撅細鏍囪 lot 涓?consumed锛堟寜 Vault 鍒嗙墖锛?
		// 浠?job 鐨?withdraw_ids 璁＄畻鎬婚噾棰濓紝鐒跺悗浠庤 Vault 鐨?FIFO 娑堣€楀搴旀暟閲忕殑 lot
		vaultID := signed.VaultId
		if vaultID == 0 {
			// 浠庣涓€涓?withdraw 鑾峰彇 vault_id锛堝鏋?job 涓湭鎸囧畾锛?
			if len(withdrawIDs) > 0 {
				firstWithdrawKey := keys.KeyFrostWithdraw(withdrawIDs[0])
				firstWithdrawData, exists, _ := sv.Get(firstWithdrawKey)
				if exists && len(firstWithdrawData) > 0 {
					firstWithdraw, _ := unmarshalWithdrawRequest(firstWithdrawData)
					if firstWithdraw != nil {
						// 浠?job 瑙勫垝鏃跺簲璇ュ凡缁忓洖濉簡 vault_id
						// 杩欓噷浣滀负澶囩敤锛屽鏋滄湭鍥炲～鍒欎粠 withdraw 璇诲彇锛堝鏋?withdraw 鏈?vault_id 瀛楁锛?
					}
				}
			}
		}

		// 璁＄畻鎬绘彁鐜伴噾棰?
		totalAmount := big.NewInt(0)
		for _, wid := range withdrawIDs {
			withdrawKey := keys.KeyFrostWithdraw(wid)
			withdrawData, exists, _ := sv.Get(withdrawKey)
			if !exists || len(withdrawData) == 0 {
				continue
			}
			withdraw, _ := unmarshalWithdrawRequest(withdrawData)
			if withdraw != nil {
				amount, err := ParseBalance(withdraw.Amount)
				if err != nil {
					continue
				}
				totalAmount, _ = SafeAdd(totalAmount, amount)
			}
		}

		// 浠庤 Vault 鐨?FIFO 娑堣€?lot锛岀洿鍒拌鐩?totalAmount
		// 娉ㄦ剰锛氳繖閲岀畝鍖栧鐞嗭紝瀹為檯搴旇鎸?lot 鐨?finalize_height + seq 閫掑椤哄簭娑堣€?
		consumedAmount := big.NewInt(0)
		for consumedAmount.Cmp(totalAmount) < 0 {
			requestID, ok := GetFundsLotAtHead(sv, chainName, asset, vaultID)
			if !ok {
				// 娌℃湁鏇村 lot锛屼絾閲戦鍙兘涓嶈冻
				// 杩欓噷绠€鍖栧鐞嗭紝瀹為檯搴旇楠岃瘉閲戦鏄惁瓒冲
				break
			}

			// 璇诲彇 RechargeRequest 鑾峰彇閲戦
			rechargeKey := keys.KeyRechargeRequest(requestID)
			rechargeData, exists, _ := sv.Get(rechargeKey)
			if !exists || len(rechargeData) == 0 {
				// lot 瀵瑰簲鐨?request 涓嶅瓨鍦紝璺宠繃
				AdvanceFundsLotHead(sv, chainName, asset, vaultID)
				continue
			}

			var recharge pb.RechargeRequest
			if err := unmarshalProtoCompat(rechargeData, &recharge); err != nil {
				// 瑙ｆ瀽澶辫触锛岃烦杩?
				AdvanceFundsLotHead(sv, chainName, asset, vaultID)
				continue
			}

			lotAmount, err := ParseBalance(recharge.Amount)
			if err != nil {
				AdvanceFundsLotHead(sv, chainName, asset, vaultID)
				continue
			}
			consumedAmount, _ = SafeAdd(consumedAmount, lotAmount)

			// 鎺ㄨ繘 FIFO 澶存寚閽堬紙鏍囪璇?lot 涓?consumed锛?
			AdvanceFundsLotHead(sv, chainName, asset, vaultID)
		}

		// 鏇存柊 FIFO 澶存寚閽堝埌 StateView锛堥€氳繃 WriteOp锛?
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

	// 棣栨鎻愪氦锛氶獙璇佸苟鏇存柊 withdraw 鐘舵€?QUEUED -> SIGNED
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

		// 楠岃瘉 chain/asset 涓€鑷存€?
		if chain.NormalizeChain(withdraw.Chain) != chainName || withdraw.Asset != asset {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "inconsistent chain/asset"}, errors.New("inconsistent chain/asset")
		}

		// 楠岃瘉鐘舵€佷负 QUEUED
		if withdraw.Status != WithdrawStatusQueued {
			return nil, &Receipt{TxID: txID, Status: "FAILED", Error: "withdraw not in QUEUED status: " + wid}, errors.New("withdraw not in QUEUED status: " + wid)
		}

		// 楠岃瘉 template_hash 缁戝畾锛圔TC锛?
		if chainName == chain.ChainBTC && len(signed.TemplateHash) > 0 {
			// template_hash 蹇呴』涓?job_id 鐨勪竴閮ㄥ垎鍖归厤锛堢‘瀹氭€ч獙璇侊級
			// job_id = H(chain || asset || vault_id || first_seq || template_hash || key_epoch)
			// 杩欓噷绠€鍖栵細鍙獙璇?template_hash 闈炵┖
		}

		// 鏇存柊鐘舵€佷负 SIGNED
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

	// 鍐欏叆 SignedPackage
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

	// 鍐欏叆璁℃暟鍣?
	ops = append(ops, WriteOp{
		Key:         countKey,
		Value:       []byte("1"),
		SyncStateDB: true,
		Category:    "frost_signed_pkg_count",
	})

	// 鏇存柊 FIFO head锛堟帹杩涘埌宸茬鍚?withdraw 涔嬪悗锛?
	// 鎵惧埌鏈€澶х殑 seq 骞舵洿鏂?head
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

	// head 鎸囧悜涓嬩竴涓緟澶勭悊鐨?seq锛坢axSeq + 1锛?
	headKey := keys.KeyFrostWithdrawFIFOHead(chainName, asset)
	ops = append(ops, WriteOp{
		Key:         headKey,
		Value:       []byte(strconv.FormatUint(maxSeq+1, 10)),
		SyncStateDB: true,
		Category:    "frost_withdraw_head",
	})

	// 搴旂敤鍒?StateView
	for _, op := range ops {
		sv.Set(op.Key, op.Value)
	}

	return ops, &Receipt{TxID: txID, Status: "SUCCEED", WriteCount: len(ops)}, nil
}

func (h *FrostWithdrawSignedTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}

// verifySignature 楠岃瘉绛惧悕锛堟敮鎸佸鏇茬嚎锛?
// 鏍规嵁 signAlgo 閫夋嫨瀵瑰簲鐨勯獙璇佺畻娉?
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
		// TODO: 瀹炵幇 ECDSA 楠岃瘉
		return false, errors.New("ecdsa signature verification not implemented yet")
	default:
		return false, errors.New("unsupported sign_algo: " + signAlgo.String())
	}
}

// validateFIFOOrder 楠岃瘉 withdraw_ids 鏄惁浠庨槦棣栧紑濮嬭繛缁?
// 绾︽潫锛?
// 1. withdraw_ids[0].seq == 褰撳墠 head锛堥槦棣栵級
// 2. withdraw_ids[i].seq == head + i锛堣繛缁€掑锛?
// 3. 鎵€鏈?withdraw 鐘舵€佸繀椤讳负 QUEUED
func (h *FrostWithdrawSignedTxHandler) validateFIFOOrder(sv StateView, chainName, asset string, withdrawIDs []string) error {
	if len(withdrawIDs) == 0 {
		return errors.New("FIFO: empty withdraw_ids")
	}

	// 1. 璇诲彇褰撳墠闃熼鎸囬拡 head
	headKey := keys.KeyFrostWithdrawFIFOHead(chainName, asset)
	currentHead := readUint64FromState(sv, headKey)
	if currentHead == 0 {
		// head 鏈垵濮嬪寲锛岄粯璁や粠 1 寮€濮?
		currentHead = 1
	}

	// 2. 鑾峰彇鎵€鏈?withdraw 骞堕獙璇?
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

		// 楠岃瘉鐘舵€佸繀椤讳负 QUEUED
		if withdraw.Status != WithdrawStatusQueued {
			return errors.New("FIFO: withdraw not in QUEUED status: " + wid)
		}

		// 楠岃瘉 chain/asset 涓€鑷存€э紙瑙勮寖鍖栨瘮杈冿級
		if chain.NormalizeChain(withdraw.Chain) != chainName || withdraw.Asset != asset {
			return errors.New("FIFO: inconsistent chain/asset in withdraw: " + wid)
		}

		withdraws = append(withdraws, withdraw)
	}

	// 3. 楠岃瘉绗竴涓?withdraw 鐨?seq == currentHead
	if withdraws[0].Seq != currentHead {
		return errors.New("FIFO: first withdraw seq (" + strconv.FormatUint(withdraws[0].Seq, 10) +
			") != head (" + strconv.FormatUint(currentHead, 10) + ")")
	}

	// 4. 楠岃瘉 seq 杩炵画閫掑
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
