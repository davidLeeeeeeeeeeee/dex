package keys

// StateDBBusinessKeySpec describes one business key family that is persisted
// into StateDB.
type StateDBBusinessKeySpec struct {
	Prefix      string
	KeyBuilder  string
	Description string
}

// stateDBBusinessKeySpecs is the single source of truth for business key
// families that belong to StateDB.
//
// Keep this list in sync with key builders in keys/keys.go.
var stateDBBusinessKeySpecs = []StateDBBusinessKeySpec{
	{Prefix: withVer("account_"), KeyBuilder: "KeyAccount", Description: "Account state"},
	{Prefix: withVer("balance_"), KeyBuilder: "KeyBalance", Description: "Per-token account balances"},
	{Prefix: withVer("acc_order_"), KeyBuilder: "KeyAccountOrderItem", Description: "Account-order relation"},
	{Prefix: withVer("order_"), KeyBuilder: "KeyOrder", Description: "Order entity data (legacy/compat path)"},
	{Prefix: withVer("orderstate_"), KeyBuilder: "KeyOrderState", Description: "Mutable order execution state"},
	{Prefix: withVer("token_"), KeyBuilder: "KeyToken / KeyTokenRegistry", Description: "Token metadata and registry"},
	{Prefix: withVer("freeze_"), KeyBuilder: "KeyFreeze", Description: "Freeze mark state"},
	{Prefix: withVer("frost_"), KeyBuilder: "KeyFrost*", Description: "FROST module mutable state"},
	{Prefix: withVer("recharge_record_"), KeyBuilder: "KeyRechargeRecord", Description: "Recharge anti-replay record"},
	{Prefix: withVer("recharge_request_"), KeyBuilder: "KeyRechargeRequest", Description: "Witness recharge request state"},
	{Prefix: withVer("winfo_"), KeyBuilder: "KeyWitnessInfo", Description: "Witness profile/state"},
	{Prefix: withVer("wvote_"), KeyBuilder: "KeyWitnessVote", Description: "Witness votes"},
	{Prefix: withVer("wstake_"), KeyBuilder: "KeyWitnessStakeIndex", Description: "Witness stake index"},
	{Prefix: withVer("witness_config"), KeyBuilder: "KeyWitnessConfig", Description: "Witness module config"},
	{Prefix: withVer("witness_reward_pool"), KeyBuilder: "KeyWitnessRewardPool", Description: "Witness reward pool"},
	{Prefix: withVer("challenge_"), KeyBuilder: "KeyChallengeRecord", Description: "Challenge records"},
	{Prefix: withVer("shelved_request_"), KeyBuilder: "KeyShelvedRequest", Description: "Shelved recharge requests"},
	{Prefix: withVer("arbitration_vote_"), KeyBuilder: "KeyArbitrationVote", Description: "Arbitration votes"},
}

// stateDBExcludePrefixes are key families that intentionally remain in KV.
var stateDBExcludePrefixes = []string{
	withVer("freeze_history_"),
}

// StateDBBusinessKeySpecs returns a copy of current StateDB business key specs.
// Use this as the canonical list of business keys persisted into StateDB.
func StateDBBusinessKeySpecs() []StateDBBusinessKeySpec {
	out := make([]StateDBBusinessKeySpec, len(stateDBBusinessKeySpecs))
	copy(out, stateDBBusinessKeySpecs)
	return out
}

// Package-private slices consumed by CategorizeKey in category.go.
var statePrefixes = func() []string {
	out := make([]string, 0, len(stateDBBusinessKeySpecs))
	for _, spec := range stateDBBusinessKeySpecs {
		out = append(out, spec.Prefix)
	}
	return out
}()

var excludeFromState = func() []string {
	out := make([]string, len(stateDBExcludePrefixes))
	copy(out, stateDBExcludePrefixes)
	return out
}()
