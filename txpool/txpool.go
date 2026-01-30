package txpool

import (
	"dex/config"
	"dex/db"
	"dex/logs"
	"dex/network"
	"dex/pb"
	"dex/stats"
	"dex/utils"
	"encoding/hex"
	"fmt"
	"log"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru"
	"github.com/shopspring/decimal"
)

// TxPool 交易池结构体
type TxPool struct {
	dbManager *db.Manager
	network   *network.Network

	// 缓存
	mu                     sync.RWMutex
	Logger                 logs.Logger
	pendingAnyTxCache      *lru.Cache
	shortPendingAnyTxCache *lru.Cache
	cacheTx                *lru.Cache
	shortTxCache           *lru.Cache

	// 内部队列管理
	Queue     *txPoolQueue
	validator TxValidator

	// 控制
	address  string
	stopChan chan struct{}
	wg       sync.WaitGroup
}

// NewTxPool 创建新的TxPool实例（替代GetInstance）
func NewTxPool(dbManager *db.Manager, validator TxValidator, address string, logger logs.Logger) (*TxPool, error) {
	cfg := config.DefaultConfig()
	pendingAnyTxCache, err := lru.New(cfg.TxPool.PendingTxCacheSize)
	if err != nil {
		return nil, err
	}
	shortPendingAnyTxCache, _ := lru.New(cfg.TxPool.ShortPendingTxCacheSize)
	cacheTx, _ := lru.New(cfg.TxPool.CacheTxSize)
	shortTxCache, _ := lru.New(cfg.TxPool.ShortTxCacheSize)

	net := network.NewNetwork(dbManager)

	tp := &TxPool{
		dbManager:              dbManager,
		address:                address,
		Logger:                 logger,
		network:                net,
		pendingAnyTxCache:      pendingAnyTxCache,
		shortPendingAnyTxCache: shortPendingAnyTxCache,
		cacheTx:                cacheTx,
		shortTxCache:           shortTxCache,
		validator:              validator,
		stopChan:               make(chan struct{}),
	}

	// 创建内部队列
	tp.Queue = newTxPoolQueue(tp, validator)

	// 从DB加载已有pendingTx
	tp.loadFromDB()

	return tp, nil
}

// Start 启动交易池
func (tp *TxPool) Start() error {
	// 启动内部队列处理
	tp.wg.Add(1)
	go tp.Queue.runLoop()

	logs.Info("[TxPool] Started")
	return nil
}

// Stop 停止交易池
func (tp *TxPool) Stop() error {
	close(tp.stopChan)
	tp.wg.Wait()
	logs.Info("[TxPool] Stopped")
	return nil
}

// 提交交易（对外的主要接口）
func (tp *TxPool) SubmitTx(anyTx *pb.AnyTx, fromIP string, onAdded OnTxAddedCallback) error {
	if anyTx == nil {
		return fmt.Errorf("nil transaction")
	}

	msg := &txPoolMessage{
		Type:    msgAddTx,
		AnyTx:   anyTx,
		IP:      fromIP,
		OnAdded: onAdded,
	}

	select {
	case tp.Queue.MsgChan <- msg:
		return nil
	default:
		return fmt.Errorf("txpool queue is full")
	}
}

// RemoveTx 移除交易
func (tp *TxPool) RemoveTx(txID string) error {
	if txID == "" {
		return fmt.Errorf("empty txID")
	}

	tp.mu.Lock()
	defer tp.mu.Unlock()

	tp.pendingAnyTxCache.Remove(txID)
	tp.shortPendingAnyTxCache.Remove(txID[2:18])

	// 同时从DB删除
	return tp.dbManager.DeletePendingAnyTx(txID)
}

// 其他公开方法保持不变...
func (tp *TxPool) HasTransaction(txID string) bool {
	tp.mu.RLock()
	defer tp.mu.RUnlock()
	_, exists := tp.pendingAnyTxCache.Get(txID)
	return exists
}

func (tp *TxPool) GetTransactionById(txID string) *pb.AnyTx {
	tp.mu.RLock()
	defer tp.mu.RUnlock()
	if v, ok := tp.pendingAnyTxCache.Get(txID); ok {
		if tx, ok := v.(*pb.AnyTx); ok {
			return tx
		}
	}
	return nil
}

func (tp *TxPool) GetPendingTxs() []*pb.AnyTx {
	tp.mu.RLock()
	defer tp.mu.RUnlock()

	var result []*pb.AnyTx
	keys := tp.pendingAnyTxCache.Keys()
	for _, k := range keys {
		keyStr, ok := k.(string)
		if !ok {
			continue
		}
		if value, exists := tp.pendingAnyTxCache.Get(keyStr); exists {
			if anyTx, ok := value.(*pb.AnyTx); ok {
				base := anyTx.GetBase()
				if base != nil {
					result = append(result, anyTx)
				}
			}
		}
	}
	return result
}

