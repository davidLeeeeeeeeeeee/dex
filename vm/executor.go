package vm

import (
	iface "dex/interfaces"
	"dex/keys"
	"dex/logs"
	"dex/matching"
	"dex/pb"
	"dex/utils"
	"dex/witness"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
)

// Executor VM閹笛嗩攽???
type Executor struct {
	mu             sync.RWMutex
	DB             iface.DBManager
	Reg            *HandlerRegistry
	Cache          SpecExecCache
	KFn            KindFn
	ReadFn         ReadThroughFn
	ScanFn         ScanFn
	WitnessService *witness.Service // 鐟欎浇鐦夐懓鍛箛閸斺槄绱欑痪顖氬敶鐎涙顓哥粻妤伳侀崸妤嬬礆
}

func NewExecutor(db iface.DBManager, reg *HandlerRegistry, cache SpecExecCache) *Executor {
	return NewExecutorWithWitness(db, reg, cache, nil)
}

// NewExecutorWithWitnessService 娴ｈ法鏁ゅ鍙夋箒閻ㄥ嫯顫嗙拠浣解偓鍛箛閸斺€冲灡瀵ゅ搫鐡欓幍褑顢戦崳?
func NewExecutorWithWitnessService(db iface.DBManager, reg *HandlerRegistry, cache SpecExecCache, witnessSvc *witness.Service) *Executor {
	if reg == nil {
		reg = NewHandlerRegistry()
	}
	if cache == nil {
		cache = NewSpecExecLRU(64)
	}

	executor := &Executor{
		DB:             db,
		Reg:            reg,
		Cache:          cache,
		KFn:            DefaultKindFn,
		WitnessService: witnessSvc,
	}

	// 鐠佸墽鐤哛eadFn
	executor.ReadFn = func(key string) ([]byte, error) {
		return db.Get(key)
	}

	// 鐠佸墽鐤哠canFn
	executor.ScanFn = func(prefix string) (map[string][]byte, error) {
		return db.Scan(prefix)
	}

	return executor
}

// NewExecutorWithWitness 閸掓稑缂撶敮锕侇潌鐠囦浇鈧懏婀囬崝锛勬畱閹笛嗩攽???
func NewExecutorWithWitness(db iface.DBManager, reg *HandlerRegistry, cache SpecExecCache, witnessConfig *witness.Config) *Executor {
	// 閸掓稑缂撻獮璺烘儙閸斻劏顫嗙拠浣解偓鍛箛???
	witnessSvc := witness.NewService(witnessConfig)
	_ = witnessSvc.Start()

	executor := NewExecutorWithWitnessService(db, reg, cache, witnessSvc)

	// 閸掓繂顫愰崠?Witness 閻樿埖???
	if err := executor.LoadWitnessState(); err != nil {
		fmt.Printf("[VM] Warning: failed to load witness state: %v\n", err)
	}

	return executor
}

// LoadWitnessState 娴犲孩鏆熼幑顔肩氨閸旂姾娴囬幍鈧張澶嬫た鐠哄啰娈戠憴浣界槈閼板懌鈧礁鍙嗙拹锕侇嚞濮瑰倸鎷伴幐鎴炲灛鐠佹澘缍嶉崚?WitnessService 閸愬懎鐡ㄦ稉?
func (x *Executor) LoadWitnessState() error {
	if x.WitnessService == nil {
		return nil
	}

	// 1. 閸旂姾娴囩憴浣界槈閼板懍淇婇幁顖ょ礄閹碘偓閺堝妞跨捄鍐潌鐠囦浇鈧懘鍏橀棁鈧憰浣告躬閸愬懎鐡ㄦ稉顓濅簰娓氳儻绻樼悰宀勬閺堟椽鈧瀚ㄩ敍?
	winfoMap, err := x.DB.Scan(keys.KeyWitnessInfoPrefix())
	if err == nil {
		count := 0
		for _, data := range winfoMap {
			var info pb.WitnessInfo
			if err := unmarshalProtoCompat(data, &info); err == nil {
				x.WitnessService.LoadWitness(&info)
				count++
			}
		}
		if count > 0 {
			fmt.Printf("[VM] Loaded %d witnesses into memory\n", count)
		}
	}

	// 2. 閸旂姾娴囬棃鐐电矒閹胶娈戦崗銉ㄥ鐠囬攱???
	requestMap, err := x.DB.Scan(keys.KeyRechargeRequestPrefix())
	if err == nil {
		count := 0
		for _, data := range requestMap {
			var req pb.RechargeRequest
			if err := unmarshalProtoCompat(data, &req); err == nil {
				// 閸欘亝婀侀棃鐐电矒閹緤绱橮ENDING, VOTING, CONSENSUS_PASS, CHALLENGE_PERIOD, CHALLENGED, ARBITRATION, SHELVED閿涘娓剁憰浣稿???
				if req.Status != pb.RechargeRequestStatus_RECHARGE_FINALIZED &&
					req.Status != pb.RechargeRequestStatus_RECHARGE_REJECTED {
					x.WitnessService.LoadRequest(&req)
					count++
				}
			}
		}
		if count > 0 {
			fmt.Printf("[VM] Loaded %d active recharge requests into memory\n", count)
		}
	}

	// 3. 閸旂姾娴囬棃鐐电矒閹胶娈戦幐鎴炲灛鐠佹澘???
	challengeMap, err := x.DB.Scan(keys.KeyChallengeRecordPrefix())
	if err == nil {
		count := 0
		for _, data := range challengeMap {
			var record pb.ChallengeRecord
			if err := unmarshalProtoCompat(data, &record); err == nil {
				if !record.Finalized {
					x.WitnessService.LoadChallenge(&record)
					count++
				}
			}
		}
		if count > 0 {
			fmt.Printf("[VM] Loaded %d active challenges into memory\n", count)
		}
	}

	return nil
}

// SetWitnessService 鐠佸墽鐤嗙憴浣界槈閼板懏婀囬崝鈽呯礄閻劋绨ù瀣槸閹存牞鍤滅€规矮绠熼柊宥囩枂???
func (x *Executor) SetWitnessService(svc *witness.Service) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.WitnessService = svc
}

// GetWitnessService 閼惧嘲褰囩憴浣界槈閼板懏婀囬崝?
func (x *Executor) GetWitnessService() *witness.Service {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.WitnessService
}

// SetKindFn 鐠佸墽鐤咾ind閹绘劕褰囬崙鑺ユ殶
func (x *Executor) SetKindFn(fn KindFn) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.KFn = fn
}

// PreExecuteBlock 妫板嫭澧界悰灞藉隘閸ф绱欐稉宥呭晸閺佺増宓佹惔鎿勭礆
func (x *Executor) PreExecuteBlock(b *pb.Block) (*SpecResult, error) {
	return x.preExecuteBlock(b, true)
}

