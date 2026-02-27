// vm/witness_handler.go
// Witness transaction handlers.
package vm

import (
	"crypto/sha256"
	"dex/frost/chain"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"dex/witness"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
)

const DefaultVaultCount = 100

// DefaultVaultCount 濠殿喗甯楃粙鎺椻€﹂崼銉晣閻犲洦鐣幒鏇犵杸闁哄啠鍋撻柦鍌氼儔濮婃椽骞嗚閳锋棃鏁?Vault 闂備浇妗ㄥ鎺楀础閹惰棄闂?// allocateVaultID 缂備胶铏庨崣搴ㄥ窗閺嶎厽鍋╁Δ锝呭暙缁犳垹鎲稿澶婂瀭閹兼番鍔嶉悡鈧?vault_id
// 濠电偠鎻紞鈧繛澶嬫礋瀵?H(request_id) % vault_count 缂備胶铏庨崣搴ㄥ窗閺囩姵宕叉慨妯垮煐閸庡海绱掔€ｎ偄顕滈柨?request_id 闂備浇顕栭崜娑氬垝椤栨锝夊箹娴ｅ摜顦梺绯曞墲缁嬫垹鏁幎鑺ョ厱闁圭儤鎸搁ˉ瀣箾閺夋埈妯€闁?vault
func allocateVaultID(requestID string, vaultCount uint32) uint32 {
	hash := sha256.Sum256([]byte(requestID))
	// 濠电偠鎻紞鈧繛澶嬫礋瀵偊濡舵径濠勵槶?4 闂佽瀛╃粙鎺椼€冩径瀣╃箚妞ゆ挾鍠愭刊瀛樼節婵炴儳浜鹃柨?uint32
	n := binary.BigEndian.Uint32(hash[:4])
	return n % vaultCount
}

// allocateVaultIDWithLifecycleCheck 缂備胶铏庨崣搴ㄥ窗閺嶎厽鍋╁Δ锝呭暙缁犳垹鎲稿澶婂瀭閹兼番鍔嶉悡鈧?vault_id闂備焦瀵х粙鎴︽嚐椤栫偞鍤愰柣鏃囧仱濞戙垹鐒垫い鎺戝閽?Vault lifecycle
// 濠电姷顣介埀顒€鍟块埀顒€缍婇幃妯诲緞閹邦剛顦梺绯曞墲缁嬫垹鏁幎鑺ョ厽?Vault 濠电姰鍨煎▔娑氣偓姘间簽閼?DRAINING 闂備胶绮…鍫ュ春閺嶎厼鐒垫い鎴ｆ硶缁涘繒绱掓潏銊х畼濞存粎顭堥埞鎴炵節閸曨儷锔界箾閹寸偞灏い鎴濆缁晝鈧稒顭囬埢鏃€銇勮箛鎾愁仼閻犱焦鐓￠弻锝夛綖椤掆偓婵″潡鏁?ACTIVE Vault
func allocateVaultIDWithLifecycleCheck(sv StateView, chain, requestID string, vaultCount uint32) (uint32, error) {
	if vaultCount == 0 {
		vaultCount = DefaultVaultCount
	}

	initialVaultID := allocateVaultID(requestID, vaultCount)
	// 婵犵妲呴崑鈧柛瀣崌閺岋紕浠︾拠鎻掑闂佹悶鍔岄悘姘舵晸?Vault 闁?lifecycle
	vaultStateKey := keys.KeyFrostVaultState(chain, initialVaultID)
	vaultStateData, exists, _ := sv.Get(vaultStateKey)
	if exists && len(vaultStateData) > 0 {
		var vaultState pb.FrostVaultState
		if err := unmarshalProtoCompat(vaultStateData, &vaultState); err == nil {
			// 婵犵妲呴崑鈧柛瀣崌閺?lifecycle闂備焦瀵х粙鎴︽偋閸涱垰鍨?VaultTransitionState 闂備礁鍚嬮崕鎶藉床閼艰翰浜归柛銉墯閺咁剟鎮橀悙鏉戝姢闁诲繑顨呴湁?VaultState.Status 闂備浇顫夋禍浠嬪礉瀹ュ钃熼柛銉墯閺?
			// 濠电姷顣介埀顒€鍟块埀顒€缍婇幃?Vault 濠电姰鍨煎▔娑氣偓姘间簽閼?DRAINING 闂備胶绮…鍫ュ春閺嶎厼鐒垫い鎴ｆ硶缁涘繒绱掓潏銊х畼濞存粎顭堥埞鎴炵節閸曨儷锔界箾閹寸偞灏い鎴濆缁晝鈧稒顭囬埢?ACTIVE Vault
			if vaultState.Status == VaultLifecycleDraining {
				// 婵犵鈧啿鈧綊鏁?Vault 婵犮垼娉涚€氼亪鏁?DRAINING 闂佺粯顭堥崺鏍焵椤戣法绛忕紒杈ㄧ箘娴滅鈹戞繝鍕︽繛鎴炴尭椤戝嫮绮╃€涙鈻?ACTIVE Vault
				for offset := uint32(1); offset < vaultCount; offset++ {
					candidateID := (initialVaultID + offset) % vaultCount
					candidateKey := keys.KeyFrostVaultState(chain, candidateID)
					candidateData, candidateExists, _ := sv.Get(candidateKey)
					if candidateExists && len(candidateData) > 0 {
						var candidateState pb.FrostVaultState
						if err := unmarshalProtoCompat(candidateData, &candidateState); err == nil {
							if candidateState.Status == "ACTIVE" {
								return candidateID, nil
							}
						}
					} else {
						// 濠电姷顣介埀顒€鍟块埀顒€缍婇幃?Vault 濠电偞鍨堕幐鍝ョ矓閹绢喗鍋ら柕濞炬櫅閹硅埖銇勯鐔风缂佲偓婢跺娼℃繛鎴炵懐閻掔偓銇勯幘鐟板惞闁瑰嘲顑夋俊鍫曞礋椤旂虎妲归柨?ACTIVE闂備焦瀵х粙鎴︽偋婵犲洤钃熷┑鐘叉搐缁€鍡樼箾閹寸儐鐒界紒鎲嬬畵閺?Vault闁?
						return candidateID, nil
					}
				}
				// 濠电姷顣介埀顒€鍟块埀顒€缍婇幃妯诲緞閹邦剛顓奸梺閫炲苯澧撮柨?Vault 闂傚倷绶￠崰娑欐叏閵堝憘?DRAINING闂備焦瀵х粙鎴︽儗娴ｇ儤宕查柟杈剧畱閻愬﹪鏌ｉ幇顔煎妺闁哄應鏅犻幃?
				return 0, fmt.Errorf("no ACTIVE vault available for chain %s", chain)
			}
		}
	}

	// 闂備礁鎲＄敮妤冩崲閸岀儑缍?Vault 闁?ACTIVE 闂備胶鎳撻悺銊╂偋閺冨倻绠斿璺哄閸嬫挸鈽夊▍顓т簻閿曘垽顢旈崼鐔告珫闂佸壊鍋侀崕宕囨暜濞戙垺鍋?ACTIVE闁?
	return initialVaultID, nil
}

