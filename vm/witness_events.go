// vm/witness_events.go
package vm

import (
	"dex/keys"
	"dex/pb"
	"dex/witness"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
)

type metaSetter interface {
	SetWithMeta(key string, value []byte, syncStateDB bool, category string)
}

func (x *Executor) applyWitnessFinalizedEvents(sv StateView, fallbackHeight uint64) error {
	if x.WitnessService == nil {
		return nil
	}

	for {
		select {
		case evt := <-x.WitnessService.Events():
			if evt == nil {
				continue
			}
			switch evt.Type {
			case witness.EventRechargeFinalized:
				req, ok := evt.Data.(*pb.RechargeRequest)
				if !ok || req == nil {
					continue
				}
				if err := applyRechargeFinalized(sv, req, fallbackHeight); err != nil {
					return err
				}
			case witness.EventRechargeRejected:
				req, ok := evt.Data.(*pb.RechargeRequest)
				if !ok || req == nil {
					continue
				}
				if err := applyRechargeRejected(sv, req); err != nil {
					return err
				}
			case witness.EventChallengePeriodStart, witness.EventScopeExpanded, witness.EventRechargeRequested, witness.EventRechargeShelved:
				// 杩欎簺浜嬩欢鎰忓懗鐫€ RechargeRequest 鐨勭姸鎬佹垨灞炴€у凡缁忔敼鍙橈紝闇€瑕佹寔涔呭寲
				req, ok := evt.Data.(*pb.RechargeRequest)
				if !ok || req == nil {
					continue
				}
				if err := persistRechargeRequest(sv, req); err != nil {
					return err
				}
			default:
				continue
			}
		default:
			return nil
		}
	}
}

func applyRechargeFinalized(sv StateView, req *pb.RechargeRequest, fallbackHeight uint64) error {
	if req == nil || req.RequestId == "" {
		return nil
	}

	requestKey := keys.KeyRechargeRequest(req.RequestId)
	requestData, exists, err := sv.Get(requestKey)
	if err != nil || !exists {
		return nil
	}

	var stored pb.RechargeRequest
	if err := unmarshalProtoCompat(requestData, &stored); err != nil {
		return err
	}

	if stored.Status == pb.RechargeRequestStatus_RECHARGE_FINALIZED && stored.FinalizeHeight > 0 {
		return nil
	}

	stored.Status = pb.RechargeRequestStatus_RECHARGE_FINALIZED
	if req.FinalizeHeight > 0 {
		stored.FinalizeHeight = req.FinalizeHeight
	} else if stored.FinalizeHeight == 0 {
		stored.FinalizeHeight = fallbackHeight
	}

	updatedRequestData, err := proto.Marshal(&stored)
	if err != nil {
		return err
	}
	setWithMeta(sv, requestKey, updatedRequestData, true, "witness_request")

	removePendingFunds(sv, stored.RequestId)

	chain := stored.NativeChain
	asset := stored.TokenAddress
	vaultID := stored.VaultId // 浣跨敤鍏ヨ处鏃跺垎閰嶇殑 vault_id锛屼繚璇佽祫閲戞寜 Vault 鍒嗙墖
	finalizeHeight := stored.FinalizeHeight

	// 妫€鏌?Vault lifecycle锛欴RAINING 鐨?Vault 涓嶅啀鎺ュ彈鏂板叆璐?
	vaultStateKey := keys.KeyFrostVaultState(chain, vaultID)
	vaultStateData, exists, _ := sv.Get(vaultStateKey)
	if exists && len(vaultStateData) > 0 {
		var vaultState pb.FrostVaultState
		if err := unmarshalProtoCompat(vaultStateData, &vaultState); err == nil {
			if vaultState.Status == VaultLifecycleDraining {
				// DRAINING 鐘舵€佺殑 Vault 涓嶅啀鎺ュ彈鏂板叆璐?
				// 娉ㄦ剰锛氳繖閲屽簲璇ユ嫆缁濓紝浣嗙敱浜庢槸 finalized 浜嬩欢锛屽彲鑳藉凡缁忓彂鐢燂紝璁板綍璀﹀憡
				// 瀹為檯搴旇鍦?witness_handler.go 涓嫆缁?
				return fmt.Errorf("vault %d is DRAINING, cannot accept new recharge", vaultID)
			}
		}
	}

	// 鍖哄垎 BTC 鍜岃处鎴烽摼/鍚堢害閾剧殑澶勭悊
	if chain == "btc" {
		// BTC锛氬啓鍏?UTXO
		txid := stored.NativeTxHash
		vout := stored.NativeVout

		// 闄嶇骇澶勭悊锛氶拡瀵规棫鏍煎紡 "txid:vout"
		if vout == 0 && strings.Contains(txid, ":") {
			if idx := strings.Index(txid, ":"); idx != -1 {
				if v, err := strconv.ParseUint(txid[idx+1:], 10, 32); err == nil {
					vout = uint32(v)
					txid = txid[:idx]
				}
			}
		}

		// 鍐欏叆 UTXO 璇︽儏
		utxoKey := keys.KeyFrostBtcUtxo(vaultID, txid, vout)
		utxo := &pb.FrostUtxo{
			Txid:           txid,
			Vout:           vout,
			Amount:         stored.Amount, // 浣跨敤 RechargeRequest 涓殑閲戦
			VaultId:        vaultID,
			FinalizeHeight: finalizeHeight,
			RequestId:      stored.RequestId,
			ScriptPubkey:   stored.NativeScript, // 鏂板锛氫繚瀛橀攣瀹氳剼鏈互澶囧悗缁彁鐜扮鍚嶄娇鐢?
		}
		utxoData, err := proto.Marshal(utxo)
		if err != nil {
			return err
		}
		setWithMeta(sv, utxoKey, utxoData, true, "frost_funds_utxo")

		// 鏇存柊 Vault 鐨勮祫閲戣处鏈仛鍚堢姸鎬侊紙鍙€夛紝鐢ㄤ簬鏌ヨ鎬婚锛?
		// TODO: 鏇存柊鎬讳綑棰?
	} else {
		// 璐︽埛閾?鍚堢害閾撅細鍐欏叆 lot FIFO
		seqKey := keys.KeyFrostFundsLotSeq(chain, asset, vaultID, finalizeHeight)
		seq := readUintSeq(sv, seqKey)

		indexKey := keys.KeyFrostFundsLotIndex(chain, asset, vaultID, finalizeHeight, seq)
		setWithMeta(sv, indexKey, []byte(stored.RequestId), true, "frost_funds")

		seq++
		setWithMeta(sv, seqKey, []byte(strconv.FormatUint(seq, 10)), true, "frost_funds")
	}

	// 澧炲姞鐢ㄦ埛浣欓锛堜娇鐢ㄥ垎绂诲瓨鍌級
	userAddr := stored.ReceiverAddress
	if userAddr != "" {
		userKey := keys.KeyAccount(userAddr)
		userData, userExists, _ := sv.Get(userKey)

		var userAccount pb.Account
		if userExists {
			if err := unmarshalProtoCompat(userData, &userAccount); err != nil {
				return fmt.Errorf("failed to unmarshal user account: %w", err)
			}
		} else {
			userAccount = pb.Account{
				Address: userAddr,
			}
		}

		// 浣跨敤鍒嗙瀛樺偍璇诲彇浣欓
		tokenBal := GetBalance(sv, userAddr, stored.TokenAddress)

		currentBalance, err := parseBalanceStrict("balance", tokenBal.Balance)
		if err != nil {
			return fmt.Errorf("invalid receiver balance state: %w", err)
		}

		amount, err := parsePositiveBalanceStrict("recharge amount", stored.Amount)
		if err != nil {
			return fmt.Errorf("invalid recharge amount: %w", err)
		}

		newBalance, err := SafeAdd(currentBalance, amount)
		if err != nil {
			return fmt.Errorf("balance overflow: %w", err)
		}

		tokenBal.Balance = newBalance.String()
		SetBalance(sv, userAddr, stored.TokenAddress, tokenBal)

		// 淇濆瓨璐︽埛锛堜笉鍚綑棰濓級
		updatedUserData, err := proto.Marshal(&userAccount)
		if err != nil {
			return fmt.Errorf("failed to marshal updated user account: %w", err)
		}
		setWithMeta(sv, userKey, updatedUserData, true, "account")

		// 淇濆瓨浣欓鏁版嵁
		balKey := keys.KeyBalance(userAddr, stored.TokenAddress)
		balData, _, _ := sv.Get(balKey)
		setWithMeta(sv, balKey, balData, true, "balance")
	}

	return nil
}