func (x *Executor) preExecuteBlock(b *pb.Block, useCache bool) (*SpecResult, error) {
	if b == nil {
		return nil, ErrNilBlock
	}

	// 濡偓閺屻儳绱???
	if useCache {
		if cached, ok := x.Cache.Get(b.BlockHash); ok {
			return cached, nil
		}
	}

	if x.WitnessService != nil {
		x.WitnessService.SetCurrentHeight(b.Header.Height)
	}

	// 閸掓稑缂撻弬鎵畱閻樿埖鈧浇顫嬮崶?
	sv := NewStateView(x.ReadFn, x.ScanFn)
	receipts := make([]*Receipt, 0, len(b.Body))
	seenTxIDs := make(map[string]struct{}, len(b.Body))
	appliedCache := make(map[string]bool, len(b.Body))
	skippedDupInBlock := 0
	skippedApplied := 0

	// Step 1: 妫板嫭澹傞幓蹇撳隘閸ф绱濋弨鍫曟肠閹碘偓閺堝姘﹂弰鎾愁嚠
	pairs := collectPairsFromBlock(b)

	// Step 2 & 3: 娑撯偓濞嗏剝鈧囧櫢瀵ょ儤澧嶉張澶庮吂閸楁洜???
	pairBooks, err := x.rebuildOrderBooksForPairs(pairs, sv)
	if err != nil {
		return &SpecResult{
			BlockID:  b.BlockHash,
			ParentID: b.Header.PrevBlockHash,
			Height:   b.Header.Height,
			Valid:    false,
			Reason:   fmt.Sprintf("failed to rebuild order books: %v", err),
		}, nil
	}

	// 闁秴宸婚幍褑顢戝В蹇庨嚋娴溿倖???
	for idx, tx := range b.Body {
		if tx == nil {
			continue
		}

		txID := tx.GetTxId()
		if txID != "" {
			if _, exists := seenTxIDs[txID]; exists {
				skippedDupInBlock++
				continue
			}
			seenTxIDs[txID] = struct{}{}

			applied, ok := appliedCache[txID]
			if !ok {
				applied = x.isTxApplied(txID)
				appliedCache[txID] = applied
			}
			if applied {
				skippedApplied++
				continue
			}
		}
		// 閹绘劕褰囨禍銈嗘缁???
		kind, err := x.KFn(tx)
		if err != nil {
			return &SpecResult{
				BlockID:  b.BlockHash,
				ParentID: b.Header.PrevBlockHash,
				Height:   b.Header.Height,
				Valid:    false,
				Reason:   fmt.Sprintf("tx %d has invalid structure: %v", idx, err),
			}, nil
		}

		// 缂佺喍绔村▔銊ュ弳瑜版挸澧犻幍褑顢戞妯哄閸掗姘﹂弰?BaseMessage 娑擃叏绱濇笟?Handler 娴ｈ法???
		if base := getBaseMessage(tx); base != nil {
			base.ExecutedHeight = b.Header.Height
		}

		// 閼惧嘲褰囩€电懓绨查惃鍑ndler
		h, ok := x.Reg.Get(kind)
		if !ok {
			return &SpecResult{
				BlockID:  b.BlockHash,
				ParentID: b.Header.PrevBlockHash,
				Height:   b.Header.Height,
				Valid:    false,
				Reason:   fmt.Sprintf("no handler for tx %d (kind: %s)", idx, kind),
			}, nil
		}

		// Step 4: 婵″倹鐏夐弰?OrderTxHandler閿涘本鏁為崗?pairBooks
		// 閻楃懓鍩嗗▔銊﹀壈閿涙艾娲滄稉?HandlerRegistry 娑擃厼鐡ㄩ崒銊ф畱閺勵垰宕熸笟瀣剁礉閸︺劌鑻熼崣鎴炲⒔鐞涘苯顦挎稉顏勫隘閸ф???
		// 娑撳秷鍏橀惄瀛樺复娣囶喗鏁奸崡鏇氱伐閻ㄥ嫮濮搁幀浣碘偓鍌氱箑妞よ鍘犻梾鍡曠娑擃亜鐪柈銊ョ杽娓氬绱濈涵顔荤箽閺堫剚顐奸幍褑顢戞担璺ㄦ暏濮濓絿鈥橀惃鍕吂閸楁洜缈遍妴?
		if orderHandler, ok := h.(*OrderTxHandler); ok {
			localHandler := *orderHandler // 濞村懏瀚圭拹婵撶礉閸ョ姳???OrderTxHandler 缂佹挻鐎粻鈧崡鏇礉閸欘亪娓堕幏鐤 map 閹稿洭鎷＄€涙???
			localHandler.SetOrderBooks(pairBooks)
			h = &localHandler
		}

		// Step 5: 婵″倹鐏夐弰?WitnessServiceAware handler閿涘本鏁為崗?WitnessService
		if witnessAware, ok := h.(WitnessServiceAware); ok && x.WitnessService != nil {
			witnessAware.SetWitnessService(x.WitnessService)
		}

		// 閸掓稑缂撹箛顐ゅ弾閻愮櫢绱濋悽銊ょ艾婢惰精瑙﹂弮璺烘礀???
		snapshot := sv.Snapshot()

		// 閹笛嗩攽娴溿倖???
		ws, rc, err := h.DryRun(tx, sv)
		if err != nil {
			// 婵″倹???Handler 鏉╂柨娲栨禍?Receipt閿涘矁顕╅弰搴㈡Ц娑撯偓娑擃亜褰叉禒銉︾垼鐠侀璐熸径杈Е閻ㄥ嫧鈧粈绗熼崝锟犳晩鐠囶垪鈧繐绱欐俊鍌欑稇妫版繀绗夌搾绛圭礆
			// 鏉╂瑧顫掗幆鍛枌娑撳绗夋惔鏃€瀵曢幒澶嬫殻娑擃亜灏崸妤嬬礉閼板本妲哥拋鏉跨秿婢惰精瑙﹂悩鑸碘偓浣歌嫙缂佈呯敾
			if rc != nil {
				// 閸ョ偞绮撮悩鑸碘偓浣稿煂鐠囥儰姘﹂弰鎾村⒔鐞涘苯???
				sv.Revert(snapshot)

				// 绾喕绻氶悩鑸碘偓浣圭垼鐠囧棔???FAILED
				rc.Status = "FAILED"
				if rc.Error == "" {
					rc.Error = err.Error()
				}

				// 婵夘偄???Receipt 閸忓啯鏆熼幑顔艰嫙閺€鍫曟肠
				// 娴ｈ法鏁ら崠鍝勬健閺冨爼妫块幋瀹犫偓宀勬姜閺堫剙婀撮弮鍫曟？閿涘瞼鈥樻穱婵嗩樋閼哄倻鍋ｇ涵顔肩暰???
				rc.BlockHeight = b.Header.Height
				rc.Timestamp = b.Header.Timestamp
				// FAILED 娴溿倖妲楁稊鐔活唶瑜版洖娲栧姘倵閻ㄥ嫪缍戞０婵嗘彥???
				if base := getBaseMessage(tx); base != nil && base.FromAddress != "" {
					fbBal := GetBalance(sv, base.FromAddress, "FB")
					if fbBal != nil {
						rc.FBBalanceAfter = balanceToJSON(fbBal)
					}
				}
				receipts = append(receipts, rc)

				logs.Info("[VM] Tx %s mark as FAILED in block %d: %v", rc.TxID, b.Header.Height, err)
				continue
			}

			// 閻喐顒滈惃鍕礂???缁崵绮虹痪褔鏁婄拠顖ょ礄婵″倹妫ら弫鍫滄唉閺勬挻鐗稿蹇ョ礆閿涘苯娲栧姘嫙閹锋帞绮烽崠鍝勬健
			sv.Revert(snapshot)
			return &SpecResult{
				BlockID:  b.BlockHash,
				ParentID: b.Header.PrevBlockHash,
				Height:   b.Header.Height,
				Valid:    false,
				Reason:   fmt.Sprintf("tx %d protocol error: %v", idx, err),
			}, nil
		}

		// 鐏忓敋s鎼存梻鏁ら崚鐨乿erlay閿涘牆顩ч弸娣抮yRun濞屸剝婀侀惄瀛樺复閸愭獨v???
		for _, w := range ws {
			if w.Del {
				sv.Del(w.Key)
			} else {
				// 娴ｈ法???SetWithMeta 娣囨繄鏆€閸忓啯鏆熼幑?
				if svWithMeta, ok := sv.(interface {
					SetWithMeta(string, []byte, bool, string)
				}); ok {
					svWithMeta.SetWithMeta(w.Key, w.Value, w.SyncStateDB, w.Category)
				} else {
					sv.Set(w.Key, w.Value)
				}
			}
		}
		// 婵夘偄???Receipt 閸忓啯鏆熼幑?
		// 娴ｈ法鏁ら崠鍝勬健閺冨爼妫块幋瀹犫偓宀勬姜閺堫剙婀撮弮鍫曟？閿涘瞼鈥樻穱婵嗩樋閼哄倻鍋ｇ涵顔肩暰???
		if rc != nil {
			rc.BlockHeight = b.Header.Height
			rc.Timestamp = b.Header.Timestamp

			// 閹规洝骞忔禍銈嗘閹笛嗩攽閸氬海???FB 娴ｆ瑩顤傝箛顐ゅ弾閿涘牅???StateView 娑擃叀顕伴崣鏍电礉閸欏秵妲цぐ鎾冲 tx 閹笛嗩攽閸氬海娈戦惇鐔风杽閻樿埖鈧緤???
			if base := getBaseMessage(tx); base != nil && base.FromAddress != "" {
				fbBal := GetBalance(sv, base.FromAddress, "FB")
				if fbBal != nil {
					rc.FBBalanceAfter = balanceToJSON(fbBal)
				}
			}
		}
		receipts = append(receipts, rc)
	}
	if skippedDupInBlock > 0 || skippedApplied > 0 {
		logs.Debug(
			"[VM] skipped txs in block: height=%d hash=%s dup=%d applied=%d body=%d",
			b.Header.Height, b.BlockHash, skippedDupInBlock, skippedApplied, len(b.Body),
		)
	}

	if err := x.applyWitnessFinalizedEvents(sv, b.Header.Height); err != nil {
		return nil, err
	}

	// ========== 閸栧搫娼℃總鏍уС娑撳孩澧滅紒顓″瀭閸掑棗???==========
	if b.Header.Miner != "" {
		// 1. 鐠侊紕鐣婚崠鍝勬健閸╄櫣顢呮總鏍уС閿涘牏閮寸紒鐔奉杻閸欐埊???
		blockReward := CalculateBlockReward(b.Header.Height, DefaultBlockRewardParams)

		// 2. 濮瑰洦鈧姘﹂弰鎾村缂侇叀???
		totalFees := big.NewInt(0)
		for _, rc := range receipts {
			if rc.Fee != "" {
				fee, err := ParseBalance(rc.Fee)
				if err == nil {
					totalFees, _ = SafeAdd(totalFees, fee)
				}
			}
		}

		// 3. 鐠侊紕鐣婚幀璇差殯???
		totalReward, _ := SafeAdd(blockReward, totalFees)

		if totalReward.Sign() > 0 {
			// 4. 閹稿鐦笟瀣瀻???
			ratio := DefaultBlockRewardParams.WitnessRewardRatio
			totalRewardDec := decimal.NewFromBigInt(totalReward, 0)

			witnessRewardDec := totalRewardDec.Mul(ratio)
			minerRewardDec := totalRewardDec.Sub(witnessRewardDec)

			witnessReward := witnessRewardDec.BigInt()
			minerReward := minerRewardDec.BigInt()

			// 5. 閸掑棝鍘ょ紒娆戠唵???(70%)
			// 绾喖鐣鹃幀褎鏁兼潻娑崇窗婵″倹鐏夐惌鍨紣鐠愶附鍩涙稉宥呯摠閸︻煉绱濋懛顏勫З閸掓稑缂撻弬鎷屽???
			// 鏉╂瑧鈥樻穱婵囧閺堝濡悙鐟版躬閹笛嗩攽婵傛牕濮抽弮鏈甸獓閻㈢喓娴夐崥宀€???WriteOp
			minerAccountKey := keys.KeyAccount(b.Header.Miner)
			minerAccountData, exists, err := sv.Get(minerAccountKey)
			var minerAccount pb.Account
			if err == nil && exists {
				_ = unmarshalProtoCompat(minerAccountData, &minerAccount)
			} else {
				// 閻灝浼愮拹锔藉煕娑撳秴鐡ㄩ崷顭掔礉閸掓稑缂撻弬鎷屽閹村嚖绱欐稉宥呭晙閸栧懎???Balances 鐎涙顔岄敍?
				minerAccount = pb.Account{
					Address: b.Header.Miner,
				}
			}
			// 娣囨繂鐡ㄩ惌鍨紣鐠愶附鍩涢敍鍫滅瑝閸栧懎鎯堟担娆擃杺???
			rewardedAccountData, _ := proto.Marshal(&minerAccount)
			sv.Set(minerAccountKey, rewardedAccountData)

			// 娴ｈ法鏁ら崚鍡欘瀲鐎涙ê鍋嶉弴瀛樻煀閻灝浼愭担娆擃杺
			const RewardToken = "FB"
			minerFBBal := GetBalance(sv, b.Header.Miner, RewardToken)
			currentBal, _ := ParseBalance(minerFBBal.Balance)
			newBal, _ := SafeAdd(currentBal, minerReward)
			minerFBBal.Balance = newBal.String()
			SetBalance(sv, b.Header.Miner, RewardToken, minerFBBal)

			// 6. 閸掑棝鍘ょ紒娆掝潌鐠囦浇鈧懎顨涢崝杈ㄧ潨 (30%)
			if witnessReward.Sign() > 0 {
				rewardPoolKey := keys.KeyWitnessRewardPool()
				currentPoolData, exists, _ := sv.Get(rewardPoolKey)
				currentPool := big.NewInt(0)
				if exists {
					currentPool, _ = ParseBalance(string(currentPoolData))
				}
				newPool, _ := SafeAdd(currentPool, witnessReward)
				sv.Set(rewardPoolKey, []byte(newPool.String()))
			}

			// 7. 娣囨繂鐡ㄦ總鏍уС鐠佹澘???
			// 娴ｈ法鏁ら崠鍝勬健閺冨爼妫块幋瀹犫偓宀勬姜閺堫剙婀撮弮鍫曟？閿涘瞼鈥樻穱婵嗩樋閼哄倻鍋ｇ涵顔肩暰???
			rewardRecord := &pb.BlockReward{
				Height:        b.Header.Height,
				Miner:         b.Header.Miner,
				MinerReward:   minerReward.String(),
				WitnessReward: witnessReward.String(),
				TotalFees:     totalFees.String(),
				BlockReward:   blockReward.String(),
				Timestamp:     b.Header.Timestamp,
			}
			rewardData, _ := proto.Marshal(rewardRecord)
			sv.Set(keys.KeyBlockReward(b.Header.Height), rewardData)
		}
	}

	// 閸掓稑缂撻幍褑顢戠紒鎾寸亯
	res := &SpecResult{
		BlockID:  b.BlockHash,
		ParentID: b.Header.PrevBlockHash,
		Height:   b.Header.Height,
		Valid:    true,
		Receipts: receipts,
		Diff:     sv.Diff(),
	}

	// 缂傛挸鐡ㄧ紒鎾寸亯
	x.Cache.Put(res)
	return res, nil
}

