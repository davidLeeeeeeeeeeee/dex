package txpool

import (
	"dex/config"
	"dex/db"
	"dex/keys"
	"dex/logs"
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
)

// TxPool 交易池结构体
type TxPool struct {
	dbManager *db.Manager

	// 缓存
	mu                     sync.RWMutex
	Logger                 logs.Logger
	pendingAnyTxCache      *lru.Cache
	shortPendingAnyTxCache *lru.Cache
	cacheTx                *lru.Cache
	shortTxCache           *lru.Cache
	appliedTxCache         *lru.Cache // isTxApplied 缓存，避免频繁查 DB

	// 内部队列管理
	Queue     *txPoolQueue
	validator TxValidator

	// 控制
	address  string
	stopChan chan struct{}
	wg       sync.WaitGroup

	// DB 持久化队列（固定 worker + 有界队列，避免每 tx 起 goroutine）
	pendingSaveQueue   chan *pb.AnyTx
	pendingSaveWorkers int
	cfg                *config.Config
}

// NewTxPool 创建新的TxPool实例（替代GetInstance）
func NewTxPool(dbManager *db.Manager, validator TxValidator, address string, logger logs.Logger) (*TxPool, error) {
	return NewTxPoolWithConfig(dbManager, validator, address, logger, nil)
}