// WitnessServiceAware 闂佽崵鍠愰悷銈吤归崶顒佸剨濠㈣埖鍔栭崵鈧梺鍛婁緱閸犳绮诲☉銏＄厱闁哄倸鎼晶鏌ユ煕閺傛寧鍤囬柟顖氱焸婵＄兘鏁冮埀顒佸緞瀹ュ鐓?
// 闂佽楠稿﹢閬嶅磻閻旇偐宓侀柛銉戝本妗ㄩ梺闈涚墕濡寰勫澶嬬厱婵炲棙鐟ч幃濂告晸?handler 闂備礁鎲￠悷顖炲垂椤栨稓顩查柟鐑橆殔缁犳娊鏌曟径娑㈡闁?WitnessService 闂備焦鐪归崝宀€鈧凹鍓涘Σ鎰板煛閸涱喖浠?
func appendUniqueNonEmpty(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func resolveVaultChainAndCount(sv StateView, chainName string) (string, uint32, error) {
	raw := strings.TrimSpace(chainName)
	candidates := make([]string, 0, 3)
	candidates = appendUniqueNonEmpty(candidates, chain.NormalizeChain(raw))
	candidates = appendUniqueNonEmpty(candidates, raw)
	candidates = appendUniqueNonEmpty(candidates, strings.ToUpper(raw))

	for _, candidate := range candidates {
		cfgKey := keys.KeyFrostVaultConfig(candidate, 0)
		cfgData, exists, err := sv.Get(cfgKey)
		if err != nil {
			return "", 0, fmt.Errorf("failed to read vault config for chain %s: %w", candidate, err)
		}
		if !exists || len(cfgData) == 0 {
			continue
		}

		var cfg pb.FrostVaultConfig
		if err := unmarshalProtoCompat(cfgData, &cfg); err != nil {
			return "", 0, fmt.Errorf("failed to parse vault config for chain %s: %w", candidate, err)
		}
		if cfg.VaultCount == 0 {
			return "", 0, fmt.Errorf("invalid vault count=0 for chain %s", candidate)
		}
		return candidate, cfg.VaultCount, nil
	}

	return "", 0, fmt.Errorf("vault config not found for chain %s", raw)
}

func isAllocatableVaultStatus(status string) bool {
	return status == VaultStatusKeyReady || status == VaultStatusActive
}

func allocateVaultIDWithStateCheck(sv StateView, chainName, requestID string, vaultCount uint32) (uint32, error) {
	if vaultCount == 0 {
		return 0, fmt.Errorf("invalid vault count=0 for chain %s", chainName)
	}

	initialVaultID := allocateVaultID(requestID, vaultCount)
	for offset := uint32(0); offset < vaultCount; offset++ {
		candidateID := (initialVaultID + offset) % vaultCount
		candidateKey := keys.KeyFrostVaultState(chainName, candidateID)
		candidateData, candidateExists, err := sv.Get(candidateKey)
		if err != nil {
			return 0, fmt.Errorf("failed to read vault state for chain %s vault %d: %w", chainName, candidateID, err)
		}
		if !candidateExists || len(candidateData) == 0 {
			continue
		}

		var candidateState pb.FrostVaultState
		if err := unmarshalProtoCompat(candidateData, &candidateState); err != nil {
			return 0, fmt.Errorf("failed to parse vault state for chain %s vault %d: %w", chainName, candidateID, err)
		}
		if isAllocatableVaultStatus(candidateState.Status) {
			return candidateID, nil
		}
	}

	return 0, fmt.Errorf("no allocatable vault (KEY_READY/ACTIVE) for chain %s", chainName)
}

type WitnessServiceAware interface {
	SetWitnessService(svc *witness.Service)
}

// ==================== WitnessStakeTxHandler ====================

// ==================== WitnessStakeTxHandler ====================
type WitnessStakeTxHandler struct {
	witnessSvc *witness.Service
}

// SetWitnessService 闂佽崵濮崇粈浣规櫠娴犲鍋柛鈩冪懄閸犲棗霉閿濆洦娅曟俊鎻掔墦閺屻倝寮堕幐搴℃濠电姭鍋撻柛銉墮缁€?
func (h *WitnessStakeTxHandler) SetWitnessService(svc *witness.Service) {
	h.witnessSvc = svc
}

func (h *WitnessStakeTxHandler) Kind() string {
	return "witness_stake"
}

func (h *WitnessStakeTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	stakeTx, ok := tx.GetContent().(*pb.AnyTx_WitnessStakeTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a witness stake transaction"}, fmt.Errorf("not a witness stake transaction")
	}

	stake := stakeTx.WitnessStakeTx
	if stake == nil || stake.Base == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid witness stake transaction"}, fmt.Errorf("invalid witness stake transaction")
	}

	ws := make([]WriteOp, 0)
	address := stake.Base.FromAddress

	accountKey := keys.KeyAccount(address)
	accountData, accountExists, err := sv.Get(accountKey)
	if err != nil {
		return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "failed to read account"}, err
	}

	var account pb.Account
	if accountExists {
		if err := unmarshalProtoCompat(accountData, &account); err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "failed to parse account"}, err
		}
	} else {
		return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "account not found"}, fmt.Errorf("account not found")
	}

	// 闂佽崵濮村ú鈺咁敋瑜戦妵鎰板炊閳哄倸鏋傛繛杈剧悼閺咁偄危閸儲鐓犻柡澶嬪閸ｇ菐閸パ嶈含闁?
	witnessKey := keys.KeyWitnessInfo(address)
	witnessData, witnessExists, _ := sv.Get(witnessKey)

	var witnessInfo pb.WitnessInfo
	if witnessExists {
		if err := unmarshalProtoCompat(witnessData, &witnessInfo); err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "failed to parse witness info"}, err
		}
	} else {
		witnessInfo = pb.WitnessInfo{Address: address, StakeAmount: "0", Status: pb.WitnessStatus_WITNESS_CANDIDATE}
	}

	amount, err := parsePositiveBalanceStrict("witness stake amount", stake.Amount)
	if err != nil {
		return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "invalid amount"}, fmt.Errorf("invalid amount")
	}

	fbBalance := GetBalance(sv, address, "FB")
	if stake.Op == pb.OrderOp_ADD {
		if h.witnessSvc != nil {
			if _, err := h.witnessSvc.ProcessStake(address, balanceToDecimal(amount)); err != nil {
				if err == witness.ErrWitnessAlreadyActive {
					logs.Warn("[WitnessStake] witness %s already active, treating as success (idempotent)", address)
				} else {
					return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: err.Error()}, err
				}
			}
		}

		balance, err := parseBalanceStrict("balance", fbBalance.Balance)
		if err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "invalid balance state"}, err
		}
		if balance.Cmp(amount) < 0 {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "insufficient balance"}, fmt.Errorf("insufficient balance")
		}
		newBalance, err := SafeSub(balance, amount)
		if err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "balance underflow"}, fmt.Errorf("balance underflow: %w", err)
		}
		lockedBalance, err := parseBalanceStrict("witness locked balance", fbBalance.WitnessLockedBalance)
		if err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "invalid locked balance state"}, err
		}
		// 濠电偠鎻紞鈧繛澶嬫礋瀵偊濡堕崶锝呬壕闁荤喖鍋婂Σ鎼佹煕濞嗗繒绠荤€规洘绻堥幃鈺佺暦閸ャ儮鍋撴繝鍐х箚妞ゆ劗鍠庢禍楣冩⒑閸濆嫯顫﹂柛搴ｆ暬瀵劑鏁愰崶锝呬壕閻熸瑥瀚悡顖滅磽瀹ュ棗鐏﹂柨娑欏姇閳规垿宕惰閸斿鏁?
		newLocked, err := SafeAdd(lockedBalance, amount)
		if err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "locked balance overflow"}, fmt.Errorf("locked balance overflow: %w", err)
		}
		fbBalance.Balance = newBalance.String()
		fbBalance.WitnessLockedBalance = newLocked.String()

		currentStake, err := parseBalanceStrict("witness stake amount", witnessInfo.StakeAmount)
		if err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "invalid witness stake state"}, err
		}
		// 濠电偠鎻紞鈧繛澶嬫礋瀵偊濡堕崶锝呬壕闁荤喖鍋婂Σ鎼佹煕濞嗗繒绠荤€规洘绻堥幃鈺佺暦閸ャ儮鍋撴繝鍐х箚妞ゆ劗鍠庢禍楣冩⒑閸濆嫯顫﹂柛搴㈡綑椤斿繐鈹戠€ｎ亞顔呮繝闈涘€婚…鍫濃枍閵娿儙娑㈡偋閸垻鐣洪柣搴㈢▓閺呯娀鏁?
		newStake, err := SafeAdd(currentStake, amount)
		if err != nil {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "stake amount overflow"}, fmt.Errorf("stake amount overflow: %w", err)
		}
		witnessInfo.StakeAmount = newStake.String()
		witnessInfo.Status = pb.WitnessStatus_WITNESS_ACTIVE
	} else {
		if h.witnessSvc != nil {
			if _, err := h.witnessSvc.ProcessUnstake(address); err != nil {
				return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: err.Error()}, err
			}
		}

		if witnessInfo.Status != pb.WitnessStatus_WITNESS_ACTIVE {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "witness is not active"}, fmt.Errorf("witness is not active")
		}
		if len(witnessInfo.PendingTasks) > 0 {
			return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "witness has pending tasks"}, fmt.Errorf("witness has pending tasks")
		}
		witnessInfo.Status = pb.WitnessStatus_WITNESS_UNSTAKING
		witnessInfo.UnstakeHeight = stake.Base.ExecutedHeight
	}

	SetBalance(sv, address, "FB", fbBalance)
	balanceKey := keys.KeyBalance(address, "FB")
	balanceData, _, _ := sv.Get(balanceKey)
	ws = append(ws, WriteOp{Key: balanceKey, Value: balanceData, Category: "balance"})

	updatedAccountData, err := proto.Marshal(&account)
	if err != nil {
		return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "failed to marshal account"}, err
	}
	ws = append(ws, WriteOp{Key: accountKey, Value: updatedAccountData, Del: false, Category: "account"})

	witnessInfoData, err := proto.Marshal(&witnessInfo)
	if err != nil {
		return nil, &Receipt{TxID: stake.Base.TxId, Status: "FAILED", Error: "failed to marshal witness info"}, err
	}
	ws = append(ws, WriteOp{Key: witnessKey, Value: witnessInfoData, Del: false, Category: "witness"})

	historyKey := keys.KeyWitnessHistory(stake.Base.TxId)
	historyData, _ := proto.Marshal(stake)
	ws = append(ws, WriteOp{Key: historyKey, Value: historyData, Del: false, Category: "history"})

	// 闂備礁鎲￠懝楣冨嫉椤掑嫷鏁嗛柣鎰惈缁€?WitnessService 闂備礁鎲￠崝鏇㈠箠鎼淬劍鍋ら柕濞炬櫆閸嬫劙鏌ら崫銉毌闁?
	if h.witnessSvc != nil {
		h.witnessSvc.LoadWitness(&witnessInfo)
	}

	return ws, &Receipt{TxID: stake.Base.TxId, Status: "SUCCEED", WriteCount: len(ws)}, nil
}