// CommitFinalizedBlock 閺堚偓缂佸牆瀵查幓鎰唉閿涘牆鍟撻崗銉︽殶閹诡喖绨遍敍?
func (x *Executor) CommitFinalizedBlock(b *pb.Block) error {
	if b == nil {
		return ErrNilBlock
	}

	x.mu.Lock()
	defer x.mu.Unlock()

	// 娴兼ê鍘涙担璺ㄦ暏缂傛挸鐡ㄩ惃鍕⒔鐞涘瞼绮ㄩ弸?
	if committed, blockHash := x.IsBlockCommitted(b.Header.Height); committed {
		if blockHash == b.BlockHash {
			return nil
		}
		return fmt.Errorf("block at height %d already committed with different hash: %s vs %s",
			b.Header.Height, blockHash, b.BlockHash)
	}

	// 缂傛挸鐡ㄧ紓鍝勩亼閿涙岸鍣搁弬鐗堝⒔???
	res, err := x.preExecuteBlock(b, false)
	if err != nil {
		return fmt.Errorf("re-execute block failed: %v", err)
	}

	if !res.Valid {
		return fmt.Errorf("block invalid: %s", res.Reason)
	}

	return x.applyResult(res, b)
}

// applyResult 鎼存梻鏁ら幍褑顢戠紒鎾寸亯閸掔増鏆熼幑顔肩氨閿涘牏绮烘稉鈧幓鎰唉閸忋儱褰涢敍?
// 鏉╂瑦妲搁崬顖欑閻ㄥ嫭娓剁紒鍫濆閹绘劒姘﹂悙鐧哥礉閹碘偓閺堝濮搁幀浣稿綁閸栨牠鍏橀崷銊ㄧ箹闁插苯顦╅悶?
func (x *Executor) applyResult(res *SpecResult, b *pb.Block) (err error) {
	// 瀵偓閸氼垱鏆熼幑顔肩氨娴兼俺???
	sess, err := x.DB.NewSession()
	if err != nil {
		return fmt.Errorf("failed to open db session: %v", err)
	}
	defer sess.Close()

	// ========== 缁楊兛绔村銉窗濡偓閺屻儱绠撶粵澶嬧偓?==========
	// 闂冨弶顒涢崥灞肩閸栧搫娼＄悮顐﹀櫢婢跺秵褰佹禍?
	if committed, blockHash := x.IsBlockCommitted(b.Header.Height); committed {
		if blockHash == b.BlockHash {
			return nil
		}
		return fmt.Errorf("block at height %d already committed with different hash: %s vs %s",
			b.Header.Height, blockHash, b.BlockHash)
	}

	// ========== 缁楊兛绨╁銉窗鎼存梻鏁ら幍鈧張澶屽Ц閹礁褰夐弴?==========
	// 闁秴???Diff 娑擃厾娈戦幍鈧張澶婂晸閹垮秳???
	stateDBUpdates := make([]*WriteOp, 0) // 閻劋绨弨鍫曟肠闂団偓鐟曚礁鎮撳銉ュ煂 StateDB 閻ㄥ嫭娲块弬?
	accountUpdates := make([]*WriteOp, 0) // 閻劋绨弨鍫曟肠鐠愶附鍩涢弴瀛樻煀閿涘瞼鏁ゆ禍搴㈡纯???stake index

	for i := range res.Diff {
		w := &res.Diff[i] // 娴ｈ法鏁ら幐鍥嫛閿涘苯娲滄稉?WriteOp 閻ㄥ嫭鏌熷▔鏇熸Ц閹稿洭鎷￠幒銉︽暪???
		// 濡偓濞村澶勯幋閿嬫纯閺傚府绱欓悽銊ょ艾閺囧瓨???stake index???
		if !w.Del && (w.Category == "account" || strings.HasPrefix(w.Key, "v1_account_")) {
			accountUpdates = append(accountUpdates, w)
		}

		// 閸愭瑥鍙嗛崚?DB
		if w.Del {
			x.DB.EnqueueDel(w.Key)
		} else {
			x.DB.EnqueueSet(w.Key, string(w.Value))
		}

		// 婵″倹鐏夐棁鈧憰浣告倱濮濄儱???StateDB閿涘矁顔囪ぐ鏇氱瑓???
		if w.SyncStateDB {
			stateDBUpdates = append(stateDBUpdates, w)
		}
	}

	// ========== 缁楊兛绗佸銉窗閺囧瓨???Stake Index ==========
	// 鐎甸€涚艾鐠愶附鍩涢弴瀛樻煀閿涘矂娓剁憰浣规纯???stake index
	if len(accountUpdates) > 0 {
		// 鐏忔繆鐦亸?DBManager 鏉烆剚宕叉稉鍝勫徔???UpdateStakeIndex 閺傝纭堕惃鍕???
		type StakeIndexUpdater interface {
			UpdateStakeIndex(oldStake, newStake decimal.Decimal, address string) error
		}
		if updater, ok := x.DB.(StakeIndexUpdater); ok {
			for _, w := range accountUpdates {
				// ???key 娑擃厽褰侀崣鏍ф勾閸р偓閿涘牊鐗稿蹇ョ窗v1_account_<address>???
				address := extractAddressFromAccountKey(w.Key)
				if address == "" {
					continue
				}

				// 娴ｈ法鏁ら崚鍡欘瀲鐎涙ê鍋嶉惃鍕稇妫版繆顓哥粻?stake
				// 濞夈劍鍓伴敍姘崇箹闁插矂娓剁憰浣风矤 StateDB ???WriteOps 娑擃叀顕伴崣?FB 娴ｆ瑩???
				// 閻㈠彉绨担娆擃杺瀹告彃鍨庣粋璇茬摠閸岊煉绱濋幋鎴滄粦閻╁瓨甯存禒?stateDBUpdates 娑擃厽鐓￠幍鎯ь嚠鎼存梻娈戞担娆擃杺閺囧瓨???

				// 鐠侊紕鐣婚弮?stake閿涘牅???StateDB session 鐠囪褰囬敍?
				oldStake := decimal.Zero
				if fbBalData, err := sess.Get(keys.KeyBalance(address, "FB")); err == nil && fbBalData != nil {
					var balRecord pb.TokenBalanceRecord
					if err := unmarshalProtoCompat(fbBalData, &balRecord); err == nil && balRecord.Balance != nil {
						oldStake, _ = decimal.NewFromString(balRecord.Balance.MinerLockedBalance)
					}
				}

				// 鐠侊紕鐣婚弬?stake閿涘牅绮犺ぐ鎾冲 WriteOps 娑擃厽鐓￠幍鎯ь嚠鎼存梻娈戞担娆擃杺閺囧瓨鏌婇敍?
				newStake := oldStake // 姒涙顓绘稉宥呭綁
				balanceKey := keys.KeyBalance(address, "FB")
				for _, wop := range stateDBUpdates {
					if wop.Key == balanceKey && !wop.Del {
						var balRecord pb.TokenBalanceRecord
						if err := unmarshalProtoCompat(wop.Value, &balRecord); err == nil && balRecord.Balance != nil {
							newStake, _ = decimal.NewFromString(balRecord.Balance.MinerLockedBalance)
						}
						break
					}
				}

				// 婵″倹???stake 閸欐垹鏁撻崣妯哄閿涘本娲块弬?stake index
				if !oldStake.Equal(newStake) {
					if err := updater.UpdateStakeIndex(oldStake, newStake, address); err != nil {
						// 鐠佹澘缍嶉柨娆掝嚖娴ｅ棔绗夋稉顓熸焽閹绘劒???
						fmt.Printf("[VM] Warning: failed to update stake index for %s: %v\n", address, err)
					}
				}
			}
		}
	}

	// ========== 缁楊剙娲撳銉窗閸氬本顒為崚?StateDB ==========
	// 缂佺喍绔存径鍕倞閹碘偓閺堝娓剁憰浣告倱濮濄儱???StateDB 閻ㄥ嫭鏆熼幑?
	// 閸楀厖濞囧▽鈩冩箒閺囧瓨鏌婇敍灞肩瘍鐠嬪啰???ApplyStateUpdate 娴犮儳鈥樼拋銈呯秼閸撳秹鐝惔锔炬畱閻樿埖鈧焦???
	// 鏉烆剚宕叉稉?[]interface{} 娴犮儲寮х搾铏复閸欙綀顩﹀Ч?
	stateDBUpdatesIface := make([]interface{}, len(stateDBUpdates))
	for i, w := range stateDBUpdates {
		stateDBUpdatesIface[i] = w
	}
	stateRoot, err := sess.ApplyStateUpdate(b.Header.Height, stateDBUpdatesIface)
	if err != nil {
		fmt.Printf("[VM] Warning: StateDB sync failed via session: %v\n", err)
	} else if stateRoot != nil {
		b.Header.StateRoot = stateRoot
	}

	// ========== 缁楊剙娲撳銉窗閸愭瑥鍙嗘禍銈嗘婢跺嫮鎮婇悩鑸碘偓?==========
	// 鐠佹澘缍嶅В蹇庨嚋娴溿倖妲楅惃鍕⒔鐞涘瞼濮搁幀浣告嫲闁挎瑨顕ゆ穱鈩冧紖
	for _, rc := range res.Receipts {
		// 娴溿倖妲楅悩鑸碘偓?
		statusKey := keys.KeyVMAppliedTx(rc.TxID)
		x.DB.EnqueueSet(statusKey, rc.Status)

		// 娴溿倖妲楅柨娆掝嚖娣団剝浼呴敍鍫濐洤閺嬫粍婀侀敍?
		if rc.Error != "" {
			errorKey := keys.KeyVMTxError(rc.TxID)
			x.DB.EnqueueSet(errorKey, rc.Error)
		}

		// 鐠佹澘缍嶆禍銈嗘閹碘偓閸︺劎娈戞妯哄
		heightKey := keys.KeyVMTxHeight(rc.TxID)
		x.DB.EnqueueSet(heightKey, fmt.Sprintf("%d", b.Header.Height))

		// 娣囨繂鐡ㄦ禍銈嗘???FB 娴ｆ瑩顤傝箛顐ゅ弾
		if rc.FBBalanceAfter != "" {
			fbKey := keys.KeyVMTxFBBalance(rc.TxID)
			x.DB.EnqueueSet(fbKey, rc.FBBalanceAfter)
		}
	}

	// ========== 缁楊剙娲撳銉ㄋ夐崗鍜冪窗閺囧瓨鏌婇崠鍝勬健娴溿倖妲楅悩鑸碘偓浣歌嫙娣囨繂鐡ㄩ崢鐔告瀮 ==========
	// 閸掓稑???TxID ???Receipt 閻ㄥ嫭妲х亸鍕剁礉閸旂娀鈧喐鐓￠幍?
	receiptMap := make(map[string]*Receipt, len(res.Receipts))
	for _, rc := range res.Receipts {
		receiptMap[rc.TxID] = rc
	}
	processedStatusTxs := make(map[string]struct{}, len(receiptMap))

	// 1. 閸忓牊娲块弬鏉垮隘閸фぞ鑵戦幍鈧張澶夋唉閺勬挾娈戦悩鑸碘偓浣告嫲閹笛嗩攽妤傛ê???
	// 娣囶喗鏁肩€电钖勫鏇犳暏閿涘奔绱伴崣宥嗘Ё閸掗绱堕崗銉ф畱 pb.Block ???
	for _, tx := range b.Body {
		if tx == nil {
			continue
		}
		base := tx.GetBase()
		if base == nil || base.TxId == "" {
			continue
		}
		if _, done := processedStatusTxs[base.TxId]; done {
			continue
		}

		// 缂佺喍绔村▔銊ュ弳閹笛嗩攽妤傛ê???

		// 閺嶈宓侀幍褑顢戦弨鑸靛祦閺囧瓨鏌婇悩鑸碘偓?
		rc, ok := receiptMap[base.TxId]
		if !ok {
			continue
		}
		processedStatusTxs[base.TxId] = struct{}{}
		base.ExecutedHeight = b.Header.Height
		if rc.Status == "SUCCEED" || rc.Status == "" {
			base.Status = pb.Status_SUCCEED
		} else {
			base.Status = pb.Status_FAILED
		}
	}

	// 2. 鐏忓棙娲块弬鏉挎倵閻ㄥ嫪姘﹂弰鎾冲斧閺傚洣绻氱€涙ê鍩岄弫鐗堝祦鎼存搫绱欐稉宥呭讲閸欐﹫绱氶敍灞间簰娓氬灝鎮楃紒顓熺叀???
	// 鐠併垹宕熺花璺ㄥЦ閹緤绱欑拋銏犲礋閻樿埖鈧降鈧椒鐜弽鑲╁偍瀵洜鐡戦敍澶婂弿闁劎???Diff 娑擃厾???WriteOp 閹貉冨煑
	// 濞夈劍鍓伴敍姝剅derTx ???娴溿倖妲楅弻銉嚄"闁插本妲告稉宥呭讲閸欐ê甯弬鍥风幢???鐠併垹宕熺花?闁插本妲搁崣顖氬綁閻樿埖鈧緤绱濇稉銈堚偓鍛氦鎼存洖鍨庣粋?
	type txRawSaver interface {
		SaveTxRaw(tx *pb.AnyTx) error
	}
	if saver, ok := x.DB.(txRawSaver); ok {
		savedRawTxs := make(map[string]struct{}, len(receiptMap))
		for _, tx := range b.Body {
			if tx == nil {
				continue
			}
			txID := tx.GetTxId()
			if txID == "" {
				continue
			}
			if _, done := savedRawTxs[txID]; done {
				continue
			}
			if _, executedThisBlock := receiptMap[txID]; !executedThisBlock {
				continue
			}
			// 娣囨繂鐡ㄦ禍銈嗘閸樼喐鏋冮敍鍫濆嚒閺囧瓨???Status ???ExecutedHeight???
			if err := saver.SaveTxRaw(tx); err != nil {
				// 鐠佹澘缍嶉柨娆掝嚖娴ｅ棔绗夋稉顓熸焽閹绘劒???
				fmt.Printf("[VM] Warning: failed to save tx raw %s: %v\n", txID, err)
			}
			savedRawTxs[txID] = struct{}{}
		}
	}

	// ========== 缁楊兛绨插銉窗閸愭瑥鍙嗛崠鍝勬健閹绘劒姘﹂弽鍥唶 ==========
	// 閻劋绨獮鍌滅搼閹勵梾???
	commitKey := keys.KeyVMCommitHeight(b.Header.Height)
	x.DB.EnqueueSet(commitKey, b.BlockHash)

	// 閸栧搫娼℃妯哄缁便垹???
	blockHeightKey := keys.KeyVMBlockHeight(b.Header.Height)
	x.DB.EnqueueSet(blockHeightKey, b.BlockHash)

	// ========== 缁楊剙鍙氬銉窗閹绘劒姘︽导姘崇樈楠炶泛鎮撳?==========
	if err := sess.Commit(); err != nil {
		return fmt.Errorf("failed to commit db session: %v", err)
	}

	// 閺囧瓨鏌婇崘鍛摠娑擃厾娈戦悩鑸碘偓浣圭壌閿涘牏鈥樻穱婵嗘倵缂侇厽澧界悰宀冨厴閻鍩岄張鈧弬鎵閺堫剨???
	if b.Header.StateRoot != nil {
		x.DB.CommitRoot(b.Header.Height, b.Header.StateRoot)
	}

	// 瀵搫鍩楅崚閿嬫煀閸掔増鏆熼幑顔肩氨閿涘牏鏁ゆ禍搴ㄦ姜閻樿埖鈧焦鏆熼幑顔炬畱 EnqueueSet???
	return x.DB.ForceFlush()
}