func applyRechargeRejected(sv StateView, req *pb.RechargeRequest) error {
	if req == nil || req.RequestId == "" {
		return nil
	}

	requestKey := keys.KeyRechargeRequest(req.RequestId)
	requestData, exists, err := sv.Get(requestKey)
	if err != nil || !exists {
		return nil
	}

	var stored pb.RechargeRequest
	if err := unmarshalProtoCompat(requestData, &stored); err != nil {
		return err
	}

	if stored.Status != pb.RechargeRequestStatus_RECHARGE_REJECTED {
		stored.Status = pb.RechargeRequestStatus_RECHARGE_REJECTED
		updatedRequestData, err := proto.Marshal(&stored)
		if err != nil {
			return err
		}
		setWithMeta(sv, requestKey, updatedRequestData, true, "witness_request")
	}

	removePendingFunds(sv, stored.RequestId)
	return nil
}

func readUintSeq(sv StateView, key string) uint64 {
	data, exists, err := sv.Get(key)
	if err != nil || !exists || len(data) == 0 {
		return 0
	}
	if n, err := strconv.ParseUint(string(data), 10, 64); err == nil {
		return n
	}
	return 0
}

func removePendingFunds(sv StateView, requestID string) {
	if requestID == "" {
		return
	}
	refKey := keys.KeyFrostFundsPendingLotRef(requestID)
	pendingKeyData, exists, err := sv.Get(refKey)
	if err != nil || !exists || len(pendingKeyData) == 0 {
		return
	}
	pendingKey := string(pendingKeyData)
	sv.Del(pendingKey)
	sv.Del(refKey)
}

func setWithMeta(sv StateView, key string, value []byte, syncStateDB bool, category string) {
	if setter, ok := sv.(metaSetter); ok {
		setter.SetWithMeta(key, value, syncStateDB, category)
		return
	}
	sv.Set(key, value)
}

func persistRechargeRequest(sv StateView, req *pb.RechargeRequest) error {
	if req == nil || req.RequestId == "" {
		return nil
	}
	requestKey := keys.KeyRechargeRequest(req.RequestId)
	data, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	setWithMeta(sv, requestKey, data, true, "witness_request")
	return nil
}
