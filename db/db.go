package db

import (
	"bytes"
	"dex/config"
	"dex/interfaces"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	statedb "dex/stateDB"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	dbReadKVKeysProbeDebugThreshold = 5 * time.Millisecond
	dbReadKVKeysProbeWarnThreshold  = 50 * time.Millisecond
)

// Manager 封装 PebbleDB 的管理器
type Manager struct {
	Db *pebble.DB
	mu sync.RWMutex

	// 队列通道，批量写的 goroutine 用它来取写请求
	writeQueueChan chan WriteTask
	// 强制刷盘通道
	forceFlushChan chan flushRequest
	// 用于通知写队列 goroutine 停止
	stopChan chan struct{}
	// 写队列运行统计（用于观测吞吐与背压）
	writeQueueEnqueueTotal         uint64
	writeQueueEnqueueSetTotal      uint64
	writeQueueEnqueueDeleteTotal   uint64
	writeQueueEnqueueBlockedCount  uint64
	writeQueueEnqueueBlockedNs     uint64
	writeQueueDequeuedTotal        uint64
	writeQueueFlushBatchTotal      uint64
	writeQueueFlushedTaskTotal     uint64
	writeQueueFlushErrTotal        uint64
	writeQueueFlushDurationNsTotal uint64
	writeQueueForceFlushTotal      uint64
	writeQueueMaxDepth             uint64
	writeQueueFlushInFlightSince   uint64   // UnixNano timestamp; 0 means no flush in progress
	writeQueueKeyCounters          sync.Map // map[string]*atomic.Uint64
	writeQueueBlockedKeyCounters   sync.Map // map[string]*atomic.Uint64

	// 还可以增加两个参数，用来控制"写多少/多长时间"就落库
	maxBatchSize  int                // 累计多少条就写一次
	flushInterval time.Duration      // 间隔多久强制写一次
	IndexMgr      *MinerIndexManager // 新增
	// 自增发号器
	seq   uint64
	seqMu sync.Mutex
	// 你可以在这里做一个 wait group，保证 close 的时候能等 goroutine 退出
	wg sync.WaitGroup
	// 缓存的区块切片，最多存 10 个
	cachedBlocks   []*pb.Block
	cachedBlocksMu sync.RWMutex
	Logger         logs.Logger
	cfg            *config.Config
	stateDB        *statedb.DB
	// Active miner snapshot cached in memory and refreshed by epoch.
	minerCacheMu      sync.RWMutex
	minerCacheEpoch   uint64
	minerCacheReady   bool
	minerParticipants []*pb.Account
	minerSampleRand   *rand.Rand
	minerSampleRandMu sync.Mutex
}

// NewManager 创建一个新的 DBManager 实例
func NewManager(path string, logger logs.Logger) (*Manager, error) {
	return NewManagerWithConfig(path, logger, nil)
}