// 内部方法
func (tp *TxPool) storeAnyTx(anyTx *pb.AnyTx) error {
	txID := anyTx.GetTxId()
	if txID == "" {
		return fmt.Errorf("txID is empty")
	}

	tp.mu.Lock()

	// 如果池里已经有了就跳过
	if _, exists := tp.pendingAnyTxCache.Get(txID); exists {
		tp.mu.Unlock()
		return nil
	}

	// 1. 先同步写入内存缓存（确保立即可用于区块还原）
	tp.pendingAnyTxCache.Add(txID, anyTx)
	tp.cacheTx.Add(txID, anyTx)
	if len(txID) >= 18 {
		tp.shortPendingAnyTxCache.Add(txID[2:18], txID)
		tp.shortTxCache.Add(txID[2:18], txID)
	}

	tp.mu.Unlock()

	// 2. 异步写入 DB（不阻塞共识流程）
	go func() {
		if err := tp.dbManager.SavePendingAnyTx(anyTx); err != nil {
			tp.Logger.Debug("[TxPool] Async DB save failed for tx %s: %v", txID, err)
		}
	}()

	return nil
}

// loadFromDB 从数据库加载"pending_tx_"记录到内存
func (p *TxPool) loadFromDB() {
	pendingAnyTxs, err := p.dbManager.LoadPendingAnyTx()
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

func (p *TxPool) StoreAnyTx(anyTx *pb.AnyTx) error {
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
	if err := p.dbManager.SavePendingAnyTx(anyTx); err != nil {
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
	_ = p.dbManager.DeletePendingAnyTx(txID)
}

// 返回内存里的 pendingTx
func (p *TxPool) GetPendingAnyTx() []*pb.AnyTx {

	p.mu.RLock()
	defer p.mu.RUnlock()

	var result []*pb.AnyTx
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
			if anyTx, ok := value.(*pb.AnyTx); ok {
				result = append(result, anyTx)
			}
		}
	}
	return result
}

// GetPending65500Tx returns up to the first 65500 AnyTx from pendingAnyTxCache.
func (p *TxPool) GetPending65500Tx() []*pb.AnyTx {
	p.mu.RLock()
	defer p.mu.RUnlock()
	cfg := config.DefaultConfig()
	var result []*pb.AnyTx
	keys := p.pendingAnyTxCache.Keys()
	maxCount := cfg.TxPool.MaxPendingTxs
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
			if anyTx, ok := value.(*pb.AnyTx); ok {
				result = append(result, anyTx)
				count++
			}
		}
	}
	return result
}

func (p *TxPool) ConcatFirst8Bytes(txs []*pb.AnyTx) []byte {
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
			log.Fatalf("invalid hex string: %v\n%s", err, debug.Stack())
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

// ExtractAnyTxId ---------------------------------------------------------------------------
//
//	用于从 AnyTx 提取 TxId
//
// ---------------------------------------------------------------------------
func ExtractAnyTxId(a *pb.AnyTx) string {
	return a.GetTxId()
}

// 根据每笔Tx的发起方(FB余额)由大到小排序，
// 并将排序后序列生成一个聚合哈希(txs_hash)。
// 返回： (sortedTxs, txsHashHexString, error)
func SortTxsByFBBalanceAndComputeHash(dbMgr *db.Manager, txs []*pb.AnyTx) ([]*pb.AnyTx, string, error) {

	// 1. 准备一个临时结构，用来同时保存 Tx 和 对应FB余额
	type txWithBal struct {
		anyTx   *pb.AnyTx
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
		acc, err := dbMgr.GetAccount(fromAddr)
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
	sortedTxs := make([]*pb.AnyTx, len(tmpList))
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
func (p *TxPool) GetTxsByShortHashes(shortHashes [][]byte, isSync bool) []*pb.AnyTx {
	//start := time.Now()
	//defer func() {
	//	elapsed := time.Since(start)
	//	logs.Info("[GetTxsByShortHashes] elapsed=%s", elapsed)
	//}()
	p.mu.RLock()
	defer p.mu.RUnlock()

	//logs.Trace("GetTxsByShortHashes called: %d shortHashes", len(shortHashes))

	var results []*pb.AnyTx
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
		anyTx, ok := txData.(*pb.AnyTx)
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

// GetChannelStats 返回 TxPool 的 channel 状态
func (tp *TxPool) GetChannelStats() []stats.ChannelStat {
	if tp.Queue != nil {
		return []stats.ChannelStat{
			stats.NewChannelStat("MsgChan", "TxPool", len(tp.Queue.MsgChan), cap(tp.Queue.MsgChan)),
		}
	}
	return nil
}

// PendingLen 返回 pending 交易缓存的长度
func (tp *TxPool) PendingLen() int {
	if tp.pendingAnyTxCache == nil {
		return 0
	}
	return tp.pendingAnyTxCache.Len()
}