// NewTxPoolWithConfig creates a TxPool with caller-provided config.
func NewTxPoolWithConfig(dbManager *db.Manager, validator TxValidator, address string, logger logs.Logger, cfg *config.Config) (*TxPool, error) {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	pendingAnyTxCache, err := lru.New(cfg.TxPool.PendingTxCacheSize)
	if err != nil {
		return nil, err
	}
	shortPendingAnyTxCache, _ := lru.New(cfg.TxPool.ShortPendingTxCacheSize)
	cacheTx, _ := lru.New(cfg.TxPool.CacheTxSize)
	shortTxCache, _ := lru.New(cfg.TxPool.ShortTxCacheSize)
	appliedTxCache, _ := lru.New(50000) // isTxApplied 缓存

	pendingSaveQueueSize := cfg.TxPool.MessageQueueSize
	if pendingSaveQueueSize <= 0 {
		pendingSaveQueueSize = 10000
	}
	pendingSaveWorkers := 4

	tp := &TxPool{
		dbManager:              dbManager,
		address:                address,
		Logger:                 logger,
		pendingAnyTxCache:      pendingAnyTxCache,
		shortPendingAnyTxCache: shortPendingAnyTxCache,
		cacheTx:                cacheTx,
		shortTxCache:           shortTxCache,
		appliedTxCache:         appliedTxCache,
		validator:              validator,
		stopChan:               make(chan struct{}),
		pendingSaveQueue:       make(chan *pb.AnyTx, pendingSaveQueueSize),
		pendingSaveWorkers:     pendingSaveWorkers,
		cfg:                    cfg,
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
	for i := 0; i < tp.pendingSaveWorkers; i++ {
		tp.wg.Add(1)
		go tp.runPendingSaveWorker(i)
	}

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
	if tp == nil || tp.Queue == nil {
		return fmt.Errorf("txpool is not initialized")
	}
	txID := anyTx.GetTxId()
	// Duplicate tx should be treated as accepted, even under pressure.
	// Otherwise peers keep receiving 429 for already-known tx and flood logs.
	if txID != "" && tp.HasTransaction(txID) {
		return nil
	}

	maxPending := tp.MaxPendingLimit()
	pending := tp.PendingLen()
	if maxPending > 0 && pending >= maxPending {
		return fmt.Errorf("txpool pending is full (%d/%d)", pending, maxPending)
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
		return fmt.Errorf("txpool queue is full (%d/%d)", len(tp.Queue.MsgChan), cap(tp.Queue.MsgChan))
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
	if len(txID) >= 18 {
		tp.shortPendingAnyTxCache.Remove(txID[2:18])
	}

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
func (tp *TxPool) isTxApplied(txID string) bool {
	if txID == "" || tp.dbManager == nil {
		return false
	}
	// 先查 LRU 缓存
	if tp.appliedTxCache != nil {
		if _, ok := tp.appliedTxCache.Get(txID); ok {
			return true
		}
	}
	status, err := tp.dbManager.Get(keys.KeyVMAppliedTx(txID))
	if err == nil && len(status) > 0 {
		if tp.appliedTxCache != nil {
			tp.appliedTxCache.Add(txID, struct{}{})
		}
		return true
	}
	return false
}

func (tp *TxPool) cacheAnyTxUnlocked(txID string, anyTx *pb.AnyTx, pending bool) {
	tp.cacheTx.Add(txID, anyTx)
	if pending {
		tp.pendingAnyTxCache.Add(txID, anyTx)
	}
	if len(txID) >= 18 {
		shortID := txID[2:18]
		tp.shortTxCache.Add(shortID, txID)
		if pending {
			tp.shortPendingAnyTxCache.Add(shortID, txID)
		}
	}
}

// CacheAnyTx stores tx only for short-hash lookup and does not put it into pending.
func (tp *TxPool) CacheAnyTx(anyTx *pb.AnyTx) error {
	if anyTx == nil {
		return fmt.Errorf("nil transaction")
	}
	txID := anyTx.GetTxId()
	if txID == "" {
		return fmt.Errorf("txID is empty")
	}
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.cacheAnyTxUnlocked(txID, anyTx, false)
	return nil
}

func (tp *TxPool) storeAnyTx(anyTx *pb.AnyTx) error {
	txID := anyTx.GetTxId()
	if txID == "" {
		return fmt.Errorf("txID is empty")
	}
	if tp.isTxApplied(txID) {
		return tp.CacheAnyTx(anyTx)
	}

	tp.mu.Lock()

	// 如果池里已经有了就跳过
	if _, exists := tp.pendingAnyTxCache.Get(txID); exists {
		tp.mu.Unlock()
		return nil
	}

	// 1. 先同步写入内存缓存（确保立即可用于区块还原）
	tp.cacheAnyTxUnlocked(txID, anyTx, true)

	tp.mu.Unlock()

	// 2. 入队由固定 worker 异步写 DB（有界队列，满时反压）
	if err := tp.enqueuePendingSave(anyTx); err != nil {
		tp.Logger.Debug("[TxPool] enqueue pending save failed for tx %s: %v", txID, err)
		return err
	}

	return nil
}

func (tp *TxPool) enqueuePendingSave(anyTx *pb.AnyTx) error {
	if anyTx == nil {
		return fmt.Errorf("nil transaction")
	}
	select {
	case tp.pendingSaveQueue <- anyTx:
		return nil
	case <-tp.stopChan:
		return fmt.Errorf("txpool is stopping")
	}
}

func (tp *TxPool) runPendingSaveWorker(workerID int) {
	defer tp.wg.Done()
	for {
		select {
		case <-tp.stopChan:
			// 停止前尽量排空队列，避免丢最后一批待落盘交易
			for {
				select {
				case anyTx := <-tp.pendingSaveQueue:
					tp.persistPendingAnyTx(anyTx, workerID)
				default:
					return
				}
			}
		case anyTx := <-tp.pendingSaveQueue:
			tp.persistPendingAnyTx(anyTx, workerID)
		}
	}
}

func (tp *TxPool) persistPendingAnyTx(anyTx *pb.AnyTx, workerID int) {
	if anyTx == nil {
		return
	}
	txID := anyTx.GetTxId()
	if err := tp.dbManager.SavePendingAnyTx(anyTx); err != nil {
		tp.Logger.Debug("[TxPool] pending save worker=%d failed for tx %s: %v", workerID, txID, err)
	}
}

// loadFromDB 从数据库加载"pending_tx_"记录到内存
func (p *TxPool) loadFromDB() {
	pendingAnyTxs, err := p.dbManager.LoadPendingAnyTx()
	if err != nil {
		logs.Verbose("[TxPool] Failed to load pending AnyTx from DB: %v", err)
		return
	}

	stalePendingKeys := make([]string, 0)
	p.mu.Lock()
	for _, anyTx := range pendingAnyTxs {
		txID := ExtractAnyTxId(anyTx)
		if txID == "" {
			continue
		}
		if p.isTxApplied(txID) {
			stalePendingKeys = append(stalePendingKeys, db.KeyPendingAnyTx(txID))
			continue
		}
		p.cacheAnyTxUnlocked(txID, anyTx, true)
	}
	p.mu.Unlock()

	for _, staleKey := range stalePendingKeys {
		_ = p.dbManager.DeleteKey(staleKey)
	}
	if len(stalePendingKeys) > 0 {
		logs.Warn("[TxPool] Dropped %d stale pending txs already applied by VM", len(stalePendingKeys))
	}

	logs.Verbose("[TxPool] Loaded %d pending AnyTx from DB.\n", len(pendingAnyTxs))
}

func (p *TxPool) StoreAnyTx(anyTx *pb.AnyTx) error {
	txID := anyTx.GetTxId()
	if txID == "" {
		return fmt.Errorf("txID is empty")
	}
	if p.isTxApplied(txID) {
		return p.CacheAnyTx(anyTx)
	}

	p.mu.Lock()

	// 如果池里已经有了就跳过
	if _, exists := p.pendingAnyTxCache.Get(txID); exists {
		p.mu.Unlock()
		return nil
	}
	// 加入内存缓存
	p.cacheAnyTxUnlocked(txID, anyTx, true)
	p.mu.Unlock()
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
	if len(txID) >= 18 {
		p.shortPendingAnyTxCache.Remove(txID[2:18])
	}
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

	cfg := p.cfg
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
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

// ComputeTxsHash 计算交易列表的累积哈希 (PoH风格: h_n = H(h_{n-1} + tx_id_n))
// 提案者有权决定交易顺序，共识层只验证给定顺序的哈希值
// 返回： txsHashHexString
func ComputeTxsHash(txs []*pb.AnyTx) string {
	// 初始哈希值使用空字节的哈希
	accumulatedHash := utils.Sha256Hash([]byte{})

	for _, tx := range txs {
		txID := tx.GetTxId()
		if txID == "" {
			continue
		}
		// 累积哈希: h_n = H(h_{n-1} + tx_id_n)
		combined := append(accumulatedHash, []byte(txID)...)
		accumulatedHash = utils.Sha256Hash(combined)
	}

	return fmt.Sprintf("%x", accumulatedHash)
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
	tp.mu.RLock()
	defer tp.mu.RUnlock()
	return tp.pendingAnyTxCache.Len()
}

func (tp *TxPool) MaxPendingLimit() int {
	if tp == nil || tp.cfg == nil {
		return 0
	}
	return tp.cfg.TxPool.MaxPendingTxs
}

func (tp *TxPool) QueueDepth() (int, int) {
	if tp == nil || tp.Queue == nil {
		return 0, 0
	}
	return len(tp.Queue.MsgChan), cap(tp.Queue.MsgChan)
}