func (h *WitnessStakeTxHandler) Apply(tx *pb.AnyTx) error {
	return nil
}

// ==================== WitnessRequestTxHandler ====================

// WitnessRequestTxHandler 闂備胶顭堢换鍫ュ磿鐎涙ê绶為柛鏇ㄥ幗閸犲棗霉閿濆洦娅曟俊鎻掔墦閹綊宕堕浣规殸闂佹椿浜炴晶妤€顕ラ崟顖氱妞ゆ挾鍠庨埀顒傚仱閺?
type WitnessRequestTxHandler struct {
	witnessSvc *witness.Service
	VaultCount uint32
}

// SetWitnessService 闂佽崵濮崇粈浣规櫠娴犲鍋柛鈩冪懄閸犲棗霉閿濆洦娅曟俊鎻掔墦閺屻倝寮堕幐搴℃濠电姭鍋撻柛銉墮缁€?
func (h *WitnessRequestTxHandler) SetWitnessService(svc *witness.Service) {
	h.witnessSvc = svc
}

func (h *WitnessRequestTxHandler) Kind() string {
	return "witness_request"
}

func (h *WitnessRequestTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	requestTx, ok := tx.GetContent().(*pb.AnyTx_WitnessRequestTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a witness request transaction"}, fmt.Errorf("not a witness request transaction")
	}

	request := requestTx.WitnessRequestTx
	if request == nil || request.Base == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid witness request transaction"}, fmt.Errorf("invalid witness request transaction")
	}

	ws := make([]WriteOp, 0)
	requestID := request.Base.TxId

	existingKey := keys.KeyRechargeRequest(requestID)
	requestData, exists, _ := sv.Get(existingKey)
	if exists {
		// Duplicate request is treated as failed receipt without VM error.
		return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: "request already exists"}, nil
	}

	chainName, vaultCount, err := resolveVaultChainAndCount(sv, request.NativeChain)
	if err != nil {
		return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: err.Error()}, err
	}

	nativeTxKey := keys.KeyRechargeRequestByNativeTx(chainName, request.NativeTxHash)
	_, nativeExists, _ := sv.Get(nativeTxKey)
	if nativeExists {
		return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: "native tx already used"}, nil
	}

	tokenKey := keys.KeyToken(request.TokenAddress)
	_, tokenExists, _ := sv.Get(tokenKey)
	if !tokenExists {
		return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: "token not found"}, fmt.Errorf("token not found")
	}

	var rechargeRequest *pb.RechargeRequest

	// 濠电偠鎻紞鈧繛澶嬫礋瀵?WitnessService 闂備礁鎲＄敮妤冪矙閹寸姷纾介柟鎹愬煐鐎氭岸姊洪崹顕呭剳婵犫偓閹绢喗鐓ユ繛鎴烆焽婢ф洟鎮楅崹顐€跨€规洏鍎甸、鏇㈠Ψ椤旂晫绉抽梺鑽ゅТ濞诧絽霉閸ヮ剙鐒垫い鎺嗗亾闁硅櫕锕㈤崺鈧い鎺嗗亾妞わ附澹嗛埀顒佸搸閸旀垿鏁?
	if h.witnessSvc != nil {
		var err error
		rechargeRequest, err = h.witnessSvc.CreateRechargeRequest(request)
		if err != nil {
			return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: err.Error()}, err
		}
	} else {
		// 闂傚倸鍊哥€氼剛绮旈懜娈挎闁搞儺鐏涢悢鐓庣伋鐎规洖娲ㄩ、鍛存⒑閹稿海鈯曢柣顓у枤缁厽寰勭€ｎ偄顎撻柣鐘充航閸斿秹鏁?WitnessService
		rechargeRequest = &pb.RechargeRequest{
			RequestId:        requestID,
			NativeChain:      request.NativeChain,
			NativeTxHash:     request.NativeTxHash,
			NativeVout:       request.NativeVout,
			NativeScript:     request.NativeScript,
			TokenAddress:     request.TokenAddress,
			Amount:           request.Amount,
			ReceiverAddress:  request.ReceiverAddress,
			RequesterAddress: request.Base.FromAddress,
			Status:           pb.RechargeRequestStatus_RECHARGE_PENDING,
			CreateHeight:     request.Base.ExecutedHeight,
			RechargeFee:      request.RechargeFee,
		}
	}

	// Deterministically assign an allocatable vault for this request.
	vaultID, err := allocateVaultIDWithStateCheck(sv, chainName, requestID, vaultCount)
	if err != nil {
		return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: err.Error()}, err
	}
	rechargeRequest.NativeChain = chainName
	rechargeRequest.VaultId = vaultID

	requestData, err = proto.Marshal(rechargeRequest)
	if err != nil {
		return nil, &Receipt{TxID: requestID, Status: "FAILED", Error: "failed to marshal request"}, err
	}
	ws = append(ws, WriteOp{Key: existingKey, Value: requestData, Del: false, Category: "witness_request"})
	ws = append(ws, WriteOp{Key: nativeTxKey, Value: []byte(requestID), Del: false, Category: "index"})

	pendingHeight := rechargeRequest.CreateHeight
	if pendingHeight == 0 {
		pendingHeight = request.Base.ExecutedHeight
	}

	pendingSeqKey := keys.KeyFrostFundsPendingLotSeq(chainName, request.TokenAddress, vaultID, pendingHeight)
	pendingSeq := readUintSeq(sv, pendingSeqKey)
	pendingIndexKey := keys.KeyFrostFundsPendingLotIndex(chainName, request.TokenAddress, vaultID, pendingHeight, pendingSeq)
	pendingRefKey := keys.KeyFrostFundsPendingLotRef(requestID)

	ws = append(ws, WriteOp{Key: pendingIndexKey, Value: []byte(requestID), Del: false, Category: "frost_funds_pending"})
	ws = append(ws, WriteOp{Key: pendingSeqKey, Value: []byte(strconv.FormatUint(pendingSeq+1, 10)), Del: false, Category: "frost_funds_pending"})
	ws = append(ws, WriteOp{Key: pendingRefKey, Value: []byte(pendingIndexKey), Del: false, Category: "frost_funds_pending"})

	return ws, &Receipt{TxID: requestID, Status: "SUCCEED", WriteCount: len(ws)}, nil
}