// IsBlockCommitted 濡偓閺屻儱灏崸妤佹Ц閸氾箑鍑￠幓鎰唉
func (x *Executor) IsBlockCommitted(height uint64) (bool, string) {
	key := keys.KeyVMCommitHeight(height)
	blockID, err := x.DB.Get(key)
	if err != nil || blockID == nil {
		return false, ""
	}
	return true, string(blockID)
}

// extractAddressFromAccountKey 娴犲氦澶勯幋?key 娑擃厽褰侀崣鏍ф勾閸р偓
// 閺嶇厧绱￠敍???_account_<address> ???account_<address>
func extractAddressFromAccountKey(key string) string {
	// 缁夊娅庨悧鍫熸拱閸撳秶???
	prefixes := []string{"v1_account_", "account_"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(key, prefix) {
			return strings.TrimPrefix(key, prefix)
		}
	}
	return ""
}

// calcStake 鐠侊紕鐣荤拹锔藉煕???stake閿涘牅濞囬悽銊ュ瀻缁傝鐡ㄩ崒顭掔礆
// CalcStake = FB.miner_locked_balance
func calcStake(sv StateView, addr string) (decimal.Decimal, error) {
	fbBal := GetBalance(sv, addr, "FB")
	ml, err := decimal.NewFromString(fbBal.MinerLockedBalance)
	if err != nil {
		ml = decimal.Zero
	}
	return ml, nil
}