// NewManagerWithConfig 创建 DBManager，可选注入整份 Config
func NewManagerWithConfig(path string, logger logs.Logger, cfg *config.Config) (*Manager, error) {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	opts := &pebble.Options{
		MaxOpenFiles: 500,
	}
	// 配置 Block Cache（关键性能参数！没有 cache 每次 Get 都会 CGo calloc/free）
	cacheSize := cfg.Database.BlockCacheSizeDB
	if cacheSize <= 0 {
		cacheSize = 32 << 20 // 默认 32MB
	}
	opts.Cache = pebble.NewCache(cacheSize)
	defer opts.Cache.Unref()

	if cfg.Database.MemTableSize > 0 {
		opts.MemTableSize = uint64(cfg.Database.MemTableSize)
	}
	numCompactors := cfg.Database.NumCompactors
	if numCompactors <= 0 {
		logs.Warn("[DB] NumCompactors=%d is unsafe for write-heavy workloads; forcing to 1", numCompactors)
		numCompactors = 1
	}
	opts.MaxConcurrentCompactions = func() int { return numCompactors }

	// Pebble 不自动创建父目录
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("failed to create db dir: %w", err)
	}
	db, err := pebble.Open(path, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open pebble db: %w", err)
	}

	indexMgr, err := NewMinerIndexManager(db, logger)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to create index manager: %w", err)
	}

	// 恢复 sequence counter
	seqVal := uint64(0)
	if raw, closer, err := db.Get([]byte("meta:seq_counter")); err == nil {
		if len(raw) >= 8 {
			seqVal = binary.BigEndian.Uint64(raw)
		}
		closer.Close()
	}
	stateCfg := cfg.Database.StateDB
	stateDir := strings.TrimSpace(stateCfg.DataDir)
	switch {
	case stateDir == "":
		stateDir = filepath.Join(path, "state")
	case !filepath.IsAbs(stateDir):
		stateDir = filepath.Join(path, stateDir)
	}
	stateStore, err := statedb.New(statedb.Config{
		Backend:         stateCfg.Backend,
		DataDir:         stateDir,
		ShardHexWidth:   stateCfg.ShardHexWidth,
		PageSize:        stateCfg.PageSize,
		CheckpointKeep:  stateCfg.CheckpointKeep,
		CheckpointEvery: stateCfg.CheckpointEvery,
	})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to init stateDB: %w", err)
	}

	manager := &Manager{
		Db:              db,
		IndexMgr:        indexMgr,
		seq:             seqVal,
		Logger:          logger,
		cfg:             cfg,
		stateDB:         stateStore,
		minerSampleRand: rand.New(rand.NewSource(time.Now().UnixNano())),
	}

	return manager, nil
}

// NewSession 创建一个新的数据库会话
func (m *Manager) NewSession() (interfaces.DBSession, error) {
	return &dbSession{manager: m}, nil
}

// dbSession 数据库会话实现
type dbSession struct {
	manager *Manager
}

func (s *dbSession) Get(key string) ([]byte, error) {
	return s.manager.Get(key)
}

func (s *dbSession) ApplyStateUpdate(height uint64, updates []interface{}) ([]byte, error) {
	return nil, nil
}

func (s *dbSession) Commit() error   { return nil }
func (s *dbSession) Rollback() error { return nil }
func (s *dbSession) Close() error    { return nil }

// prefixUpperBound 计算前缀的上界（用于 Pebble IterOptions.UpperBound）
func prefixUpperBound(prefix []byte) []byte {
	upper := make([]byte, len(prefix))
	copy(upper, prefix)
	for i := len(upper) - 1; i >= 0; i-- {
		upper[i]++
		if upper[i] != 0 {
			return upper
		}
	}
	return nil // prefix 全是 0xFF，无上界
}

func (manager *Manager) readStateKey(key string) ([]byte, error) {
	manager.mu.RLock()
	stateStore := manager.stateDB
	manager.mu.RUnlock()
	if stateStore == nil {
		return nil, fmt.Errorf("stateDB is not initialized")
	}
	val, exists, err := stateStore.Get(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, statedb.ErrNotFound
	}
	return val, nil
}

func (manager *Manager) readKVKey(key string) ([]byte, error) {
	manager.mu.RLock()
	db := manager.Db
	manager.mu.RUnlock()
	if db == nil {
		return nil, fmt.Errorf("database is not initialized or closed")
	}
	raw, closer, err := db.Get([]byte(key))
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	val := make([]byte, len(raw))
	copy(val, raw)
	return val, nil
}

func (manager *Manager) readStateKeys(keys []string) (map[string][]byte, error) {
	manager.mu.RLock()
	stateStore := manager.stateDB
	manager.mu.RUnlock()
	if stateStore == nil {
		return nil, fmt.Errorf("stateDB is not initialized")
	}
	return stateStore.GetMany(keys)
}

