// frost/runtime/planning/btc_utxo_selector.go
// BTC UTXO 选择：FIFO 索引扫描、费率估算、地址推导

package planning

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	chainpkg "dex/frost/chain"
	"dex/keys"
	"dex/pb"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"google.golang.org/protobuf/proto"
)

type btcUTXOScanStats struct {
	Scanned      uint64
	IndexMissing uint64
	DecodeFailed uint64
	Locked       uint64
	ZeroAmount   uint64
	Selected     uint64
}

type btcUTXOSelectionError struct {
	Code   string
	Reason string

	Chain    string
	VaultID  uint32
	Need     uint64
	Got      uint64
	Head     uint64
	MaxSeq   uint64
	ScanStat btcUTXOScanStats
}

func (e *btcUTXOSelectionError) Error() string {
	return fmt.Sprintf(
		"UTXO selection failed: need=%d got=%d code=%s chain=%s vault=%d head=%d max_seq=%d scanned=%d selected=%d locked=%d missing=%d decode_failed=%d zero_amount=%d reason=%s",
		e.Need, e.Got, e.Code, e.Chain, e.VaultID, e.Head, e.MaxSeq,
		e.ScanStat.Scanned, e.ScanStat.Selected, e.ScanStat.Locked, e.ScanStat.IndexMissing, e.ScanStat.DecodeFailed, e.ScanStat.ZeroAmount,
		e.Reason,
	)
}

func newBTCUTXOSelectionError(
	code, reason, chain string,
	vaultID uint32,
	need, got, head, maxSeq uint64,
	scanStat btcUTXOScanStats,
) error {
	return &btcUTXOSelectionError{
		Code:     code,
		Reason:   reason,
		Chain:    chain,
		VaultID:  vaultID,
		Need:     need,
		Got:      got,
		Head:     head,
		MaxSeq:   maxSeq,
		ScanStat: scanStat,
	}
}

func classifyBTCUTXOInsufficient(got uint64, stat btcUTXOScanStats) (string, string) {
	if got == 0 {
		switch {
		case stat.Scanned == 0:
			return "fifo_window_empty", "no FIFO window scanned"
		case stat.IndexMissing == stat.Scanned:
			return "fifo_index_missing_all", "all FIFO index entries are missing"
		case stat.Locked == stat.Scanned:
			return "all_locked", "all candidate UTXOs are locked"
		case stat.DecodeFailed == stat.Scanned:
			return "decode_failed_all", "all candidate UTXOs failed to decode"
		case stat.ZeroAmount == stat.Scanned:
			return "zero_amount_all", "all candidate UTXOs have zero amount"
		default:
			return "no_spendable_utxo", "no spendable UTXO found in FIFO window"
		}
	}
	if stat.Selected == 0 {
		return "no_spendable_utxo", "no spendable UTXO found in FIFO window"
	}
	return "selected_but_insufficient_amount", "spendable UTXOs exist but total amount is below required amount"
}

