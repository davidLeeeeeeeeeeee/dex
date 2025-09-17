// txpool/interfaces.go
package txpool

import (
	"dex/config"
	"dex/db"
	"dex/interfaces"
	"dex/logs"
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

// NetworkManager 网络管理接口
type NetworkManager interface {
	IsKnownNode(pubKey string) bool
	AddOrUpdateNode(pubKey, ip string, isKnown bool)
}

// TxValidator 交易验证接口
type TxValidator interface {
	CheckAnyTx(tx *db.AnyTx) error
}

// TxPoolInterface 交易池接口
type TxPoolInterface interface {
	// 生命周期管理
	Start() error
	Stop() error

	// 交易管理
	StoreAnyTx(tx *db.AnyTx) error
	RemoveAnyTx(txID string)
	HasTransaction(txID string) bool
	GetTransactionById(txID string) *db.AnyTx

	// 查询操作
	GetPendingAnyTx() []*db.AnyTx
	GetPending65500Tx() []*db.AnyTx
	GetTxsByTargetHeight(height uint64) []*db.AnyTx
	GetTxsByShortHashes(shortHashes [][]byte, isSync bool) []*db.AnyTx

	// 分析操作
	AnalyzeProposalTxs(proposalTxs []byte) (missing map[string]bool, existTx map[string]bool)
	ConcatFirst8Bytes(txs []*db.AnyTx) []byte
}

// Config 交易池配置
type Config struct {
	MaxPendingTxs    int           // 最大pending交易数
	MaxShortCacheTxs int           // 短缓存最大交易数
	SyncInterval     time.Duration // 同步间隔
	EnableAutoLoad   bool          // 是否启动时自动从DB加载
}

// DefaultConfig 默认配置
func DefaultConfig() *Config {
	return &Config{
		MaxPendingTxs:    100000,
		MaxShortCacheTxs: 100000,
		SyncInterval:     3 * time.Second,
		EnableAutoLoad:   true,
	}
}

// ExtractAnyTxId 从 AnyTx 提取 TxId
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

// SortTxsByFBBalanceAndComputeHash 根据FB余额排序并计算哈希
func SortTxsByFBBalanceAndComputeHash(dbMgr interfaces.DBManager, txs []*db.AnyTx) ([]*db.AnyTx, string, error) {
	type txWithBal struct {
		anyTx   *db.AnyTx
		balance decimal.Decimal
	}

	tmpList := make([]txWithBal, 0, len(txs))

	for _, tx := range txs {
		base := tx.GetBase()
		if base == nil {
			continue
		}
		fromAddr := base.FromAddress
		if fromAddr == "" {
			continue
		}

		// 调用接口方法获取账户
		accInterface, err := dbMgr.GetAccount(fromAddr)
		if err != nil {
			// 账户不存在时，余额为0
			tmpList = append(tmpList, txWithBal{
				anyTx:   tx,
				balance: decimal.Zero,
			})
			continue
		}

		var fbBalance decimal.Decimal
		// 类型断言为具体类型
		if acc, ok := accInterface.(*db.Account); ok && acc != nil {
			if acc.Balances != nil {
				if tokenBal, exists := acc.Balances["FB"]; exists && tokenBal != nil {
					fbBalance, _ = decimal.NewFromString(tokenBal.Balance)
				}
			}
		}

		tmpList = append(tmpList, txWithBal{
			anyTx:   tx,
			balance: fbBalance,
		})
	}

	// 排序：按 FB余额 从大到小
	sort.Slice(tmpList, func(i, j int) bool {
		return tmpList[i].balance.Cmp(tmpList[j].balance) > 0
	})

	// 生成排序后的结果
	sortedTxs := make([]*db.AnyTx, len(tmpList))
	for i, v := range tmpList {
		sortedTxs[i] = v.anyTx
	}

	// 计算聚合哈希
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

// TxPool 交易池实现
type TxPool struct {
	// 依赖注入
	config    *Config
	dbManager interfaces.DBManager
	network   NetworkManager

	// 内部状态
	mu sync.RWMutex

	// 内存缓存
	pendingAnyTxCache      *lru.Cache
	shortPendingAnyTxCache *lru.Cache
	cacheTx                *lru.Cache
	shortTxCache           *lru.Cache

	// 控制
	stopCh chan struct{}
	wg     sync.WaitGroup

	// 状态
	started bool
	queue   *TxPoolQueue // 内部持有，不对外暴露
}

// NewTxPool 创建新的交易池实例
func NewTxPool(cfg *Config, dbManager interfaces.DBManager, network NetworkManager, validator TxValidator) (*TxPool, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	if dbManager == nil {
		return nil, fmt.Errorf("dbManager cannot be nil")
	}
	if network == nil {
		return nil, fmt.Errorf("network cannot be nil")
	}

	pendingAnyTxCache, err := lru.New(cfg.MaxPendingTxs)
	if err != nil {
		return nil, fmt.Errorf("failed to create pendingAnyTxCache: %w", err)
	}

	shortPendingAnyTxCache, err := lru.New(cfg.MaxShortCacheTxs)
	if err != nil {
		return nil, fmt.Errorf("failed to create shortPendingAnyTxCache: %w", err)
	}

	cacheTx, err := lru.New(cfg.MaxPendingTxs)
	if err != nil {
		return nil, fmt.Errorf("failed to create cacheTx: %w", err)
	}

	shortTxCache, err := lru.New(cfg.MaxShortCacheTxs)
	if err != nil {
		return nil, fmt.Errorf("failed to create shortTxCache: %w", err)
	}

	tp := &TxPool{
		config:                 cfg,
		dbManager:              dbManager,
		network:                network,
		pendingAnyTxCache:      pendingAnyTxCache,
		shortPendingAnyTxCache: shortPendingAnyTxCache,
		cacheTx:                cacheTx,
		shortTxCache:           shortTxCache,
		stopCh:                 make(chan struct{}),
		started:                false,
	}

	// 如果配置启用自动加载，从DB加载已有pendingTx
	if cfg.EnableAutoLoad {
		if err := tp.loadFromDB(); err != nil {
			logs.Verbose("[TxPool] Failed to load from DB: %v", err)
			// 不返回错误，允许继续运行
		}
	}
	// 创建内部队列时，需要传递网络配置而不是txpool配置
	// 创建一个默认的网络配置或者从某处获取
	networkCfg := config.NetworkConfig{
		MaxPeers:         100,
		HandshakeTimeout: 30 * time.Second,
	}
	// 创建内部队列
	tp.queue, err = NewTxPoolQueue(dbManager, validator, tp, networkCfg)
	if err != nil {
		return nil, err
	}
	return tp, nil
}

// Start 启动交易池
func (p *TxPool) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.started {
		return fmt.Errorf("TxPool is already started")
	}

	// 如果需要定期同步或其他后台任务，可以在这里启动
	// p.wg.Add(1)
	// go p.syncLoop()

	p.started = true
	logs.Info("[TxPool] Started")
	return nil
}

// Stop 停止交易池
func (p *TxPool) Stop() error {
	p.mu.Lock()
	if !p.started {
		p.mu.Unlock()
		return nil
	}
	p.started = false
	p.mu.Unlock()

	close(p.stopCh)
	p.wg.Wait()

	logs.Info("[TxPool] Stopped")
	return nil
}

// loadFromDB 从数据库加载pending交易
func (p *TxPool) loadFromDB() error {
	pendingAnyTxs, err := p.dbManager.LoadPendingAnyTx()
	if err != nil {
		return fmt.Errorf("failed to load pending txs: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	for _, anyTx := range pendingAnyTxs {
		txID := ExtractAnyTxId(anyTx)
		if txID == "" {
			continue
		}
		p.pendingAnyTxCache.Add(txID, anyTx)

		// 添加到短缓存索引
		if len(txID) >= 18 {
			p.shortPendingAnyTxCache.Add(txID[2:18], txID)
			p.shortTxCache.Add(txID[2:18], txID)
		}
	}

	logs.Verbose("[TxPool] Loaded %d pending AnyTx from DB", len(pendingAnyTxs))
	return nil
}

// StoreAnyTx 存储交易
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

	// 加入短哈希索引
	if len(txID) >= 18 {
		p.shortPendingAnyTxCache.Add(txID[2:18], txID)
		p.shortTxCache.Add(txID[2:18], txID)
	}

	// 持久化到数据库
	if err := p.dbManager.SavePendingAnyTx(anyTx); err != nil {
		// 失败时回滚内存状态
		p.pendingAnyTxCache.Remove(txID)
		p.cacheTx.Remove(txID)
		if len(txID) >= 18 {
			p.shortPendingAnyTxCache.Remove(txID[2:18])
			p.shortTxCache.Remove(txID[2:18])
		}
		return fmt.Errorf("failed to save to db: %w", err)
	}

	return nil
}

// RemoveAnyTx 移除交易
func (p *TxPool) RemoveAnyTx(txID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.pendingAnyTxCache.Remove(txID)
	p.cacheTx.Remove(txID)

	if len(txID) >= 18 {
		p.shortPendingAnyTxCache.Remove(txID[2:18])
		p.shortTxCache.Remove(txID[2:18])
	}

	// 从数据库删除，忽略错误
	_ = p.dbManager.DeletePendingAnyTx(txID)
}

// GetTxsByTargetHeight 获取指定高度的交易
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

// GetPendingAnyTx 获取所有pending交易（已排序）
func (p *TxPool) GetPendingAnyTx() []*db.AnyTx {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var result []*db.AnyTx

	// 获取所有key并转为字符串数组
	rawKeys := p.pendingAnyTxCache.Keys()
	var keys []string
	for _, k := range rawKeys {
		if keyStr, ok := k.(string); ok {
			keys = append(keys, keyStr)
		}
	}

	// 排序
	sort.Strings(keys)

	// 遍历已排序的key获取值
	for _, k := range keys {
		if value, exists := p.pendingAnyTxCache.Get(k); exists {
			if anyTx, ok := value.(*db.AnyTx); ok {
				result = append(result, anyTx)
			}
		}
	}
	return result
}

// GetPending65500Tx 获取最多10000笔pending交易
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

// ConcatFirst8Bytes 拼接交易前8字节
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
			log.Printf("invalid hex string: %v", err)
			continue
		}

		if len(idBytes) < 3 {
			continue
		}

		endIndex := 8
		if len(idBytes) < endIndex {
			endIndex = len(idBytes)
		}

		result = append(result, idBytes[:endIndex]...)
	}
	return result
}