func (h *WitnessRequestTxHandler) Apply(tx *pb.AnyTx) error {
	return nil
}

// ==================== WitnessVoteTxHandler ====================

// WitnessVoteTxHandler 闂佽崵鍠愰悷銈吤归崶顒佸剨濠㈣埖鍔曠粻顕€鏌￠崶鈺佇為柛瀣Т椤法鎹勯崫鍕典紑闂佽鍠栭敃顏堟晸?
type WitnessVoteTxHandler struct {
	witnessSvc *witness.Service
}

// SetWitnessService 闂佽崵濮崇粈浣规櫠娴犲鍋柛鈩冪懄閸犲棗霉閿濆洦娅曟俊鎻掔墦閺屻倝寮堕幐搴℃濠电姭鍋撻柛銉墮缁€?
func (h *WitnessVoteTxHandler) SetWitnessService(svc *witness.Service) {
	h.witnessSvc = svc
}

func (h *WitnessVoteTxHandler) Kind() string {
	return "witness_vote"
}

func (h *WitnessVoteTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	voteTx, ok := tx.GetContent().(*pb.AnyTx_WitnessVoteTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a witness vote transaction"}, fmt.Errorf("not a witness vote transaction")
	}

	vote := voteTx.WitnessVoteTx
	if vote == nil || vote.Base == nil || vote.Vote == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid witness vote transaction"}, fmt.Errorf("invalid witness vote transaction")
	}

	ws := make([]WriteOp, 0)
	requestID := vote.Vote.RequestId
	witnessAddr := vote.Vote.WitnessAddress

	requestKey := keys.KeyRechargeRequest(requestID)
	requestData, requestExists, err := sv.Get(requestKey)
	if err != nil || !requestExists {
		// 濠电姷顣介埀顒€鍟块埀顒€缍婇幃妯诲緞鐏炴儳鐝伴梻浣哥仢椤戝洤锕㈤幘顔界厵闂傚牊绋戠紞鏍磼濡も偓椤﹂潧鐣峰杈ㄦ殰妞ゆ柨澧介ˇ顕€鏌℃径鍡樻珕闁哄被鍔岀叅?FAILED 闂備礁鎲￠崺鍫ュ储閽樺鏆ゅ〒姘ｅ亾闁轰礁绉舵禒锕傛嚃閳哄叐鎴炵箾閹寸偞灏紒澶愵棑閹广垽骞嬮敃鈧悙?error
		// 闂佸搫顦弲婊堟偡閿曞倹鍋嬮梺顒€绉撮惌妤併亜閺嶃劎鎳佺紒銊ゅ嵆濮婃椽顢楅埀顒勬倶濠靛鍌ㄩ柕鍫濐槸閺嬩線鎮楅棃娑欐喐闁瑰啿顦湁婵犙冪仢閳ь剚鐗曞嵄婵°倕鍟犻崑鎾存媴閸愵煈妫堥梺鎼炰紘閸パ咁槺闂佸憡绻傜€氼參宕戦妷銉庢棃鎮╅懠顒傚姰闂佷紮绠戦ˇ鏉款嚗閸曨剚缍囨い鎰╁剾閺囥垺鐓ユ繛鎴灻〃娆戠磼椤旀儳鈻堥柟铏尭閻ｆ繈宕橀崘鈺佹诞鐎规洘锕㈠畷姗€骞撻幒鎴犵▏闂佽崵濮村ú銈呂涘Δ浣侯浄闁绘ê妯婇悡銉╂煟閹邦喖鍔嬮柨娑欑墪铻?
		return nil, &Receipt{TxID: vote.Base.TxId, Status: "FAILED", Error: "request not found"}, nil
	}

	var request pb.RechargeRequest
	if err := unmarshalProtoCompat(requestData, &request); err != nil {
		return nil, &Receipt{TxID: vote.Base.TxId, Status: "FAILED", Error: "failed to parse request"}, err
	}

	voteKey := keys.KeyWitnessVote(requestID, witnessAddr)
	_, voteExists, _ := sv.Get(voteKey)
	if voteExists {
		return nil, &Receipt{TxID: vote.Base.TxId, Status: "FAILED", Error: "duplicate vote"}, nil
	}

	// 4. 闂佽崵濮撮鍛村疮娴兼潙鏋?WitnessService 闂備礁鎼ú銈夋偤閵娾晛钃熷┑鐘叉处閸嬫劙鏌ら崫銉毌闁稿鎸荤粭鐔哥節閸曨収妲遍梻浣告啞閸旀洟骞婃惔銊﹀仱闁靛ň鏅滈弲?
	var finalRequest *pb.RechargeRequest
	if h.witnessSvc != nil {
		vote.Vote.TxId = vote.Base.TxId
		updatedRequest, err := h.witnessSvc.ProcessVote(vote.Vote)
		if err != nil {
			// 濠电偞鍨堕幐濠氭嚌閻愵剚鍙忛柣鏂垮悑閻掑鏌￠崟顐ょ閻㈩垰妫濆娲箰鎼达絾鍣銈嗘磸閸婃繈寮澶婇唶婵犲﹤鎳嶇槐锝嗙節绾版ǚ鍋撻崘娴嬪亾缂佹ɑ鍙忛柟闂寸缁犳垵霉閿濆妫戦柣鐔哥箞閹鎮介崹顐户缂備浇椴哥喊宥囩矙婢跺鍚嬮柛娑卞灠閻ｃ劑鏌ｉ悩鍙夊偍闁稿氦浜懞杈ㄣ偅閸愩劎顔婃繝銏ｆ硾椤戝棗鈻撶拠娴嬫闁瑰灝鍟╃花濠氭倶韫囷絽鐏﹂柨?
			return nil, &Receipt{TxID: vote.Base.TxId, Status: "FAILED", Error: err.Error()}, nil
		}
		finalRequest = updatedRequest
	} else {

		request.Votes = append(request.Votes, vote.Vote)
		switch vote.Vote.VoteType {
		case pb.WitnessVoteType_VOTE_PASS:
			request.PassCount++
		case pb.WitnessVoteType_VOTE_FAIL:
			request.FailCount++
		case pb.WitnessVoteType_VOTE_ABSTAIN:
			request.AbstainCount++
		}
		finalRequest = &request
	}

	voteData, err := proto.Marshal(vote.Vote)
	if err != nil {
		return nil, &Receipt{TxID: vote.Base.TxId, Status: "FAILED", Error: "failed to marshal vote"}, err
	}
	ws = append(ws, WriteOp{Key: voteKey, Value: voteData, Del: false, Category: "witness_vote"})

	updatedRequestData, err := proto.Marshal(finalRequest)
	if err != nil {
		return nil, &Receipt{TxID: vote.Base.TxId, Status: "FAILED", Error: "failed to marshal request"}, err
	}
	ws = append(ws, WriteOp{Key: requestKey, Value: updatedRequestData, Del: false, Category: "witness_request"})

	return ws, &Receipt{TxID: vote.Base.TxId, Status: "SUCCEED", WriteCount: len(ws)}, nil
}