// selectBTCUTXOs 选择 BTC UTXO（按 FIFO 索引顺序，不扫描 UTXO 全前缀）
func (p *JobWindowPlanner) selectBTCUTXOs(chain string, vaultID uint32, needAmount uint64) ([]chainpkg.UTXO, error) {
	// 1. 读取 VaultState，构建 Taproot scriptPubKey
	vaultStateKey := keys.KeyFrostVaultState(chain, vaultID)
	vaultStateData, exists, err := p.stateReader.Get(vaultStateKey)
	if err != nil {
		return nil, newBTCUTXOSelectionError("vault_state_read_failed", err.Error(), chain, vaultID, needAmount, 0, 0, 0, btcUTXOScanStats{})
	}
	if !exists || len(vaultStateData) == 0 {
		return nil, newBTCUTXOSelectionError("vault_state_missing", fmt.Sprintf("vault state not found: key=%s", vaultStateKey), chain, vaultID, needAmount, 0, 0, 0, btcUTXOScanStats{})
	}

	var vaultState pb.FrostVaultState
	if err := proto.Unmarshal(vaultStateData, &vaultState); err != nil {
		return nil, newBTCUTXOSelectionError("vault_state_decode_failed", err.Error(), chain, vaultID, needAmount, 0, 0, 0, btcUTXOScanStats{})
	}

	xOnlyPubKey, err := normalizeTaprootXOnlyPubKey(vaultState.GroupPubkey)
	if err != nil {
		return nil, newBTCUTXOSelectionError("vault_pubkey_invalid", err.Error(), chain, vaultID, needAmount, 0, 0, 0, btcUTXOScanStats{})
	}
	expectedScriptPubKey := make([]byte, 34)
	expectedScriptPubKey[0] = 0x51
	expectedScriptPubKey[1] = 0x20
	copy(expectedScriptPubKey[2:], xOnlyPubKey)

	// 2. 按 head/seq 从 FIFO 索引顺序读取
	headKey := keys.KeyFrostBtcUtxoFIFOHead(vaultID)
	seqKey := keys.KeyFrostBtcUtxoFIFOSeq(vaultID)

	head := uint64(1)
	if headData, headExists, e := p.stateReader.Get(headKey); e != nil {
		return nil, newBTCUTXOSelectionError("fifo_head_read_failed", e.Error(), chain, vaultID, needAmount, 0, 0, 0, btcUTXOScanStats{})
	} else if headExists && len(headData) > 0 {
		parsed, pe := strconv.ParseUint(string(headData), 10, 64)
		if pe != nil {
			return nil, newBTCUTXOSelectionError("fifo_head_invalid", fmt.Sprintf("invalid head value=%q: %v", string(headData), pe), chain, vaultID, needAmount, 0, 0, 0, btcUTXOScanStats{})
		}
		if parsed == 0 {
			return nil, newBTCUTXOSelectionError("fifo_head_invalid", fmt.Sprintf("invalid head value=%q: must be >= 1", string(headData)), chain, vaultID, needAmount, 0, 0, 0, btcUTXOScanStats{})
		}
		head = parsed
	}

	seqData, exists, err := p.stateReader.Get(seqKey)
	if err != nil {
		return nil, newBTCUTXOSelectionError("fifo_seq_read_failed", err.Error(), chain, vaultID, needAmount, 0, head, 0, btcUTXOScanStats{})
	}
	if !exists || len(seqData) == 0 {
		return nil, newBTCUTXOSelectionError("fifo_seq_missing", fmt.Sprintf("FIFO seq key missing: key=%s", seqKey), chain, vaultID, needAmount, 0, head, 0, btcUTXOScanStats{})
	}
	maxSeq, err := strconv.ParseUint(string(seqData), 10, 64)
	if err != nil {
		return nil, newBTCUTXOSelectionError("fifo_seq_invalid", fmt.Sprintf("invalid seq value=%q: %v", string(seqData), err), chain, vaultID, needAmount, 0, head, 0, btcUTXOScanStats{})
	}
	if maxSeq == 0 {
		return nil, newBTCUTXOSelectionError("fifo_seq_zero", fmt.Sprintf("invalid seq value=%q: must be >= 1", string(seqData)), chain, vaultID, needAmount, 0, head, maxSeq, btcUTXOScanStats{})
	}
	if head > maxSeq {
		return nil, newBTCUTXOSelectionError("fifo_head_ahead", "FIFO head is ahead of max seq", chain, vaultID, needAmount, 0, head, maxSeq, btcUTXOScanStats{})
	}

	selected := make([]chainpkg.UTXO, 0)
	var total uint64
	scanStat := btcUTXOScanStats{}
	for seq := head; seq <= maxSeq && total < needAmount; seq++ {
		scanStat.Scanned++
		indexKey := keys.KeyFrostBtcUtxoFIFOIndex(vaultID, seq)
		utxoData, idxExists, e := p.stateReader.Get(indexKey)
		if e != nil {
			return nil, newBTCUTXOSelectionError("fifo_index_read_failed", fmt.Sprintf("failed to read FIFO index key=%s: %v", indexKey, e), chain, vaultID, needAmount, total, head, maxSeq, scanStat)
		}
		if !idxExists || len(utxoData) == 0 {
			scanStat.IndexMissing++
			continue
		}

		var protoUtxo pb.FrostUtxo
		if e := proto.Unmarshal(utxoData, &protoUtxo); e != nil {
			scanStat.DecodeFailed++
			continue
		}
		if protoUtxo.VaultId != vaultID {
			scanStat.DecodeFailed++
			continue
		}
		if !bytes.Equal(protoUtxo.ScriptPubkey, expectedScriptPubKey) {
			scanStat.DecodeFailed++
			continue
		}

		lockKey := keys.KeyFrostBtcLockedUtxo(vaultID, protoUtxo.Txid, protoUtxo.Vout)
		if lockVal, locked, e := p.stateReader.Get(lockKey); e != nil {
			return nil, newBTCUTXOSelectionError("utxo_lock_read_failed", fmt.Sprintf("failed to read lock key=%s: %v", lockKey, e), chain, vaultID, needAmount, total, head, maxSeq, scanStat)
		} else if locked && len(lockVal) > 0 {
			scanStat.Locked++
			continue
		}

		amount := parseAmount(protoUtxo.Amount)
		if amount == 0 {
			scanStat.ZeroAmount++
			continue
		}

		selected = append(selected, chainpkg.UTXO{
			TxID:          protoUtxo.Txid,
			Vout:          protoUtxo.Vout,
			Amount:        amount,
			ScriptPubKey:  append([]byte(nil), protoUtxo.ScriptPubkey...),
			ConfirmHeight: protoUtxo.FinalizeHeight,
		})
		scanStat.Selected++
		total += amount
	}

	if total < needAmount {
		code, reason := classifyBTCUTXOInsufficient(total, scanStat)
		return nil, newBTCUTXOSelectionError(code, reason, chain, vaultID, needAmount, total, head, maxSeq, scanStat)
	}

	return selected, nil
}