// GetTransactionStatus 閼惧嘲褰囨禍銈嗘閻樿埖???
func (x *Executor) GetTransactionStatus(txID string) (string, error) {
	key := keys.KeyVMAppliedTx(txID)
	status, err := x.DB.Get(key)
	if err != nil {
		return "", err
	}
	if status == nil {
		return "PENDING", nil
	}
	return string(status), nil
}

func (x *Executor) isTxApplied(txID string) bool {
	if txID == "" {
		return false
	}
	key := keys.KeyVMAppliedTx(txID)
	status, err := x.DB.Get(key)
	if err != nil || status == nil {
		return false
	}
	return true
}

// GetTransactionError 閼惧嘲褰囨禍銈嗘闁挎瑨顕ゆ穱鈩冧紖
func (x *Executor) GetTransactionError(txID string) (string, error) {
	key := keys.KeyVMTxError(txID)
	errMsg, err := x.DB.Get(key)
	if err != nil {
		return "", err
	}
	if errMsg == nil {
		return "", nil
	}
	return string(errMsg), nil
}

// CleanupCache 濞撳懐鎮婄紓鎾崇摠
func (x *Executor) CleanupCache(finalizedHeight uint64) {
	if finalizedHeight > 100 {
		// 娣囨繄鏆€閺堚偓???00娑擃亪鐝惔锔炬畱缂傛挸???
		x.Cache.EvictBelow(finalizedHeight - 100)
	}
}

