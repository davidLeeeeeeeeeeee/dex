package txpool

import (
	"dex/db"
	"dex/logs"
	"dex/network"
	"dex/utils"
	"encoding/hex"
	"fmt"
	lru "github.com/hashicorp/golang-lru"
	"github.com/shopspring/decimal"
	"log"
	"sort"
	"sync"
	"time"
)

// TxPool 交易池结构体
type TxPool struct {
	dbManager *db.Manager
	network   *network.Network

	mu sync.RWMutex

	// 内存缓存: pendingTx
	pendingAnyTxCache      *lru.Cache
	shortPendingAnyTxCache *lru.Cache
	cacheTx                *lru.Cache
	shortTxCache           *lru.Cache
	interval               time.Duration
}

var (
	instance *TxPool
	once     sync.Once
)

// GetInstance 获取 TxPool 的单例实例
func GetInstance() *TxPool {
	once.Do(func() {
		pendingAnyTxCache, err := lru.New(100000)
		shortPendingAnyTxCache, _ := lru.New(100000)
		cacheTx, _ := lru.New(100000)
		shortTxCache, _ := lru.New(100000)
		dataPath := "./data"
		dbManager, err := db.NewManager(dataPath)
		if err != nil {
			logs.Error("Failed to initialize DB manager: %v", err)
		}
		net := network.NewNetwork(dbManager)

		tp := &TxPool{
			dbManager:              dbManager,
			network:                net,
			interval:               3 * time.Second, // 自行决定同步频率
			pendingAnyTxCache:      pendingAnyTxCache,
			shortPendingAnyTxCache: shortPendingAnyTxCache,
			cacheTx:                cacheTx,
			shortTxCache:           shortTxCache,
		}

		// 启动时先从DB加载已有pendingTx
		tp.loadFromDB()

		instance = tp
	})
	return instance
}

