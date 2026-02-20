package vm

import (
	"dex/keys"
	"dex/pb"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
)

func isActiveFrostTransitionState(state *pb.VaultTransitionState) bool {
	if state == nil {
		return false
	}
	switch state.DkgStatus {
	case DKGStatusKeyReady, DKGStatusFailed:
		return false
	}
	if state.Lifecycle == VaultLifecycleRetired {
		return false
	}
	return true
}

func appendTransitionWriteOps(ops []WriteOp, transitionKey string, transition *pb.VaultTransitionState) ([]WriteOp, error) {
	if transition == nil {
		return ops, fmt.Errorf("transition is nil")
	}
	transitionData, err := proto.Marshal(transition)
	if err != nil {
		return ops, err
	}
	ops = append(ops, WriteOp{
		Key:      transitionKey,
		Value:    transitionData,
		Category: "frost_vault_transition",
	})

	activeKey := keys.KeyFrostVaultTransitionActive(transition.Chain, transition.VaultId, transition.EpochId)
	if isActiveFrostTransitionState(transition) {
		ops = append(ops, WriteOp{
			Key:      activeKey,
			Value:    []byte(transitionKey),
			Category: "frost_vault_transition_active",
		})
	} else {
		ops = append(ops, WriteOp{
			Key:      activeKey,
			Del:      true,
			Category: "frost_vault_transition_active",
		})
	}
	return ops, nil
}

func appendTransitionIndexOpsFromWriteSet(ops []WriteOp) []WriteOp {
	extra := make([]WriteOp, 0, 2)
	for _, op := range ops {
		if op.Del || op.Category != "frost_vault_transition" || len(op.Value) == 0 {
			continue
		}
		transition := &pb.VaultTransitionState{}
		if err := proto.Unmarshal(op.Value, transition); err != nil {
			continue
		}
		activeKey := keys.KeyFrostVaultTransitionActive(transition.Chain, transition.VaultId, transition.EpochId)
		if isActiveFrostTransitionState(transition) {
			extra = append(extra, WriteOp{
				Key:      activeKey,
				Value:    []byte(op.Key),
				Category: "frost_vault_transition_active",
			})
		} else {
			extra = append(extra, WriteOp{
				Key:      activeKey,
				Del:      true,
				Category: "frost_vault_transition_active",
			})
		}
	}
	if len(extra) == 0 {
		return ops
	}
	return append(ops, extra...)
}

func setTransitionStateWithIndex(sv StateView, transitionKey string, transition *pb.VaultTransitionState) error {
	if transition == nil {
		return fmt.Errorf("transition is nil")
	}
	transitionData, err := proto.Marshal(transition)
	if err != nil {
		return err
	}
	sv.Set(transitionKey, transitionData)

	activeKey := keys.KeyFrostVaultTransitionActive(transition.Chain, transition.VaultId, transition.EpochId)
	if isActiveFrostTransitionState(transition) {
		sv.Set(activeKey, []byte(transitionKey))
	} else {
		sv.Del(activeKey)
	}
	return nil
}

func getVaultAvailableBalance(sv StateView, chain, asset string, vaultID uint32) (*big.Int, error) {
	key := keys.KeyFrostVaultAvailableBalance(chain, asset, vaultID)
	data, exists, err := sv.Get(key)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return big.NewInt(0), nil
		}
		return nil, err
	}
	if !exists || len(data) == 0 {
		return big.NewInt(0), nil
	}
	return ParseBalance(string(data))
}

func setVaultAvailableBalance(sv StateView, chain, asset string, vaultID uint32, value *big.Int) {
	if value == nil || value.Sign() < 0 {
		value = big.NewInt(0)
	}
	key := keys.KeyFrostVaultAvailableBalance(chain, asset, vaultID)
	sv.Set(key, []byte(value.String()))
}

func addVaultAvailableBalance(sv StateView, chain, asset string, vaultID uint32, delta *big.Int) error {
	if delta == nil || delta.Sign() == 0 {
		return nil
	}
	current, err := getVaultAvailableBalance(sv, chain, asset, vaultID)
	if err != nil {
		return err
	}
	next, err := SafeAdd(current, delta)
	if err != nil {
		return err
	}
	setVaultAvailableBalance(sv, chain, asset, vaultID, next)
	return nil
}

func subVaultAvailableBalance(sv StateView, chain, asset string, vaultID uint32, delta *big.Int) error {
	if delta == nil || delta.Sign() == 0 {
		return nil
	}
	current, err := getVaultAvailableBalance(sv, chain, asset, vaultID)
	if err != nil {
		return err
	}
	next, err := SafeSub(current, delta)
	if err != nil {
		return err
	}
	setVaultAvailableBalance(sv, chain, asset, vaultID, next)
	return nil
}

func appendOrSetFrostWriteOp(sv StateView, ops *[]WriteOp, key, category string, value []byte, del bool) {
	if del {
		sv.Del(key)
		*ops = append(*ops, WriteOp{Key: key, Del: true, Category: category})
		return
	}
	sv.Set(key, value)
	*ops = append(*ops, WriteOp{Key: key, Value: value, Category: category})
}

func advanceBtcUtxoFIFOHead(sv StateView, vaultID uint32) {
	headKey := keys.KeyFrostBtcUtxoFIFOHead(vaultID)
	head := readUint64FromState(sv, headKey)
	if head == 0 {
		head = 1
	}
	maxSeq := readUint64FromState(sv, keys.KeyFrostBtcUtxoFIFOSeq(vaultID))
	if maxSeq == 0 || head > maxSeq {
		if head == 0 {
			head = 1
		}
		sv.Set(headKey, []byte(strconv.FormatUint(head, 10)))
		return
	}

	for head <= maxSeq {
		indexKey := keys.KeyFrostBtcUtxoFIFOIndex(vaultID, head)
		utxoData, exists, err := sv.Get(indexKey)
		if err != nil || !exists || len(utxoData) == 0 {
			head++
			continue
		}

		var utxo pb.FrostUtxo
		if err := proto.Unmarshal(utxoData, &utxo); err != nil {
			head++
			continue
		}
		lockKey := keys.KeyFrostBtcLockedUtxo(vaultID, utxo.Txid, utxo.Vout)
		lockVal, locked, _ := sv.Get(lockKey)
		if !locked || len(lockVal) == 0 {
			break
		}
		head++
	}
	sv.Set(headKey, []byte(strconv.FormatUint(head, 10)))
}