func (h *WitnessVoteTxHandler) Apply(tx *pb.AnyTx) error {
	return nil
}

// ==================== WitnessChallengeTxHandler ====================

// WitnessChallengeTxHandler 闂備胶鎳撻幉锟犲垂閸洖鍨傛い鎺嶈兌椤╂煡鏌曢崼婵囶棡妞ゎ偅绮岄…璺ㄦ崉閸濆嫷浼€闂佽鍠栭敃顏堟晸?
type WitnessChallengeTxHandler struct {
	witnessSvc *witness.Service
}

// SetWitnessService 闂佽崵濮崇粈浣规櫠娴犲鍋柛鈩冪懄閸犲棗霉閿濆洦娅曟俊鎻掔墦閺屻倝寮堕幐搴℃濠电姭鍋撻柛銉墮缁€?
func (h *WitnessChallengeTxHandler) SetWitnessService(svc *witness.Service) {
	h.witnessSvc = svc
}

func (h *WitnessChallengeTxHandler) Kind() string {
	return "witness_challenge"
}

func (h *WitnessChallengeTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	challengeTx, ok := tx.GetContent().(*pb.AnyTx_WitnessChallengeTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a witness challenge transaction"}, fmt.Errorf("not a witness challenge transaction")
	}

	challenge := challengeTx.WitnessChallengeTx
	if challenge == nil || challenge.Base == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid witness challenge transaction"}, fmt.Errorf("invalid witness challenge transaction")
	}

	ws := make([]WriteOp, 0)
	challengeID := challenge.Base.TxId
	requestID := challenge.RequestId

	requestKey := keys.KeyRechargeRequest(requestID)
	requestData, requestExists, err := sv.Get(requestKey)
	if err != nil || !requestExists {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "request not found"}, nil
	}

	var request pb.RechargeRequest
	if err := unmarshalProtoCompat(requestData, &request); err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "failed to parse request"}, err
	}

	if request.Status != pb.RechargeRequestStatus_RECHARGE_CHALLENGE_PERIOD {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "request is not in challenge period"}, nil
	}

	if request.ChallengeId != "" {
		existingChallengeKey := keys.KeyChallengeRecord(request.ChallengeId)
		_, exists, _ := sv.Get(existingChallengeKey)
		if exists {
			return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "challenge already exists"}, fmt.Errorf("challenge already exists")
		}
	}

	challengerAddr := challenge.Base.FromAddress
	accountKey := keys.KeyAccount(challengerAddr)
	accountData, accountExists, _ := sv.Get(accountKey)
	if !accountExists {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "challenger account not found"}, fmt.Errorf("challenger account not found")
	}

	var account pb.Account
	if err := unmarshalProtoCompat(accountData, &account); err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "failed to parse account"}, err
	}

	stakeAmount, err := parsePositiveBalanceStrict("challenge stake amount", challenge.StakeAmount)
	if err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "invalid stake amount"}, fmt.Errorf("invalid stake amount")
	}

	fbBalance := GetBalance(sv, challengerAddr, "FB")
	balance, err := parseBalanceStrict("balance", fbBalance.Balance)
	if err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "invalid balance state"}, err
	}
	if balance.Cmp(stakeAmount) < 0 {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "insufficient balance"}, fmt.Errorf("insufficient balance")
	}

	newBalance, err := SafeSub(balance, stakeAmount)
	if err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "balance underflow"}, fmt.Errorf("balance underflow: %w", err)
	}
	fbBalance.Balance = newBalance.String()
	SetBalance(sv, challengerAddr, "FB", fbBalance)

	balanceKey := keys.KeyBalance(challengerAddr, "FB")
	balanceData, _, _ := sv.Get(balanceKey)
	ws = append(ws, WriteOp{Key: balanceKey, Value: balanceData, Category: "balance"})

	updatedAccountData, err := proto.Marshal(&account)
	if err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "failed to marshal account"}, err
	}
	ws = append(ws, WriteOp{Key: accountKey, Value: updatedAccountData, Del: false, Category: "account"})

	challengeRecord := &pb.ChallengeRecord{
		ChallengeId:       challengeID,
		RequestId:         requestID,
		ChallengerAddress: challengerAddr,
		StakeAmount:       challenge.StakeAmount,
		Reason:            challenge.Reason,
		CreateHeight:      challenge.Base.ExecutedHeight,
		Finalized:         false,
	}

	challengeData, err := proto.Marshal(challengeRecord)
	if err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "failed to marshal challenge"}, err
	}

	challengeKey := keys.KeyChallengeRecord(challengeID)
	ws = append(ws, WriteOp{Key: challengeKey, Value: challengeData, Del: false, Category: "challenge"})

	request.Status = pb.RechargeRequestStatus_RECHARGE_CHALLENGED
	request.ChallengeId = challengeID

	updatedRequestData, err := proto.Marshal(&request)
	if err != nil {
		return nil, &Receipt{TxID: challengeID, Status: "FAILED", Error: "failed to marshal request"}, err
	}
	ws = append(ws, WriteOp{Key: requestKey, Value: updatedRequestData, Del: false, Category: "witness_request"})

	return ws, &Receipt{TxID: challengeID, Status: "SUCCEED", WriteCount: len(ws)}, nil
}