// ValidateBlock 妤犲矁鐦夐崠鍝勬健閸╃儤婀版穱鈩冧紖
func ValidateBlock(b *pb.Block) error {
	if b == nil {
		return ErrNilBlock
	}
	if b.BlockHash == "" {
		return fmt.Errorf("empty block hash")
	}
	if b.Header.Height == 0 && b.Header.PrevBlockHash != "" {
		return fmt.Errorf("genesis block should not have parent")
	}
	if b.Header.Height > 0 && b.Header.PrevBlockHash == "" {
		return fmt.Errorf("non-genesis block should have parent")
	}
	return nil
}

// collectPairsFromBlock 妫板嫭澹傞幓蹇撳隘閸ф绱濋弨鍫曟肠閹碘偓閺堝娓剁憰浣规尦閸氬牏娈戞禍銈嗘???
// 绾喖鐣鹃幀褎鏁兼潻娑崇窗鏉╂柨娲栭幒鎺戠碍閸氬海娈戞禍銈嗘鐎电懓鍨悰顭掔礉绾喕绻氭径姘冲Ν閻愮懓顦╅悶鍡涖€庢惔蹇庣???
func collectPairsFromBlock(b *pb.Block) []string {
	pairSet := make(map[string]struct{})

	for _, anyTx := range b.Body {
		// 閸欘亜顦╅悶?OrderTx
		orderTx := anyTx.GetOrderTx()
		if orderTx == nil {
			continue
		}

		// 閸欘亜顦╅悶?ADD 閹垮秳???
		if orderTx.Op != pb.OrderOp_ADD {
			continue
		}

		// 閻㈢喐鍨氭禍銈嗘???key
		pair := utils.GeneratePairKey(orderTx.BaseToken, orderTx.QuoteToken)
		pairSet[pair] = struct{}{}
	}

	// 鏉烆剚宕叉稉?slice 楠炶埖甯撴惔蹇ョ礉绾喕绻氱涵顔肩暰閹囦憾閸樺棝銆庢惔?
	pairs := make([]string, 0, len(pairSet))
	for pair := range pairSet {
		pairs = append(pairs, pair)
	}
	sort.Strings(pairs)

	return pairs
}