func (manager *Manager) readKVKeys(keys []string) (out map[string][]byte, err error) {
	startAt := time.Now()
	mode := "seek"
	uniqCount := 0
	defer func() {
		cost := time.Since(startAt)
		if err == nil && cost < dbReadKVKeysProbeDebugThreshold {
			return
		}
		if err != nil || cost >= dbReadKVKeysProbeWarnThreshold {
			logs.Warn(
				"[DB][readKVKeys] mode=%s keys=%d uniq=%d hits=%d cost=%s err=%v",
				mode, len(keys), uniqCount, len(out), cost, err,
			)
			return
		}
		logs.Debug(
			"[DB][readKVKeys] mode=%s keys=%d uniq=%d hits=%d cost=%s",
			mode, len(keys), uniqCount, len(out), cost,
		)
	}()

	manager.mu.RLock()
	db := manager.Db
	manager.mu.RUnlock()
	if db == nil {
		return nil, fmt.Errorf("database is not initialized or closed")
	}

	out = make(map[string][]byte, len(keys))
	if len(keys) == 0 {
		mode = "empty"
		return out, nil
	}

	sorted := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		sorted = append(sorted, key)
	}
	if len(sorted) == 0 {
		mode = "empty_filtered"
		return out, nil
	}
	uniqCount = len(sorted)
	sort.Strings(sorted)

	snap := db.NewSnapshot()
	defer snap.Close()
	iter, err := snap.NewIter(nil)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for _, key := range sorted {
		target := []byte(key)
		if !iter.SeekGE(target) {
			break
		}
		if !bytes.Equal(iter.Key(), target) {
			continue
		}
		raw := iter.Value()
		val := make([]byte, len(raw))
		copy(val, raw)
		out[key] = val
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return out, nil
}

func (manager *Manager) scanStateByPrefix(prefix string, limit int, reverse bool) (map[string][]byte, error) {
	manager.mu.RLock()
	stateStore := manager.stateDB
	manager.mu.RUnlock()
	if stateStore == nil {
		return nil, fmt.Errorf("stateDB is not initialized")
	}

	items := make(map[string][]byte)
	if err := stateStore.IterateLatestByPrefix(prefix, func(key string, value []byte) error {
		items[key] = value
		return nil
	}); err != nil {
		return nil, err
	}

	if limit <= 0 || len(items) <= limit {
		return items, nil
	}

	keysSlice := make([]string, 0, len(items))
	for k := range items {
		keysSlice = append(keysSlice, k)
	}
	sort.Strings(keysSlice)
	if reverse {
		for i, j := 0, len(keysSlice)-1; i < j; i, j = i+1, j-1 {
			keysSlice[i], keysSlice[j] = keysSlice[j], keysSlice[i]
		}
	}

	out := make(map[string][]byte, limit)
	for i, k := range keysSlice {
		if i >= limit {
			break
		}
		out[k] = items[k]
	}
	return out, nil
}

