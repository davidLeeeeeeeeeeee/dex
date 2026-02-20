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
	sv.Set(requestKey, updatedRequestData)

	removePendingFunds(sv, stored.RequestId)

	chain := stored.NativeChain
	asset := stored.TokenAddress
	vaultID := stored.VaultId
	finalizeHeight := stored.FinalizeHeight

	creditedAmount, err := ParseBalance(stored.Amount)
	if err != nil {
		return fmt.Errorf("invalid recharge amount: %w", err)
	}

	vaultStateKey := keys.KeyFrostVaultState(chain, vaultID)
	vaultStateData, exists, _ := sv.Get(vaultStateKey)
	if exists && len(vaultStateData) > 0 {
		var vaultState pb.FrostVaultState
		if err := unmarshalProtoCompat(vaultStateData, &vaultState); err == nil {
			if vaultState.Status == VaultLifecycleDraining {
				return fmt.Errorf("vault %d is DRAINING, cannot accept new recharge", vaultID)
			}
		}
	}

	if chain == "btc" {
		txid := stored.NativeTxHash
		vout := stored.NativeVout

		if vout == 0 && strings.Contains(txid, ":") {
			if idx := strings.Index(txid, ":"); idx != -1 {
				if v, e := strconv.ParseUint(txid[idx+1:], 10, 32); e == nil {
					vout = uint32(v)
					txid = txid[:idx]
				}
			}
		}

		utxoKey := keys.KeyFrostBtcUtxo(vaultID, txid, vout)
		utxo := &pb.FrostUtxo{
			Txid:           txid,
			Vout:           vout,
			Amount:         stored.Amount,
			VaultId:        vaultID,
			FinalizeHeight: finalizeHeight,
			RequestId:      stored.RequestId,
			ScriptPubkey:   stored.NativeScript,
		}
		utxoData, err := proto.Marshal(utxo)
		if err != nil {
			return err
		}
		sv.Set(utxoKey, utxoData)

		utxoSeqKey := keys.KeyFrostBtcUtxoFIFOSeq(vaultID)
		utxoSeq := readUintSeq(sv, utxoSeqKey) + 1
		utxoIndexKey := keys.KeyFrostBtcUtxoFIFOIndex(vaultID, utxoSeq)
		sv.Set(utxoIndexKey, utxoData)
		sv.Set(utxoSeqKey, []byte(strconv.FormatUint(utxoSeq, 10)))

		utxoHeadKey := keys.KeyFrostBtcUtxoFIFOHead(vaultID)
		if _, headExists, _ := sv.Get(utxoHeadKey); !headExists {
			sv.Set(utxoHeadKey, []byte("1"))
		}
	} else {
		seqKey := keys.KeyFrostFundsLotSeq(chain, asset, vaultID, finalizeHeight)
		seq := readUintSeq(sv, seqKey)

		indexKey := keys.KeyFrostFundsLotIndex(chain, asset, vaultID, finalizeHeight, seq)
		sv.Set(indexKey, []byte(stored.RequestId))

		seq++
		sv.Set(seqKey, []byte(strconv.FormatUint(seq, 10)))
	}

	if err := addVaultAvailableBalance(sv, chain, asset, vaultID, creditedAmount); err != nil {
		return fmt.Errorf("failed to add vault available balance chain=%s asset=%s vault=%d: %w", chain, asset, vaultID, err)
	}

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
			userAccount = pb.Account{Address: userAddr}
		}

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

		updatedUserData, err := proto.Marshal(&userAccount)
		if err != nil {
			return fmt.Errorf("failed to marshal updated user account: %w", err)
		}
		sv.Set(userKey, updatedUserData)

		balKey := keys.KeyBalance(userAddr, stored.TokenAddress)
		balData, _, _ := sv.Get(balKey)
		sv.Set(balKey, balData)
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
		sv.Set(requestKey, updatedRequestData)
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

func persistRechargeRequest(sv StateView, req *pb.RechargeRequest) error {
	if req == nil || req.RequestId == "" {
		return nil
	}
	requestKey := keys.KeyRechargeRequest(req.RequestId)
	data, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	sv.Set(requestKey, data)
	return nil
}