func (h *WitnessChallengeTxHandler) Apply(tx *pb.AnyTx) error {
	return nil
}

// ==================== ArbitrationVoteTxHandler ====================

// ArbitrationVoteTxHandler handles arbitration vote transactions.
type ArbitrationVoteTxHandler struct {
	witnessSvc *witness.Service
}

// SetWitnessService sets witness service.
func (h *ArbitrationVoteTxHandler) SetWitnessService(svc *witness.Service) {
	h.witnessSvc = svc
}

func (h *ArbitrationVoteTxHandler) Kind() string {
	return "arbitration_vote"
}

func (h *ArbitrationVoteTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	arbVoteTx, ok := tx.GetContent().(*pb.AnyTx_ArbitrationVoteTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not an arbitration vote transaction"}, fmt.Errorf("not an arbitration vote transaction")
	}

	arbVote := arbVoteTx.ArbitrationVoteTx
	if arbVote == nil || arbVote.Base == nil || arbVote.Vote == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid arbitration vote transaction"}, fmt.Errorf("invalid arbitration vote transaction")
	}

	ws := make([]WriteOp, 0)
	challengeID := arbVote.ChallengeId
	arbitratorAddr := arbVote.Vote.WitnessAddress

	challengeKey := keys.KeyChallengeRecord(challengeID)
	challengeData, challengeExists, err := sv.Get(challengeKey)
	if err != nil || !challengeExists {
		return nil, &Receipt{TxID: arbVote.Base.TxId, Status: "FAILED", Error: "challenge not found"}, nil
	}

	var challenge pb.ChallengeRecord
	if err := unmarshalProtoCompat(challengeData, &challenge); err != nil {
		return nil, &Receipt{TxID: arbVote.Base.TxId, Status: "FAILED", Error: "failed to parse challenge"}, err
	}

	arbVoteKey := keys.KeyArbitrationVote(challengeID, arbitratorAddr)
	_, voteExists, _ := sv.Get(arbVoteKey)
	if voteExists {
		return nil, &Receipt{TxID: arbVote.Base.TxId, Status: "FAILED", Error: "duplicate vote"}, fmt.Errorf("duplicate vote")
	}

	voteData, err := proto.Marshal(arbVote.Vote)
	if err != nil {
		return nil, &Receipt{TxID: arbVote.Base.TxId, Status: "FAILED", Error: "failed to marshal vote"}, err
	}
	ws = append(ws, WriteOp{Key: arbVoteKey, Value: voteData, Del: false, Category: "arbitration_vote"})

	challenge.ArbitrationVotes = append(challenge.ArbitrationVotes, arbVote.Vote)
	switch arbVote.Vote.VoteType {
	case pb.WitnessVoteType_VOTE_PASS:
		challenge.PassCount++
	case pb.WitnessVoteType_VOTE_FAIL:
		challenge.FailCount++
	}

	updatedChallengeData, err := proto.Marshal(&challenge)
	if err != nil {
		return nil, &Receipt{TxID: arbVote.Base.TxId, Status: "FAILED", Error: "failed to marshal challenge"}, err
	}
	ws = append(ws, WriteOp{Key: challengeKey, Value: updatedChallengeData, Del: false, Category: "challenge"})

	return ws, &Receipt{TxID: arbVote.Base.TxId, Status: "SUCCEED", WriteCount: len(ws)}, nil
}