// Scan scans all keys with the given prefix and returns a map of key-value pairs
func (manager *Manager) Scan(prefix string) (map[string][]byte, error) {
	if keys.IsStatefulKey(prefix) {
		return manager.scanStateByPrefix(prefix, 0, false)
	}

	result := make(map[string][]byte)
	p := []byte(prefix)
	iter, err := manager.Db.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: prefixUpperBound(p)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	for iter.SeekGE(p); iter.Valid(); iter.Next() {
		k := make([]byte, len(iter.Key()))
		copy(k, iter.Key())
		v := make([]byte, len(iter.Value()))
		copy(v, iter.Value())
		result[string(k)] = v
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return result, nil
}

// ScanKVWithLimit 扫描指定前缀的所有键值对，最多返回 limit 条记录
func (manager *Manager) ScanKVWithLimit(prefix string, limit int) (map[string][]byte, error) {
	if keys.IsStatefulKey(prefix) {
		return manager.scanStateByPrefix(prefix, limit, false)
	}

	result := make(map[string][]byte)
	p := []byte(prefix)
	iter, err := manager.Db.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: prefixUpperBound(p)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	count := 0
	for iter.SeekGE(p); iter.Valid(); iter.Next() {
		if limit > 0 && count >= limit {
			break
		}
		k := make([]byte, len(iter.Key()))
		copy(k, iter.Key())
		v := make([]byte, len(iter.Value()))
		copy(v, iter.Value())
		result[string(k)] = v
		count++
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return result, nil
}

// ScanKVWithLimitReverse 反向扫描指定前缀的所有键值对，最多返回 limit 条记录
func (manager *Manager) ScanKVWithLimitReverse(prefix string, limit int) (map[string][]byte, error) {
	if keys.IsStatefulKey(prefix) {
		return manager.scanStateByPrefix(prefix, limit, true)
	}

	result := make(map[string][]byte)
	p := []byte(prefix)
	upper := prefixUpperBound(p)
	iter, err := manager.Db.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	count := 0
	// 反向扫描：SeekLT(upper) 然后 iter.Prev()
	for iter.SeekLT(upper); iter.Valid(); iter.Prev() {
		if limit > 0 && count >= limit {
			break
		}
		k := make([]byte, len(iter.Key()))
		copy(k, iter.Key())
		v := make([]byte, len(iter.Value()))
		copy(v, iter.Value())
		result[string(k)] = v
		count++
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return result, nil
}

// ScanByPrefix 扫描指定前缀的所有键值对
func (manager *Manager) ScanByPrefix(prefix string, limit int) (map[string]string, error) {
	if keys.IsStatefulKey(prefix) {
		stateItems, err := manager.scanStateByPrefix(prefix, limit, false)
		if err != nil {
			return nil, err
		}
		result := make(map[string]string, len(stateItems))
		for k, v := range stateItems {
			result[k] = string(v)
		}
		return result, nil
	}

	result := make(map[string]string)
	p := []byte(prefix)
	iter, err := manager.Db.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: prefixUpperBound(p)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	count := 0
	for iter.SeekGE(p); iter.Valid(); iter.Next() {
		if limit > 0 && count >= limit {
			break
		}
		k := make([]byte, len(iter.Key()))
		copy(k, iter.Key())
		v := make([]byte, len(iter.Value()))
		copy(v, iter.Value())
		result[string(k)] = string(v)
		count++
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return result, nil
}

func (manager *Manager) View(fn func(txn *TransactionView) error) error {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	snap := manager.Db.NewSnapshot()
	defer snap.Close()
	return fn(&TransactionView{Snap: snap})
}

// TransactionView 包装 pebble.Snapshot
type TransactionView struct {
	Snap *pebble.Snapshot
}

func (tv *TransactionView) NewIterator() *pebble.Iterator {
	iter, _ := tv.Snap.NewIter(nil)
	return iter
}

func (manager *Manager) Close() {
	// 1. 先做一次同步 flush
	if err := manager.ForceFlush(); err != nil {
		logs.Error("[db.Close] force flush failed: %v", err)
	}
	// 2. 通知写队列 goroutine 停止
	if manager.stopChan != nil {
		select {
		case <-manager.stopChan:
		default:
			close(manager.stopChan)
		}
	}
	// 3. 等待 goroutine 退出
	manager.wg.Wait()
	manager.stopChan = nil
	manager.forceFlushChan = nil
	// 4. 关闭 DB
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.stateDB != nil {
		if err := manager.stateDB.Close(); err != nil {
			logs.Error("[db.Close] stateDB close failed: %v", err)
		}
		manager.stateDB = nil
	}
	if manager.Db != nil {
		_ = manager.Db.Close()
		manager.Db = nil
	}
}

// Read 读取键对应的值
func (manager *Manager) Read(key string) (string, error) {
	raw, err := manager.Get(key)
	if err != nil {
		return "", err
	}
	val := string(raw)
	return val, nil
}

// Get 实现 vm.DBManager 接口，返回 []byte
func (manager *Manager) Get(key string) ([]byte, error) {
	if keys.IsStatefulKey(key) {
		return manager.readStateKey(key)
	}
	return manager.readKVKey(key)
}

// GetKV 直接从 KV 读取（绕过 StateDB）
func (manager *Manager) GetKV(key string) ([]byte, error) {
	return manager.readKVKey(key)
}

func (manager *Manager) GetKVs(keyList []string) (map[string][]byte, error) {
	result := make(map[string][]byte, len(keyList))
	if len(keyList) == 0 {
		return result, nil
	}

	stateKeys := make([]string, 0, len(keyList))
	kvKeys := make([]string, 0, len(keyList))
	for _, key := range keyList {
		if key == "" {
			continue
		}
		if keys.IsStatefulKey(key) {
			stateKeys = append(stateKeys, key)
		} else {
			kvKeys = append(kvKeys, key)
		}
	}

	if len(stateKeys) > 0 {
		stateValues, err := manager.readStateKeys(stateKeys)
		if err != nil {
			return nil, err
		}
		for k, v := range stateValues {
			result[k] = v
		}
	}

	if len(kvKeys) > 0 {
		kvValues, err := manager.readKVKeys(kvKeys)
		if err != nil {
			return nil, err
		}
		for k, v := range kvValues {
			result[k] = v
		}
	}

	return result, nil
}

// 将 db.Transaction 序列化为 [][]byte
func SerializeAllTransactions(txCopy []*pb.AnyTx) [][]byte {
	sort.Slice(txCopy, func(i, j int) bool { return txCopy[i].GetTxId() < txCopy[j].GetTxId() })
	var result [][]byte
	for _, tx := range txCopy {
		result = append(result, serializeTransaction(tx))
	}
	return result
}

func serializeTransaction(tx *pb.AnyTx) []byte {
	return []byte(tx.GetTxId() + "|" + tx.GetBase().FromAddress)
}

// NextIndex 获取下一个自增索引
func (m *Manager) NextIndex() (uint64, error) {
	m.seqMu.Lock()
	defer m.seqMu.Unlock()
	m.seq++
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, m.seq)
	if err := m.Db.Set([]byte("meta:seq_counter"), buf, pebble.Sync); err != nil {
		return 0, err
	}
	return m.seq, nil
}

// GetMinerByIndex 通过节点索引获取对应的矿工账户
func (m *Manager) GetMinerByIndex(index uint64) (*pb.Account, error) {
	if m.IndexMgr == nil {
		return nil, fmt.Errorf("IndexMgr not initialized")
	}
	addrOrKey, err := m.IndexMgr.GetAddressByIndex(index)
	if err != nil {
		return nil, err
	}
	// indexToAccount currently stores account key (e.g. v1_account_<addr>),
	// not plain address. Support both forms.
	if keys.IsAccountKey(addrOrKey) {
		raw, err := m.Get(addrOrKey)
		if err != nil {
			return nil, err
		}
		account := &pb.Account{}
		if err := ProtoUnmarshal(raw, account); err != nil {
			return nil, err
		}
		return account, nil
	}
	return m.GetAccount(addrOrKey)
}

// CompactOrderIndexes 强制对订单价格索引区间进行后台物理合并，清除 Tombstone
func (manager *Manager) CompactOrderIndexes() error {
	manager.mu.RLock()
	db := manager.Db
	manager.mu.RUnlock()
	if db == nil {
		return fmt.Errorf("database is not initialized or closed")
	}

	startStr := "pair:"
	if keys.KeyVersion != "" {
		startStr = keys.KeyVersion + "_pair:"
	}
	start := []byte(startStr)
	end := prefixUpperBound(start)

	// 调用底层 Pebble 的 Compact
	// 注意 Compact 会阻塞执行直到合并完成，所以调用方应在 goroutine 中调用
	return db.Compact(start, end, true)
}

// CompactStateOrderStates compacts StateDB order-state keys to purge tombstones
// generated by frequent order state updates.
func (manager *Manager) CompactStateOrderStates() error {
	manager.mu.RLock()
	stateStore := manager.stateDB
	manager.mu.RUnlock()
	if stateStore == nil {
		return fmt.Errorf("stateDB is not initialized")
	}
	return stateStore.CompactBusinessPrefix(keys.KeyOrderStatePrefix())
}