//	娑撯偓濞嗏剝鈧囧櫢瀵ょ儤澧嶉張澶夋唉閺勬挸顕惃鍕吂閸楁洜???
//
// 鏉╂柨娲栭敍姝產p[pair]*matching.OrderBook
func (x *Executor) rebuildOrderBooksForPairs(pairs []string, sv StateView) (map[string]*matching.OrderBook, error) {
	if len(pairs) == 0 {
		return make(map[string]*matching.OrderBook), nil
	}

	pairBooks := make(map[string]*matching.OrderBook)

	for _, pair := range pairs {
		ob := matching.NewOrderBookWithSink(nil)

		// 2. 閸掑棗鍩嗛崝鐘烘祰娑旀壆娲忛崪灞藉礌閻╂娈戦張顏呭灇娴溿倛顓归崡?(is_filled:false)
		// 閸旂姾娴囨稊鎵磸 Top 500
		buyPrefix := keys.KeyOrderPriceIndexPrefix(pair, pb.OrderSide_BUY, false)
		buyOrders, err := x.DB.ScanKVWithLimitReverse(buyPrefix, 500)
		if err == nil {
			// ???keys 閹烘帒绨禒銉р€樻穱婵堚€樼€规碍鈧囦憾閸樺棝銆庢惔?
			buyKeys := make([]string, 0, len(buyOrders))
			for k := range buyOrders {
				buyKeys = append(buyKeys, k)
			}
			sort.Strings(buyKeys)
			for _, indexKey := range buyKeys {
				data := buyOrders[indexKey]
				orderID := extractOrderIDFromIndexKey(indexKey)
				if orderID == "" {
					continue
				}
				x.loadOrderToBook(orderID, data, ob, sv)
			}
		}

		// 閸旂姾娴囬崡鏍磸 Top 500
		sellPrefix := keys.KeyOrderPriceIndexPrefix(pair, pb.OrderSide_SELL, false)
		sellOrders, err := x.DB.ScanKVWithLimit(sellPrefix, 500)
		if err == nil {
			// ???keys 閹烘帒绨禒銉р€樻穱婵堚€樼€规碍鈧囦憾閸樺棝銆庢惔?
			sellKeys := make([]string, 0, len(sellOrders))
			for k := range sellOrders {
				sellKeys = append(sellKeys, k)
			}
			sort.Strings(sellKeys)
			for _, indexKey := range sellKeys {
				data := sellOrders[indexKey]
				orderID := extractOrderIDFromIndexKey(indexKey)
				if orderID == "" {
					continue
				}
				x.loadOrderToBook(orderID, data, ob, sv)
			}
		}

		pairBooks[pair] = ob
	}

	return pairBooks, nil
}

// 鏉堝懎濮弬瑙勭《閿涙矮绮犻弫鐗堝祦閸旂姾娴囩拋銏犲礋閸掓媽顓归崡鏇犵勘
func (x *Executor) loadOrderToBook(orderID string, indexData []byte, ob *matching.OrderBook, sv StateView) {
	// 鐏忔繆鐦禒?OrderState 鐠囪褰囬敍鍫熸付閺傛壆濮搁幀渚婄礆
	orderStateKey := keys.KeyOrderState(orderID)
	orderStateData, err := x.DB.GetKV(orderStateKey)
	if err == nil && len(orderStateData) > 0 {
		var orderState pb.OrderState
		if err := unmarshalProtoCompat(orderStateData, &orderState); err == nil {
			matchOrder, _ := convertOrderStateToMatchingOrder(&orderState)
			if matchOrder != nil {
				ob.AddOrderWithoutMatch(matchOrder)
				return
			}
		}
	}

	// 閸忕厧顔愰柅鏄忕帆閿涙矮???indexData ???sv 鐠囪???
	var orderTx pb.OrderTx
	if err := unmarshalProtoCompat(indexData, &orderTx); err == nil {
		matchOrder, _ := convertToMatchingOrderLegacy(&orderTx)
		if matchOrder != nil {
			ob.AddOrderWithoutMatch(matchOrder)
		}
	}
}