func (h *ArbitrationVoteTxHandler) Apply(tx *pb.AnyTx) error {
	return nil
}

// ==================== WitnessClaimRewardTxHandler ====================

// WitnessClaimRewardTxHandler handles witness reward claim transactions.
type WitnessClaimRewardTxHandler struct {
	witnessSvc *witness.Service
}

// SetWitnessService sets witness service.
func (h *WitnessClaimRewardTxHandler) SetWitnessService(svc *witness.Service) {
	h.witnessSvc = svc
}

func (h *WitnessClaimRewardTxHandler) Kind() string {
	return "witness_claim_reward"
}

func (h *WitnessClaimRewardTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	claimTx, ok := tx.GetContent().(*pb.AnyTx_WitnessClaimRewardTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a witness claim reward transaction"}, fmt.Errorf("not a witness claim reward transaction")
	}

	claim := claimTx.WitnessClaimRewardTx
	if claim == nil || claim.Base == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid witness claim reward transaction"}, fmt.Errorf("invalid witness claim reward transaction")
	}

	ws := make([]WriteOp, 0)
	witnessAddr := claim.Base.FromAddress

	witnessKey := keys.KeyWitnessInfo(witnessAddr)
	witnessData, witnessExists, err := sv.Get(witnessKey)
	if err != nil || !witnessExists {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "witness not found"}, fmt.Errorf("witness not found")
	}

	var witnessInfo pb.WitnessInfo
	if err := unmarshalProtoCompat(witnessData, &witnessInfo); err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "failed to parse witness info"}, err
	}

	pendingReward, err := parseBalanceStrict("pending reward", witnessInfo.PendingReward)
	if err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "invalid pending reward state"}, err
	}
	if pendingReward.Sign() <= 0 {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "no pending reward"}, fmt.Errorf("no pending reward")
	}

	accountKey := keys.KeyAccount(witnessAddr)
	accountData, accountExists, _ := sv.Get(accountKey)

	var account pb.Account
	if accountExists {
		if err := unmarshalProtoCompat(accountData, &account); err != nil {
			return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "failed to parse account"}, err
		}
	} else {
		account = pb.Account{Address: witnessAddr}
	}

	fbBalance := GetBalance(sv, witnessAddr, "FB")

	currentBalance, err := parseBalanceStrict("balance", fbBalance.Balance)
	if err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "invalid balance state"}, err
	}
	newBalance, err := SafeAdd(currentBalance, pendingReward)
	if err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "balance overflow"}, fmt.Errorf("balance overflow: %w", err)
	}
	fbBalance.Balance = newBalance.String()
	SetBalance(sv, witnessAddr, "FB", fbBalance)

	totalReward, err := parseBalanceStrict("total reward", witnessInfo.TotalReward)
	if err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "invalid total reward state"}, err
	}
	newTotalReward, err := SafeAdd(totalReward, pendingReward)
	if err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "total reward overflow"}, fmt.Errorf("total reward overflow: %w", err)
	}
	witnessInfo.TotalReward = newTotalReward.String()
	witnessInfo.PendingReward = "0"

	balanceKey := keys.KeyBalance(witnessAddr, "FB")
	balanceData, _, _ := sv.Get(balanceKey)
	ws = append(ws, WriteOp{Key: balanceKey, Value: balanceData, Category: "balance"})

	updatedAccountData, err := proto.Marshal(&account)
	if err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "failed to marshal account"}, err
	}
	ws = append(ws, WriteOp{Key: accountKey, Value: updatedAccountData, Del: false, Category: "account"})

	updatedWitnessData, err := proto.Marshal(&witnessInfo)
	if err != nil {
		return nil, &Receipt{TxID: claim.Base.TxId, Status: "FAILED", Error: "failed to marshal witness info"}, err
	}
	ws = append(ws, WriteOp{Key: witnessKey, Value: updatedWitnessData, Del: false, Category: "witness"})

	return ws, &Receipt{TxID: claim.Base.TxId, Status: "SUCCEED", WriteCount: len(ws)}, nil
}

func (h *WitnessClaimRewardTxHandler) Apply(tx *pb.AnyTx) error {
	return nil
}
