package keys

import "strings"

// KeyCategory classifies where a key should be stored.
type KeyCategory int

const (
	CategoryKV KeyCategory = iota
	CategoryState
)

// CategorizeKey decides whether the key belongs to KV or StateDB.
//
// The key families that belong to StateDB are defined in
// keys/statedb_business_keys.go.
func CategorizeKey(key string) KeyCategory {
	for _, ex := range excludeFromState {
		if strings.HasPrefix(key, ex) {
			return CategoryKV
		}
	}
	for _, prefix := range statePrefixes {
		if strings.HasPrefix(key, prefix) {
			return CategoryState
		}
	}
	return CategoryKV
}

// IsStatefulKey reports whether key belongs to mutable StateDB data.
func IsStatefulKey(key string) bool {
	return CategorizeKey(key) == CategoryState
}

// IsFlowKey reports whether key belongs to KV-only flow/history/index data.
func IsFlowKey(key string) bool {
	return CategorizeKey(key) == CategoryKV
}

func IsAccountKey(key string) bool {
	return strings.HasPrefix(key, withVer("account_"))
}

func IsOrderStateKey(key string) bool {
	return strings.HasPrefix(key, withVer("orderstate_"))
}

func IsBlockKey(key string) bool {
	return strings.HasPrefix(key, withVer("blockdata_")) ||
		strings.HasPrefix(key, withVer("height_")) ||
		strings.HasPrefix(key, withVer("blockid_")) ||
		key == withVer("latest_block_height")
}

func IsTxKey(key string) bool {
	return strings.HasPrefix(key, withVer("txraw_")) ||
		strings.HasPrefix(key, withVer("anyTx_")) ||
		strings.HasPrefix(key, withVer("pending_anytx_")) ||
		strings.HasPrefix(key, withVer("tx_")) ||
		strings.HasPrefix(key, withVer("order_")) ||
		strings.HasPrefix(key, withVer("minerTx_"))
}

func IsIndexKey(key string) bool {
	return strings.HasPrefix(key, withVer("pair:")) ||
		strings.HasPrefix(key, withVer("stakeIndex_")) ||
		strings.HasPrefix(key, withVer("indexToAccount_")) ||
		strings.HasPrefix(key, withVer("trade_"))
}

func IsHistoryKey(key string) bool {
	return strings.HasPrefix(key, withVer("transfer_history_")) ||
		strings.HasPrefix(key, withVer("miner_history_")) ||
		strings.HasPrefix(key, withVer("recharge_history_")) ||
		strings.HasPrefix(key, withVer("freeze_history_")) ||
		strings.HasPrefix(key, withVer("whist_"))
}

func IsVMKey(key string) bool {
	return strings.HasPrefix(key, withVer("vm_"))
}

func IsFrostKey(key string) bool {
	return strings.HasPrefix(key, withVer("frost_"))
}

func IsWitnessKey(key string) bool {
	return strings.HasPrefix(key, withVer("winfo_")) ||
		strings.HasPrefix(key, withVer("wvote_")) ||
		strings.HasPrefix(key, withVer("wstake_")) ||
		strings.HasPrefix(key, withVer("witness_")) ||
		strings.HasPrefix(key, withVer("challenge_")) ||
		strings.HasPrefix(key, withVer("arbitration_vote_")) ||
		strings.HasPrefix(key, withVer("shelved_request_")) ||
		strings.HasPrefix(key, withVer("recharge_request_"))
}