// HasTransaction 检查交易是否存在
func (p *TxPool) HasTransaction(txID string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	_, exists := p.pendingAnyTxCache.Get(txID)
	return exists
}

// GetTransactionById 根据ID获取交易
func (p *TxPool) GetTransactionById(txID string) *db.AnyTx {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if v, ok := p.pendingAnyTxCache.Get(txID); ok {
		if tx, ok := v.(*db.AnyTx); ok {
			return tx
		}
	}
	return nil
}

// GetTxsByShortHashes 根据短哈希获取交易
func (p *TxPool) GetTxsByShortHashes(shortHashes [][]byte, isSync bool) []*db.AnyTx {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var results []*db.AnyTx
	seen := make(map[string]struct{}, len(shortHashes))

	for _, shortHash := range shortHashes {
		shortHex := hex.EncodeToString(shortHash)

		var v interface{}
		var exists bool

		if isSync {
			v, exists = p.shortTxCache.Get(shortHex)
		} else {
			v, exists = p.shortPendingAnyTxCache.Get(shortHex)
		}

		if !exists {
			continue
		}

		txID, ok := v.(string)
		if !ok {
			continue
		}

		if _, added := seen[txID]; added {
			continue
		}

		txData, found := p.cacheTx.Get(txID)
		if !found {
			continue
		}

		anyTx, ok := txData.(*db.AnyTx)
		if !ok {
			continue
		}

		results = append(results, anyTx)
		seen[txID] = struct{}{}
	}

	return results
}

// AnalyzeProposalTxs 分析提案交易
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

		matchedTxs := p.GetTxsByShortHashes([][]byte{shortHash}, false)
		if len(matchedTxs) == 0 {
			missing[shortHashHex] = true
		} else {
			for _, tx := range matchedTxs {
				existTx[tx.GetTxId()] = true
			}
		}
	}

	return missing, existTx
}