// estimateBTCFee 估算 BTC 手续费（简化：基于 input/output 数量）
func (p *JobWindowPlanner) estimateBTCFee(inputCount, outputCount int) uint64 {
	// 简化估算：每个 input 约 148 bytes，每个 output 约 34 bytes
	// 假设费率 10 sat/vbyte
	baseSize := 10 // 交易基础大小
	inputSize := inputCount * 148
	outputSize := outputCount * 34
	witnessSize := inputCount * 64 // Taproot witness
	totalVBytes := baseSize + inputSize + outputSize + witnessSize/4
	feeRate := uint64(10) // sat/vbyte
	return uint64(totalVBytes) * feeRate
}

func sumUTXOAmount(utxos []chainpkg.UTXO) uint64 {
	var total uint64
	for _, utxo := range utxos {
		total += utxo.Amount
	}
	return total
}

func (p *JobWindowPlanner) getYoungestVaultTreasuryAddress(chain string) (string, error) {
	youngestVaultID, err := p.selectYoungestActiveVaultID(chain)
	if err != nil {
		return "", err
	}

	vaultStateKey := keys.KeyFrostVaultState(chain, youngestVaultID)
	vaultStateData, exists, err := p.stateReader.Get(vaultStateKey)
	if err != nil {
		return "", err
	}
	if !exists || len(vaultStateData) == 0 {
		return "", fmt.Errorf("vault state not found for youngest vault: chain=%s vault=%d", chain, youngestVaultID)
	}

	var state pb.FrostVaultState
	if err := proto.Unmarshal(vaultStateData, &state); err != nil {
		return "", err
	}
	xOnly, err := normalizeTaprootXOnlyPubKey(state.GroupPubkey)
	if err != nil {
		return "", fmt.Errorf("invalid group pubkey for youngest vault: chain=%s vault=%d err=%w", chain, youngestVaultID, err)
	}
	addr, err := btcTaprootAddressFromXOnly(xOnly, btcNetParamsForChain(chain))
	if err != nil {
		return "", fmt.Errorf("derive taproot address for youngest vault failed: chain=%s vault=%d err=%w", chain, youngestVaultID, err)
	}
	return addr, nil
}

func normalizeTaprootXOnlyPubKey(groupPubkey []byte) ([]byte, error) {
	switch len(groupPubkey) {
	case 32:
		xOnly := make([]byte, 32)
		copy(xOnly, groupPubkey)
		return xOnly, nil
	case 33:
		if groupPubkey[0] != 0x02 && groupPubkey[0] != 0x03 {
			return nil, fmt.Errorf("invalid compressed pubkey prefix: 0x%x", groupPubkey[0])
		}
		xOnly := make([]byte, 32)
		copy(xOnly, groupPubkey[1:])
		return xOnly, nil
	default:
		return nil, fmt.Errorf("unsupported group pubkey length: %d", len(groupPubkey))
	}
}

