// vm/witness_events.go
package vm

import (
	"dex/keys"
	"dex/pb"
	"dex/witness"
	"fmt"
	"math/big"
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
				// 这些事件意味着 RechargeRequest 的状态或属性已经改变，需要持久化
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
	if err := proto.Unmarshal(requestData, &stored); err != nil {
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
	vaultID := stored.VaultId // 使用入账时分配的 vault_id，保证资金按 Vault 分片
	finalizeHeight := stored.FinalizeHeight

	// 检查 Vault lifecycle：DRAINING 的 Vault 不再接受新入账
	vaultStateKey := keys.KeyFrostVaultState(chain, vaultID)
	vaultStateData, exists, _ := sv.Get(vaultStateKey)
	if exists && len(vaultStateData) > 0 {
		var vaultState pb.FrostVaultState
		if err := proto.Unmarshal(vaultStateData, &vaultState); err == nil {
			if vaultState.Status == VaultLifecycleDraining {
				// DRAINING 状态的 Vault 不再接受新入账
				// 注意：这里应该拒绝，但由于是 finalized 事件，可能已经发生，记录警告
				// 实际应该在 witness_handler.go 中拒绝
				return fmt.Errorf("vault %d is DRAINING, cannot accept new recharge", vaultID)
			}
		}
	}

	// 区分 BTC 和账户链/合约链的处理
	if chain == "btc" {
		// BTC：写入 UTXO
		txid := stored.NativeTxHash
		vout := stored.NativeVout

		// 降级处理：针对旧格式 "txid:vout"
		if vout == 0 && strings.Contains(txid, ":") {
			if idx := strings.Index(txid, ":"); idx != -1 {
				if v, err := strconv.ParseUint(txid[idx+1:], 10, 32); err == nil {
					vout = uint32(v)
					txid = txid[:idx]
				}
			}
		}

		// 写入 UTXO 详情
		utxoKey := keys.KeyFrostBtcUtxo(vaultID, txid, vout)
		utxo := &pb.FrostUtxo{
			Txid:           txid,
			Vout:           vout,
			Amount:         stored.Amount, // 使用 RechargeRequest 中的金额
			VaultId:        vaultID,
			FinalizeHeight: finalizeHeight,
			RequestId:      stored.RequestId,
			ScriptPubkey:   stored.NativeScript, // 新增：保存锁定脚本以备后续提现签名使用
		}
		utxoData, err := proto.Marshal(utxo)
		if err != nil {
			return err
		}
		setWithMeta(sv, utxoKey, utxoData, true, "frost_funds_utxo")

		// 更新 Vault 的资金账本聚合状态（可选，用于查询总额）
		// TODO: 更新总余额
	} else {
		// 账户链/合约链：写入 lot FIFO
		seqKey := keys.KeyFrostFundsLotSeq(chain, asset, vaultID, finalizeHeight)
		seq := readUintSeq(sv, seqKey)

		indexKey := keys.KeyFrostFundsLotIndex(chain, asset, vaultID, finalizeHeight, seq)
		setWithMeta(sv, indexKey, []byte(stored.RequestId), true, "frost_funds")

		seq++
		setWithMeta(sv, seqKey, []byte(strconv.FormatUint(seq, 10)), true, "frost_funds")
	}

	// 增加用户余额（使用分离存储）
	userAddr := stored.ReceiverAddress
	if userAddr != "" {
		userKey := keys.KeyAccount(userAddr)
		userData, userExists, _ := sv.Get(userKey)

		var userAccount pb.Account
		if userExists {
			if err := proto.Unmarshal(userData, &userAccount); err != nil {
				return fmt.Errorf("failed to unmarshal user account: %w", err)
			}
		} else {
			userAccount = pb.Account{
				Address: userAddr,
			}
		}

		// 使用分离存储读取余额
		tokenBal := GetBalance(sv, userAddr, stored.TokenAddress)

		currentBalance, _ := new(big.Int).SetString(tokenBal.Balance, 10)
		if currentBalance == nil {
			currentBalance = big.NewInt(0)
		}

		amount, ok := new(big.Int).SetString(stored.Amount, 10)
		if !ok {
			return fmt.Errorf("invalid recharge amount: %s", stored.Amount)
		}

		newBalance, err := SafeAdd(currentBalance, amount)
		if err != nil {
			return fmt.Errorf("balance overflow: %w", err)
		}

		tokenBal.Balance = newBalance.String()
		SetBalance(sv, userAddr, stored.TokenAddress, tokenBal)

		// 保存账户（不含余额）
		updatedUserData, err := proto.Marshal(&userAccount)
		if err != nil {
			return fmt.Errorf("failed to marshal updated user account: %w", err)
		}
		setWithMeta(sv, userKey, updatedUserData, true, "account")

		// 保存余额数据
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
	if err := proto.Unmarshal(requestData, &stored); err != nil {
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
