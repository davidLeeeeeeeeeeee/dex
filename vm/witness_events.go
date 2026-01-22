// vm/witness_events.go
package vm

import (
	"dex/keys"
	"dex/pb"
	"dex/witness"
	"fmt"
	"strconv"

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
		// BTC：写入 UTXO（需要从 RechargeRequest 或 WitnessRequestTx 中获取 txid 和 vout）
		// 注意：WitnessRequestTx 应该包含 BTC 交易的 txid 和 vout 信息
		// 这里简化处理，实际应该从 request 中解析 BTC 交易信息
		// TODO: 从 RechargeRequest 或 WitnessRequestTx 中获取 BTC txid 和 vout
		// 示例：如果 request 中有 NativeTxHash，可以解析为 BTC txid
		// 对于 BTC，通常需要额外的字段来标识 UTXO（txid:vout）
		// 这里假设 WitnessRequestTx 或 RechargeRequest 中有这些信息
		// 实际实现需要根据具体的数据结构来解析
	} else {
		// 账户链/合约链：写入 lot FIFO
		seqKey := keys.KeyFrostFundsLotSeq(chain, asset, vaultID, finalizeHeight)
		seq := readUintSeq(sv, seqKey)

		indexKey := keys.KeyFrostFundsLotIndex(chain, asset, vaultID, finalizeHeight, seq)
		setWithMeta(sv, indexKey, []byte(stored.RequestId), true, "frost_funds")

		seq++
		setWithMeta(sv, seqKey, []byte(strconv.FormatUint(seq, 10)), true, "frost_funds")
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