// loadFromDB 从数据库加载"pending_tx_"记录到内存
func (p *TxPool) loadFromDB() {
	pendingAnyTxs, err := db.LoadPendingAnyTx(p.dbManager)
	if err != nil {
		logs.Verbose("[TxPool] Failed to load pending AnyTx from DB: %v", err)
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	for _, anyTx := range pendingAnyTxs {
		txID := ExtractAnyTxId(anyTx)
		if txID == "" {
			continue
		}
		p.pendingAnyTxCache.Add(txID, anyTx)
	}
	logs.Verbose("[TxPool] Loaded %d pending AnyTx from DB.\n", len(pendingAnyTxs))
}

func (p *TxPool) StoreAnyTx(anyTx *db.AnyTx) error {
	txID := anyTx.GetTxId()
	if txID == "" {
		return fmt.Errorf("txID is empty")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// 如果池里已经有了就跳过
	if _, exists := p.pendingAnyTxCache.Get(txID); exists {
		return nil
	}
	// 加入内存缓存
	p.pendingAnyTxCache.Add(txID, anyTx)
	p.cacheTx.Add(txID, anyTx)
	p.shortPendingAnyTxCache.Add(txID[2:18], txID)
	p.shortTxCache.Add(txID[2:18], txID)
	//logs.Trace("[TxPool] Added TxId %s to shortPendingAnyTxCache.", txID[2:18])
	// —— 直接落DB
	if err := db.SavePendingAnyTx(p.dbManager, anyTx); err != nil {
		return err
	}
	return nil
}

func (p *TxPool) RemoveAnyTx(txID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.pendingAnyTxCache.Remove(txID)
	p.shortPendingAnyTxCache.Remove(txID[2:18])
	// —— 直接删DB
	_ = db.DeletePendingAnyTx(p.dbManager, txID)
}

// 返回所有 target_height == height 的交易
func (p *TxPool) GetTxsByTargetHeight(height uint64) []*db.AnyTx {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var result []*db.AnyTx
	keys := p.pendingAnyTxCache.Keys()
	for _, k := range keys {
		keyStr, ok := k.(string)
		if !ok {
			continue
		}
		if value, exists := p.pendingAnyTxCache.Get(keyStr); exists {
			if anyTx, ok := value.(*db.AnyTx); ok {
				base := anyTx.GetBase()
				if base != nil && base.TargetHeight == height {
					result = append(result, anyTx)
				}
			}
		}
	}
	return result
}

// 返回内存里的 pendingTx
func (p *TxPool) GetPendingAnyTx() []*db.AnyTx {

	p.mu.RLock()
	defer p.mu.RUnlock()

	var result []*db.AnyTx
	// Step 1: 获取所有 key，并转为 []string
	rawKeys := p.pendingAnyTxCache.Keys()
	var keys []string
	for _, k := range rawKeys {
		if keyStr, ok := k.(string); ok {
			keys = append(keys, keyStr)
		}
	}

	// Step 2: 排序
	sort.Strings(keys)

	// Step 3: 遍历已排序的 key 获取值
	for _, k := range keys {
		if value, exists := p.pendingAnyTxCache.Get(k); exists {
			if anyTx, ok := value.(*db.AnyTx); ok {
				result = append(result, anyTx)
			}
		}
	}
	return result
}

// GetPending65500Tx returns up to the first 65500 AnyTx from pendingAnyTxCache.
func (p *TxPool) GetPending65500Tx() []*db.AnyTx {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var result []*db.AnyTx
	keys := p.pendingAnyTxCache.Keys()
	maxCount := 10000
	count := 0

	for _, k := range keys {
		if count >= maxCount {
			break
		}
		keyStr, ok := k.(string)
		if !ok {
			continue
		}
		if value, exists := p.pendingAnyTxCache.Get(keyStr); exists {
			if anyTx, ok := value.(*db.AnyTx); ok {
				result = append(result, anyTx)
				count++
			}
		}
	}
	return result
}

func (p *TxPool) ConcatFirst8Bytes(txs []*db.AnyTx) []byte {
	start := time.Now()
	defer func() {
		elapsed := time.Since(start)
		logs.Info("[ConcatFirst8Bytes] elapsed=%s", elapsed)
	}()
	var result []byte
	for _, tx := range txs {
		txID := tx.GetTxId()
		idBytes, err := hex.DecodeString(txID[2:])
		if err != nil {
			log.Fatalf("invalid hex string: %v", err)
		}
		// 如果交易标识的长度小于3，则跳过或按需求处理
		if len(idBytes) < 3 {
			continue
		}
		// 截取8个字节
		endIndex := 8
		if len(idBytes) < endIndex {
			endIndex = len(idBytes)
		}
		//logs.Trace("endIndex := 8 %x", idBytes[:endIndex])
		result = append(result, idBytes[:endIndex]...)
	}
	return result
}

func (p *TxPool) HasTransaction(txID string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if _, exists := p.pendingAnyTxCache.Get(txID); exists {
		return true
	}
	return false
}

func (p *TxPool) GetTransactionById(txID string) *db.AnyTx {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if v, ok := p.pendingAnyTxCache.Get(txID); ok {
		if tx, ok := v.(*db.AnyTx); ok {
			// 现在 tx 是 *db.AnyTx 类型
			return tx
		}
	}
	return nil
}

// ExtractAnyTxId ---------------------------------------------------------------------------
//
//	用于从 AnyTx 提取 TxId
//
// ---------------------------------------------------------------------------
func ExtractAnyTxId(a *db.AnyTx) string {
	switch content := a.GetContent().(type) {
	case *db.AnyTx_IssueTokenTx:
		if content.IssueTokenTx.Base != nil {
			return content.IssueTokenTx.Base.TxId
		}
	case *db.AnyTx_FreezeTx:
		if content.FreezeTx.Base != nil {
			return content.FreezeTx.Base.TxId
		}
	case *db.AnyTx_Transaction:
		if content.Transaction.Base != nil {
			return content.Transaction.Base.TxId
		}
	case *db.AnyTx_OrderTx:
		if content.OrderTx.Base != nil {
			return content.OrderTx.Base.TxId
		}
	case *db.AnyTx_AddressTx:
		if content.AddressTx.Base != nil {
			return content.AddressTx.Base.TxId
		}
	case *db.AnyTx_CandidateTx:
		if content.CandidateTx.Base != nil {
			return content.CandidateTx.Base.TxId
		}

	}
	return ""
}

// 根据每笔Tx的发起方(FB余额)由大到小排序，
// 并将排序后序列生成一个聚合哈希(txs_hash)。
// 返回： (sortedTxs, txsHashHexString, error)
func SortTxsByFBBalanceAndComputeHash(dbMgr *db.Manager, txs []*db.AnyTx) ([]*db.AnyTx, string, error) {

	// 1. 准备一个临时结构，用来同时保存 Tx 和 对应FB余额
	type txWithBal struct {
		anyTx   *db.AnyTx
		balance decimal.Decimal
	}

	tmpList := make([]txWithBal, 0, len(txs))

	for _, tx := range txs {
		base := tx.GetBase()
		if base == nil {
			// 如果该笔Tx没有BaseMessage，不满足要求，跳过或做错误处理
			continue
		}
		fromAddr := base.FromAddress
		if fromAddr == "" {
			continue
		}
		// 2. 从DB中获取该 fromAddr 的账户并拿到其"FB"的可用余额
		acc, err := db.GetAccount(dbMgr, fromAddr)
		if err != nil {
			return nil, "", fmt.Errorf("get account %s error: %v", fromAddr, err)
		}
		var fbBalance decimal.Decimal
		if acc != nil {
			if tokenBal, ok := acc.Balances["FB"]; ok && tokenBal != nil {
				fbBalance, _ = decimal.NewFromString(tokenBal.Balance)
			}
		}
		tmpList = append(tmpList, txWithBal{
			anyTx:   tx,
			balance: fbBalance,
		})
	}

	// 3. 排序：按 FB余额 从大到小 排列
	sort.Slice(tmpList, func(i, j int) bool {
		return tmpList[i].balance.Cmp(tmpList[j].balance) > 0 // i>j => descending
	})

	// 4. 生成排序后的结果切片
	sortedTxs := make([]*db.AnyTx, len(tmpList))
	for i, v := range tmpList {
		sortedTxs[i] = v.anyTx
	}

	// 5. 计算聚合哈希(txs_hash)
	var allBytes []byte
	for _, v := range tmpList {
		base := v.anyTx.GetBase()
		if base == nil {
			continue
		}
		fromAddr := base.FromAddress
		balStr := v.balance.String()
		segment := []byte(base.TxId + "|" + fromAddr + "|" + balStr + "||")
		allBytes = append(allBytes, segment...)
	}

	finalHash := utils.Sha256Hash(allBytes)
	txsHashHex := fmt.Sprintf("%x", finalHash)

	return sortedTxs, txsHashHex, nil
}
func (p *TxPool) GetTxsByShortHashes(shortHashes [][]byte, isSync bool) []*db.AnyTx {
	//start := time.Now()
	//defer func() {
	//	elapsed := time.Since(start)
	//	logs.Info("[GetTxsByShortHashes] elapsed=%s", elapsed)
	//}()
	p.mu.RLock()
	defer p.mu.RUnlock()

	//logs.Trace("GetTxsByShortHashes called: %d shortHashes", len(shortHashes))

	var results []*db.AnyTx
	seen := make(map[string]struct{}, len(shortHashes))

	for _, shortHash := range shortHashes {
		shortHex := hex.EncodeToString(shortHash)
		//logs.Trace("Looking up short hash: %s", shortHex)
		var v interface{}
		var exists bool
		if isSync { //为了解决一个很奇怪的bug
			v, exists = p.shortTxCache.Get(shortHex)
		} else {
			v, exists = p.shortPendingAnyTxCache.Get(shortHex)
		}
		if !exists {
			//logs.Trace("No mapping in shortPendingAnyTxCache for %s", shortHex)
			continue
		}
		txID, ok := v.(string)
		if !ok {
			//logs.Trace("Invalid type for cache value, expected string, got %T", v)
			continue
		}
		//logs.Trace("shortHex %s -> full txID %s", shortHex, txID)

		if _, added := seen[txID]; added {
			//logs.Trace("txID %s already added, skipping", txID)
			continue
		}

		txData, found := p.cacheTx.Get(txID)
		if !found {
			//logs.Trace("pendingAnyTxCache miss for txID %s", txID)
			continue
		}
		anyTx, ok := txData.(*db.AnyTx)
		if !ok {
			//logs.Trace("pendingAnyTxCache value for %s is not *db.AnyTx (got %T)", txID, txData)
			continue
		}

		//logs.Trace("Appending AnyTx with txID %s", txID)
		results = append(results, anyTx)
		seen[txID] = struct{}{}
	}

	//logs.Trace("GetTxsByShortHashes returning %d results", len(results))
	return results
}

// 根据传入的 proposalTxs（多个8字节短 hash拼接而成）
// 对比 txpool 内部的 pendingAnyTxCache，返回两个结果：
// missing：键为 hex 格式的短 hash（那些未在本地找到的）
func (p *TxPool) AnalyzeProposalTxs(proposalTxs []byte) (missing map[string]bool, existTx map[string]bool) {
	start := time.Now()
	defer func() {
		elapsed := time.Since(start)
		logs.Info("[AnalyzeProposalTxs] elapsed=%s", elapsed)
	}()
	missing = make(map[string]bool)
	existTx = make(map[string]bool)
	const shortHashSize = 8
	for i := 0; i+shortHashSize <= len(proposalTxs); i += shortHashSize {
		shortHash := proposalTxs[i : i+shortHashSize]
		shortHashHex := hex.EncodeToString(shortHash)
		//logs.Trace("proposalTxs=%x", proposalTxs)
		matchedTxs := p.GetTxsByShortHashes([][]byte{shortHash}, false)
		if len(matchedTxs) == 0 {
			// 没有匹配到，记为缺失
			missing[shortHashHex] = true
		} else {
			// 如果存在则检查交易状态，假设只有 PENDING 状态的交易才算有效
			for _, tx := range matchedTxs {
				existTx[tx.GetTxId()] = true
			}
		}
	}
	return missing, existTx
}