func btcTaprootAddressFromXOnly(xOnly []byte, netParams *chaincfg.Params) (string, error) {
	addr, err := btcutil.NewAddressTaproot(xOnly, netParams)
	if err != nil {
		return "", err
	}
	return addr.EncodeAddress(), nil
}

func btcNetParamsForChain(chain string) *chaincfg.Params {
	lower := strings.ToLower(strings.TrimSpace(chain))
	switch lower {
	case "btc_testnet", "btc-testnet", "btctestnet", "testnet":
		return &chaincfg.TestNet3Params
	case "btc_regtest", "btc-regtest", "btcregtest", "regtest":
		return &chaincfg.RegressionNetParams
	case "btc_signet", "btc-signet", "btcsignet", "signet":
		return &chaincfg.SigNetParams
	default:
		return &chaincfg.MainNetParams
	}
}

func (p *JobWindowPlanner) selectYoungestActiveVaultID(chain string) (uint32, error) {
	vaultCfgKey := keys.KeyFrostVaultConfig(chain, 0)
	vaultCfgData, exists, err := p.stateReader.Get(vaultCfgKey)
	if err != nil {
		return 0, err
	}

	vaultCount := uint32(1)
	if exists && len(vaultCfgData) > 0 {
		var cfg pb.FrostVaultConfig
		if err := proto.Unmarshal(vaultCfgData, &cfg); err == nil && cfg.VaultCount > 0 {
			vaultCount = cfg.VaultCount
		}
	}

	found := false
	var youngestID uint32
	var bestEpoch uint64
	var bestActiveSince uint64

	for id := uint32(0); id < vaultCount; id++ {
		vaultStateKey := keys.KeyFrostVaultState(chain, id)
		vaultStateData, stateExists, e := p.stateReader.Get(vaultStateKey)
		if e != nil || !stateExists || len(vaultStateData) == 0 {
			continue
		}

		var state pb.FrostVaultState
		if e := proto.Unmarshal(vaultStateData, &state); e != nil {
			continue
		}
		if state.Status != "ACTIVE" {
			continue
		}

		if !found ||
			state.KeyEpoch > bestEpoch ||
			(state.KeyEpoch == bestEpoch && state.ActiveSinceHeight > bestActiveSince) ||
			(state.KeyEpoch == bestEpoch && state.ActiveSinceHeight == bestActiveSince && id > youngestID) {
			found = true
			youngestID = id
			bestEpoch = state.KeyEpoch
			bestActiveSince = state.ActiveSinceHeight
		}
	}

	if !found {
		return 0, fmt.Errorf("no ACTIVE vault found for chain=%s", chain)
	}
	return youngestID, nil
}

// calculateBTCBalance 计算 BTC Vault 的可用余额（未锁定的 UTXO）
func (p *JobWindowPlanner) calculateBTCBalance(vaultID uint32) (uint64, error) {
	utxoPrefix := fmt.Sprintf("v1_frost_btc_utxo_%d_", vaultID)
	var total uint64

	type utxoCandidate struct {
		value   []byte
		lockKey string
	}
	candidates := make([]utxoCandidate, 0)
	lockKeys := make([]string, 0)

	err := p.stateReader.Scan(utxoPrefix, func(k string, v []byte) bool {
		parts := strings.Split(k, "_")
		if len(parts) < 6 {
			return true
		}

		txid := parts[4]
		vout, err := strconv.ParseUint(parts[5], 10, 32)
		if err != nil {
			return true
		}

		lk := keys.KeyFrostBtcLockedUtxo(vaultID, txid, uint32(vout))
		lockKeys = append(lockKeys, lk)

		valueCopy := append([]byte(nil), v...)
		candidates = append(candidates, utxoCandidate{value: valueCopy, lockKey: lk})
		return true
	})

	if err != nil {
		return 0, err
	}

	lockedMap, err := getMany(p.stateReader, lockKeys)
	if err != nil {
		return 0, err
	}

	for _, c := range candidates {
		if _, locked := lockedMap[c.lockKey]; locked {
			continue // 已锁定，跳过
		}
		var protoUtxo pb.FrostUtxo
		if err := proto.Unmarshal(c.value, &protoUtxo); err == nil {
			total += parseAmount(protoUtxo.Amount)
		}
	}

	return total, nil
}