// extractOrderIDFromIndexKey 娴犲簼鐜弽鑲╁偍???key 娑擃厽褰侀崣?orderID
func extractOrderIDFromIndexKey(indexKey string) string {
	parts := strings.Split(indexKey, "|order_id:")
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}

// convertOrderStateToMatchingOrder ???pb.OrderState 鏉烆剚宕叉稉?matching.Order
func convertOrderStateToMatchingOrder(state *pb.OrderState) (*matching.Order, error) {
	if state == nil {
		return nil, fmt.Errorf("invalid order state")
	}

	priceBI, err := parsePositiveBalanceStrict("order price", state.Price)
	if err != nil {
		return nil, fmt.Errorf("invalid price: %w", err)
	}

	totalAmountBI, err := parsePositiveBalanceStrict("order amount", state.Amount)
	if err != nil {
		return nil, fmt.Errorf("invalid amount: %w", err)
	}

	filledBaseBI, err := parseBalanceStrict("order filled_base", state.FilledBase)
	if err != nil {
		return nil, fmt.Errorf("invalid filled_base: %w", err)
	}

	filledQuoteBI, err := parseBalanceStrict("order filled_quote", state.FilledQuote)
	if err != nil {
		return nil, fmt.Errorf("invalid filled_quote: %w", err)
	}

	filledTradeBI := filledBaseBI
	if state.BaseToken > state.QuoteToken {
		filledTradeBI = filledQuoteBI
	}

	remainingAmountBI, err := SafeSub(totalAmountBI, filledTradeBI)
	if err != nil || remainingAmountBI.Sign() <= 0 {
		return nil, fmt.Errorf(
			"order already filled (orderId=%s, amount=%s, filledBase=%s, filledQuote=%s, isFilled=%v)",
			state.OrderId, state.Amount, state.FilledBase, state.FilledQuote, state.IsFilled,
		)
	}

	var side matching.OrderSide
	if state.Side == pb.OrderSide_SELL {
		side = matching.SELL
	} else {
		side = matching.BUY
	}

	return &matching.Order{
		ID:     state.OrderId,
		Side:   side,
		Price:  balanceToDecimal(priceBI),
		Amount: balanceToDecimal(remainingAmountBI),
	}, nil
}

// convertToMatchingOrderLegacy 鐏忓棙妫悧?pb.OrderTx 鏉烆剚宕叉稉?matching.Order閿涘牆鍚嬬€硅鈧嶇礆
// 閺冄呭 OrderTx 濞屸剝???FilledBase/FilledQuote 鐎涙顔岄敍灞戒海鐠佹崘顓归崡鏇熸弓閹存劒???
func convertToMatchingOrderLegacy(ord *pb.OrderTx) (*matching.Order, error) {
	if ord == nil || ord.Base == nil {
		return nil, fmt.Errorf("invalid order")
	}

	priceBI, err := parsePositiveBalanceStrict("order price", ord.Price)
	if err != nil {
		return nil, fmt.Errorf("invalid price: %w", err)
	}

	amountBI, err := parsePositiveBalanceStrict("order amount", ord.Amount)
	if err != nil {
		return nil, fmt.Errorf("invalid amount: %w", err)
	}

	var side matching.OrderSide
	if ord.Side == pb.OrderSide_SELL {
		side = matching.SELL
	} else {
		side = matching.BUY
	}

	return &matching.Order{
		ID:     ord.Base.TxId,
		Side:   side,
		Price:  balanceToDecimal(priceBI),
		Amount: balanceToDecimal(amountBI),
	}, nil
}

// getBaseMessage 鏉堝懎濮崙鑺ユ殶閿涙矮???AnyTx 娑擃厽褰侀崣?BaseMessage
func getBaseMessage(tx *pb.AnyTx) *pb.BaseMessage {
	if tx == nil {
		return nil
	}
	switch v := tx.Content.(type) {
	case *pb.AnyTx_Transaction:
		return v.Transaction.Base
	case *pb.AnyTx_OrderTx:
		return v.OrderTx.Base
	case *pb.AnyTx_MinerTx:
		return v.MinerTx.Base
	case *pb.AnyTx_IssueTokenTx:
		return v.IssueTokenTx.Base
	case *pb.AnyTx_FreezeTx:
		return v.FreezeTx.Base
	case *pb.AnyTx_WitnessStakeTx:
		return v.WitnessStakeTx.Base
	case *pb.AnyTx_WitnessRequestTx:
		return v.WitnessRequestTx.Base
	case *pb.AnyTx_WitnessVoteTx:
		return v.WitnessVoteTx.Base
	case *pb.AnyTx_WitnessChallengeTx:
		return v.WitnessChallengeTx.Base
	case *pb.AnyTx_ArbitrationVoteTx:
		return v.ArbitrationVoteTx.Base
	case *pb.AnyTx_WitnessClaimRewardTx:
		return v.WitnessClaimRewardTx.Base
	case *pb.AnyTx_FrostWithdrawRequestTx:
		return v.FrostWithdrawRequestTx.Base
	case *pb.AnyTx_FrostWithdrawSignedTx:
		return v.FrostWithdrawSignedTx.Base
	case *pb.AnyTx_FrostVaultDkgCommitTx:
		return v.FrostVaultDkgCommitTx.Base
	case *pb.AnyTx_FrostVaultDkgShareTx:
		return v.FrostVaultDkgShareTx.Base
	case *pb.AnyTx_FrostVaultDkgComplaintTx:
		return v.FrostVaultDkgComplaintTx.Base
	case *pb.AnyTx_FrostVaultDkgRevealTx:
		return v.FrostVaultDkgRevealTx.Base
	case *pb.AnyTx_FrostVaultDkgValidationSignedTx:
		return v.FrostVaultDkgValidationSignedTx.Base
	case *pb.AnyTx_FrostVaultTransitionSignedTx:
		return v.FrostVaultTransitionSignedTx.Base
	}
	return nil
}

// balanceToJSON ???TokenBalance 鎼村繐鍨崠鏍﹁礋 JSON 鐎涙顑佹稉璇х礄閻劋???Receipt 韫囶偆鍙庨敍?
func balanceToJSON(bal *pb.TokenBalance) string {
	if bal == nil {
		return ""
	}
	type snap struct {
		Balance       string `json:"balance"`
		MinerLocked   string `json:"miner_locked,omitempty"`
		WitnessLocked string `json:"witness_locked,omitempty"`
		OrderFrozen   string `json:"order_frozen,omitempty"`
		LiquidLocked  string `json:"liquid_locked,omitempty"`
	}
	s := snap{
		Balance:       bal.Balance,
		MinerLocked:   bal.MinerLockedBalance,
		WitnessLocked: bal.WitnessLockedBalance,
		OrderFrozen:   bal.OrderFrozenBalance,
		LiquidLocked:  bal.LiquidLockedBalance,
	}
	data, _ := json.Marshal(s)
	return string(data)
}
