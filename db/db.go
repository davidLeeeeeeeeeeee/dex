package db

import (
	"bytes"
	"dex/config"
	"dex/interfaces"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"dex/stats"
	"dex/utils"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble"
	"google.golang.org/protobuf/proto"
)

// stateKVUpdate is the internal state-sync payload shape.
type stateKVUpdate struct {
	Key     string
	Value   []byte
	Deleted bool
}

// stateDBSession abstracts state backend sessions (disabled in current runtime).
type stateDBSession interface {
	Get(key string) ([]byte, bool, error)
	GetKV(key string) ([]byte, error)
	ApplyUpdate(height uint64, kvs ...stateKVUpdate) error
	Commit() error
	Rollback() error
	Close() error
	Root() []byte
}

// stateDB abstracts the optional state backend (formerly verkle/jmt).
type stateDB interface {
	Get(key string) ([]byte, bool, error)
	ApplyAccountUpdate(height uint64, kvs ...stateKVUpdate) error
	NewSession() (stateDBSession, error)
	Root() []byte
	CommitRoot(version uint64, root []byte)
	Close() error
}

// Manager 封装 PebbleDB 的管理器
type Manager struct {
	Db      *pebble.DB
	StateDB stateDB // optional state backend; nil means disabled
	mu      sync.RWMutex

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
	// 自增发号器 (替代 Badger Sequence)
	seq   uint64
	seqMu sync.Mutex
	// 你可以在这里做一个 wait group，保证 close 的时候能等 goroutine 退出
	wg sync.WaitGroup
	// 缓存的区块切片，最多存 10 个
	cachedBlocks   []*pb.Block
	cachedBlocksMu sync.RWMutex
	Logger         logs.Logger
	cfg            *config.Config
	// Active miner snapshot cached in memory and refreshed by epoch.
	minerCacheMu      sync.RWMutex
	minerCacheEpoch   uint64
	minerCacheReady   bool
	minerParticipants []*pb.Account
	minerSampleRand   *rand.Rand
	minerSampleRandMu sync.Mutex
}

type flushRequest struct {
	done chan error
}

type writeQueueMetricsSnapshot struct {
	enqueueTotal         uint64
	enqueueSetTotal      uint64
	enqueueDeleteTotal   uint64
	enqueueBlockedCount  uint64
	enqueueBlockedNs     uint64
	dequeuedTotal        uint64
	flushBatchTotal      uint64
	flushedTaskTotal     uint64
	flushErrTotal        uint64
	flushDurationNsTotal uint64
	forceFlushTotal      uint64
	maxDepth             uint64
	keyCounters          map[string]uint64
	blockedKeyCounters   map[string]uint64
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

	manager := &Manager{
		Db:              db,
		StateDB:         nil,
		IndexMgr:        indexMgr,
		seq:             seqVal,
		Logger:          logger,
		cfg:             cfg,
		minerSampleRand: rand.New(rand.NewSource(time.Now().UnixNano())),
	}

	return manager, nil
}

func (manager *Manager) InitWriteQueue(maxBatchSize int, flushInterval time.Duration) {
	cfg := manager.cfg
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	manager.maxBatchSize = maxBatchSize
	manager.flushInterval = flushInterval
	manager.resetWriteQueueMetrics()
	manager.writeQueueChan = make(chan WriteTask, cfg.Database.WriteQueueSize)
	manager.forceFlushChan = make(chan flushRequest, 1)
	manager.stopChan = make(chan struct{})
	manager.wg.Add(2)
	go manager.runWriteQueue()
	go manager.runWriteQueueWatchdog()
}

func (manager *Manager) resetWriteQueueMetrics() {
	manager.writeQueueEnqueueTotal = 0
	manager.writeQueueEnqueueSetTotal = 0
	manager.writeQueueEnqueueDeleteTotal = 0
	manager.writeQueueEnqueueBlockedCount = 0
	manager.writeQueueEnqueueBlockedNs = 0
	manager.writeQueueDequeuedTotal = 0
	manager.writeQueueFlushBatchTotal = 0
	manager.writeQueueFlushedTaskTotal = 0
	manager.writeQueueFlushErrTotal = 0
	manager.writeQueueFlushDurationNsTotal = 0
	manager.writeQueueForceFlushTotal = 0
	manager.writeQueueMaxDepth = 0
	manager.writeQueueFlushInFlightSince = 0
	manager.clearCounterMap(&manager.writeQueueKeyCounters)
	manager.clearCounterMap(&manager.writeQueueBlockedKeyCounters)
}

func (manager *Manager) observeQueueDepth() {
	q := len(manager.writeQueueChan)
	for {
		old := atomic.LoadUint64(&manager.writeQueueMaxDepth)
		if uint64(q) <= old {
			return
		}
		if atomic.CompareAndSwapUint64(&manager.writeQueueMaxDepth, old, uint64(q)) {
			return
		}
	}
}

func normalizeWriteQueueKeyBucket(key string) string {
	if key == "" {
		return "empty"
	}
	keyNoVer := key
	if idx := strings.IndexByte(key, '_'); idx > 1 && idx <= 4 && key[0] == 'v' {
		allDigits := true
		for i := 1; i < idx; i++ {
			if key[i] < '0' || key[i] > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			keyNoVer = key[idx+1:]
		}
	}
	switch {
	case strings.HasPrefix(keyNoVer, "snapshot_"):
		return "snapshot"
	case strings.HasPrefix(keyNoVer, "finalization_chits_"):
		return "finalization_chits"
	case strings.HasPrefix(keyNoVer, "consensus_sig_set_"):
		return "consensus_sig_set"
	case strings.HasPrefix(keyNoVer, "pending_anytx_"):
		return "pending_anytx"
	case strings.HasPrefix(keyNoVer, "anyTx_"):
		return "anyTx"
	case strings.HasPrefix(keyNoVer, "tx_"):
		return "tx"
	case strings.HasPrefix(keyNoVer, "order_"):
		return "order"
	case strings.HasPrefix(keyNoVer, "orderstate_"):
		return "orderstate"
	case strings.HasPrefix(keyNoVer, "blockdata_"):
		return "blockdata"
	case strings.HasPrefix(keyNoVer, "height_"):
		return "height"
	case strings.HasPrefix(keyNoVer, "blockid_"):
		return "blockid"
	case strings.HasPrefix(keyNoVer, "account_"):
		return "account"
	case strings.HasPrefix(keyNoVer, "balance_"):
		return "balance"
	case strings.HasPrefix(keyNoVer, "acc_order_"):
		return "acc_order"
	case strings.HasPrefix(keyNoVer, "frost_planning_log"):
		return "frost_planning_log"
	}
	parts := strings.Split(keyNoVer, "_")
	if len(parts) >= 2 {
		bucket := parts[0] + "_" + parts[1]
		if len(bucket) > 48 {
			return parts[0]
		}
		return bucket
	}
	if len(keyNoVer) > 48 {
		return keyNoVer[:48] + "..."
	}
	return keyNoVer
}

func (manager *Manager) addCounter(counterMap *sync.Map, key string) {
	if counterMap == nil || key == "" {
		return
	}
	val, _ := counterMap.LoadOrStore(key, &atomic.Uint64{})
	if c, ok := val.(*atomic.Uint64); ok && c != nil {
		c.Add(1)
	}
}

func (manager *Manager) clearCounterMap(counterMap *sync.Map) {
	if counterMap == nil {
		return
	}
	counterMap.Range(func(k, _ any) bool {
		counterMap.Delete(k)
		return true
	})
}

func (manager *Manager) snapshotCounterMapDelta(counterMap *sync.Map) map[string]uint64 {
	snapshot := make(map[string]uint64)
	if counterMap == nil {
		return snapshot
	}
	counterMap.Range(func(k, v any) bool {
		key, ok := k.(string)
		if !ok || key == "" {
			return true
		}
		c, ok := v.(*atomic.Uint64)
		if !ok || c == nil {
			return true
		}
		delta := c.Swap(0)
		if delta == 0 {
			return true
		}
		snapshot[key] = delta
		return true
	})
	return snapshot
}

type counterValue struct {
	Key   string
	Value uint64
}

func topCounterValues(cur map[string]uint64, topN int) []string {
	if topN <= 0 || len(cur) == 0 {
		return nil
	}
	values := make([]counterValue, 0, len(cur))
	for k, v := range cur {
		values = append(values, counterValue{Key: k, Value: v})
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Value != values[j].Value {
			return values[i].Value > values[j].Value
		}
		return values[i].Key < values[j].Key
	})
	if len(values) > topN {
		values = values[:topN]
	}
	result := make([]string, 0, len(values))
	for _, d := range values {
		result = append(result, fmt.Sprintf("%s=%d", d.Key, d.Value))
	}
	return result
}

func (manager *Manager) snapshotWriteQueueMetrics() writeQueueMetricsSnapshot {
	return writeQueueMetricsSnapshot{
		enqueueTotal:         atomic.LoadUint64(&manager.writeQueueEnqueueTotal),
		enqueueSetTotal:      atomic.LoadUint64(&manager.writeQueueEnqueueSetTotal),
		enqueueDeleteTotal:   atomic.LoadUint64(&manager.writeQueueEnqueueDeleteTotal),
		enqueueBlockedCount:  atomic.LoadUint64(&manager.writeQueueEnqueueBlockedCount),
		enqueueBlockedNs:     atomic.LoadUint64(&manager.writeQueueEnqueueBlockedNs),
		dequeuedTotal:        atomic.LoadUint64(&manager.writeQueueDequeuedTotal),
		flushBatchTotal:      atomic.LoadUint64(&manager.writeQueueFlushBatchTotal),
		flushedTaskTotal:     atomic.LoadUint64(&manager.writeQueueFlushedTaskTotal),
		flushErrTotal:        atomic.LoadUint64(&manager.writeQueueFlushErrTotal),
		flushDurationNsTotal: atomic.LoadUint64(&manager.writeQueueFlushDurationNsTotal),
		forceFlushTotal:      atomic.LoadUint64(&manager.writeQueueForceFlushTotal),
		maxDepth:             atomic.LoadUint64(&manager.writeQueueMaxDepth),
		keyCounters:          manager.snapshotCounterMapDelta(&manager.writeQueueKeyCounters),
		blockedKeyCounters:   manager.snapshotCounterMapDelta(&manager.writeQueueBlockedKeyCounters),
	}
}

func (manager *Manager) logWriteQueueStats(prev writeQueueMetricsSnapshot, interval time.Duration) writeQueueMetricsSnapshot {
	cur := manager.snapshotWriteQueueMetrics()
	seconds := interval.Seconds()
	if seconds <= 0 {
		seconds = 1
	}

	enqDelta := cur.enqueueTotal - prev.enqueueTotal
	setDelta := cur.enqueueSetTotal - prev.enqueueSetTotal
	delDelta := cur.enqueueDeleteTotal - prev.enqueueDeleteTotal
	deqDelta := cur.dequeuedTotal - prev.dequeuedTotal
	flushBatchDelta := cur.flushBatchTotal - prev.flushBatchTotal
	flushedTaskDelta := cur.flushedTaskTotal - prev.flushedTaskTotal
	flushErrDelta := cur.flushErrTotal - prev.flushErrTotal
	flushDurationDeltaNs := cur.flushDurationNsTotal - prev.flushDurationNsTotal
	forceFlushDelta := cur.forceFlushTotal - prev.forceFlushTotal
	blockedDelta := cur.enqueueBlockedCount - prev.enqueueBlockedCount
	blockedNsDelta := cur.enqueueBlockedNs - prev.enqueueBlockedNs

	avgBatch := 0.0
	avgFlushMs := 0.0
	if flushBatchDelta > 0 {
		avgBatch = float64(flushedTaskDelta) / float64(flushBatchDelta)
		avgFlushMs = float64(flushDurationDeltaNs) / float64(flushBatchDelta) / float64(time.Millisecond)
	}

	avgBlockMs := 0.0
	if blockedDelta > 0 {
		avgBlockMs = float64(blockedNsDelta) / float64(blockedDelta) / float64(time.Millisecond)
	}

	qLen := len(manager.writeQueueChan)
	qCap := cap(manager.writeQueueChan)
	msg := fmt.Sprintf(
		"[DBQueue] 10s stats q=%d/%d max=%d enq=%d(%.1f/s,set=%d,del=%d) deq=%d(%.1f/s) flushTasks=%d batches=%d avgBatch=%.1f avgFlush=%.2fms flushErr=%d forceFlush=%d blocked=%d avgBlock=%.2fms",
		qLen, qCap, cur.maxDepth,
		enqDelta, float64(enqDelta)/seconds, setDelta, delDelta,
		deqDelta, float64(deqDelta)/seconds,
		flushedTaskDelta, flushBatchDelta, avgBatch, avgFlushMs,
		flushErrDelta, forceFlushDelta, blockedDelta, avgBlockMs,
	)
	topKeys := topCounterValues(cur.keyCounters, 6)
	if len(topKeys) > 0 {
		msg += " topWriteKeys=" + strings.Join(topKeys, ",")
	}
	topBlockedKeys := topCounterValues(cur.blockedKeyCounters, 6)
	if len(topBlockedKeys) > 0 {
		msg += " topBlockedKeys=" + strings.Join(topBlockedKeys, ",")
	}
	if manager.Logger != nil {
		manager.Logger.Info(msg)
	} else {
		logs.Info(msg)
	}
	return cur
}

// 写队列的核心 goroutine 逻辑
func (manager *Manager) runWriteQueue() {
	defer manager.wg.Done()
	var batch []WriteTask
	batch = make([]WriteTask, 0, manager.maxBatchSize)
	ticker := time.NewTicker(manager.flushInterval)
	defer ticker.Stop()
	metricsTicker := time.NewTicker(10 * time.Second)
	defer metricsTicker.Stop()
	lastMetricsAt := time.Now()
	metricsPrev := manager.snapshotWriteQueueMetrics()

	flushCurrentBatch := func() error {
		if len(batch) == 0 {
			return nil
		}
		count := len(batch)
		start := time.Now()
		atomic.StoreUint64(&manager.writeQueueFlushInFlightSince, uint64(start.UnixNano()))
		err := manager.flushBatch(batch)
		atomic.StoreUint64(&manager.writeQueueFlushInFlightSince, 0)
		duration := time.Since(start)
		atomic.AddUint64(&manager.writeQueueFlushBatchTotal, 1)
		atomic.AddUint64(&manager.writeQueueFlushedTaskTotal, uint64(count))
		atomic.AddUint64(&manager.writeQueueFlushDurationNsTotal, uint64(duration))
		if err != nil {
			atomic.AddUint64(&manager.writeQueueFlushErrTotal, 1)
		}
		if duration >= 2*time.Second {
			logs.Warn("[DBQueue] slow flush batch=%d took=%s q=%d/%d", count, duration, len(manager.writeQueueChan), cap(manager.writeQueueChan))
		}
		batch = batch[:0]
		return err
	}

	for {
		select {
		case <-manager.stopChan:
			batch = manager.drainWriteQueue(batch)
			err := flushCurrentBatch()
			manager.resolvePendingForceFlush(err)
			return
		case task := <-manager.writeQueueChan:
			atomic.AddUint64(&manager.writeQueueDequeuedTotal, 1)
			batch = append(batch, task)
			if len(batch) >= manager.maxBatchSize {
				if err := flushCurrentBatch(); err != nil {
					logs.Error("[runWriteQueue] flush by size failed: %v", err)
				}
			}
		case <-ticker.C:
			batch = manager.drainWriteQueue(batch)
			if err := flushCurrentBatch(); err != nil {
				logs.Error("[runWriteQueue] flush by ticker failed: %v", err)
			}
		case <-metricsTicker.C:
			metricsPrev = manager.logWriteQueueStats(metricsPrev, time.Since(lastMetricsAt))
			lastMetricsAt = time.Now()
		case req := <-manager.forceFlushChan:
			atomic.AddUint64(&manager.writeQueueForceFlushTotal, 1)
			batch = manager.drainWriteQueue(batch)
			err := flushCurrentBatch()
			manager.finishForceFlush(req, err)
			for {
				select {
				case req = <-manager.forceFlushChan:
					atomic.AddUint64(&manager.writeQueueForceFlushTotal, 1)
					batch = manager.drainWriteQueue(batch)
					err = flushCurrentBatch()
					manager.finishForceFlush(req, err)
				default:
					goto doneForceFlush
				}
			}
		doneForceFlush:
		}
	}
}

func (manager *Manager) runWriteQueueWatchdog() {
	defer manager.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-manager.stopChan:
			return
		case <-ticker.C:
			sinceNs := atomic.LoadUint64(&manager.writeQueueFlushInFlightSince)
			if sinceNs == 0 {
				continue
			}
			elapsed := time.Since(time.Unix(0, int64(sinceNs)))
			if elapsed < 5*time.Second {
				continue
			}
			logs.Warn("[DBQueue] flush appears stuck elapsed=%s q=%d/%d", elapsed.Truncate(time.Millisecond), len(manager.writeQueueChan), cap(manager.writeQueueChan))
		}
	}
}

// ForceFlush triggers a batch queue flush
func (manager *Manager) ForceFlush() error {
	if manager.forceFlushChan == nil {
		return nil
	}
	req := flushRequest{done: make(chan error, 1)}
	if manager.stopChan != nil {
		select {
		case manager.forceFlushChan <- req:
		case <-manager.stopChan:
			return fmt.Errorf("write queue already stopped")
		}
	} else {
		manager.forceFlushChan <- req
	}
	if manager.stopChan != nil {
		select {
		case err := <-req.done:
			return err
		case <-manager.stopChan:
			select {
			case err := <-req.done:
				return err
			default:
			}
			return fmt.Errorf("write queue stopped before flush completed")
		}
	}
	return <-req.done
}

func (manager *Manager) drainWriteQueue(batch []WriteTask) []WriteTask {
	for {
		select {
		case task := <-manager.writeQueueChan:
			atomic.AddUint64(&manager.writeQueueDequeuedTotal, 1)
			batch = append(batch, task)
		default:
			return batch
		}
	}
}

func (manager *Manager) finishForceFlush(req flushRequest, err error) {
	req.done <- err
	close(req.done)
}

func (manager *Manager) resolvePendingForceFlush(err error) {
	for {
		select {
		case req := <-manager.forceFlushChan:
			manager.finishForceFlush(req, err)
		default:
			return
		}
	}
}

// NewSession 创建一个新的数据库会话
func (m *Manager) NewSession() (interfaces.DBSession, error) {
	if m.StateDB == nil {
		return &dbSession{manager: m}, nil
	}
	stateSess, err := m.StateDB.NewSession()
	if err != nil {
		return nil, err
	}
	return &dbSession{manager: m, verkleSess: stateSess}, nil
}

func (m *Manager) CommitRoot(height uint64, root []byte) {
	if m.StateDB != nil {
		m.StateDB.CommitRoot(height, root)
	}
}

// dbSession 数据库会话实现
type dbSession struct {
	manager    *Manager
	verkleSess stateDBSession
}

func (s *dbSession) Get(key string) ([]byte, error) {
	if s.verkleSess == nil {
		return s.manager.GetKV(key)
	}
	if keys.IsStatefulKey(key) {
		val, exists, err := s.verkleSess.Get(key)
		if err == nil && exists {
			return val, nil
		}
	}
	return s.verkleSess.GetKV(key)
}

func (s *dbSession) ApplyStateUpdate(height uint64, updates []interface{}) ([]byte, error) {
	if s.verkleSess == nil {
		return nil, nil
	}
	kvUpdates := make([]stateKVUpdate, 0, len(updates))
	for _, u := range updates {
		type writeOpInterface interface {
			GetKey() string
			GetValue() []byte
			IsDel() bool
		}
		if writeOp, ok := u.(writeOpInterface); ok {
			key := writeOp.GetKey()
			if !keys.IsStatefulKey(key) {
				continue
			}
			kvUpdates = append(kvUpdates, stateKVUpdate{Key: key, Value: writeOp.GetValue(), Deleted: writeOp.IsDel()})
		}
	}
	if len(kvUpdates) > 0 {
		if err := s.verkleSess.ApplyUpdate(height, kvUpdates...); err != nil {
			return nil, err
		}
	}
	return s.verkleSess.Root(), nil
}

func (s *dbSession) Commit() error {
	if s.verkleSess == nil {
		return nil
	}
	return s.verkleSess.Commit()
}

func (s *dbSession) Rollback() error {
	if s.verkleSess == nil {
		return nil
	}
	return s.verkleSess.Rollback()
}

func (s *dbSession) Close() error {
	if s.verkleSess == nil {
		return nil
	}
	return s.verkleSess.Close()
}

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

// Scan scans all keys with the given prefix and returns a map of key-value pairs
func (manager *Manager) Scan(prefix string) (map[string][]byte, error) {
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

func extractPriceKeyFromIndexKey(indexKey string) (string, bool) {
	priceMarker := "|price:"
	orderIDMarker := "|order_id:"
	priceStart := strings.Index(indexKey, priceMarker)
	if priceStart < 0 {
		return "", false
	}
	priceStart += len(priceMarker)
	rest := indexKey[priceStart:]
	priceEndOffset := strings.Index(rest, orderIDMarker)
	if priceEndOffset < 0 {
		return "", false
	}
	return rest[:priceEndOffset], true
}

func extractOrderIDFromIndexKeyBytes(indexKey []byte) (string, bool) {
	const marker = "|order_id:"
	idx := bytes.Index(indexKey, []byte(marker))
	if idx < 0 {
		return "", false
	}
	start := idx + len(marker)
	if start >= len(indexKey) {
		return "", false
	}
	return string(indexKey[start:]), true
}

func (manager *Manager) ScanOrderPriceIndexRange(
	pair string, side pb.OrderSide, isFilled bool,
	minPriceKey67 string, maxPriceKey67 string, limit int, reverse bool,
) (map[string][]byte, error) {
	result := make(map[string][]byte)
	if minPriceKey67 == "" || maxPriceKey67 == "" || minPriceKey67 > maxPriceKey67 {
		return result, nil
	}

	prefix := keys.KeyOrderPriceIndexPrefix(pair, side, isFilled)
	prefixBytes := []byte(prefix)
	lowSeek := []byte(fmt.Sprintf("%sprice:%s|order_id:", prefix, minPriceKey67))
	highSeek := []byte(fmt.Sprintf("%sprice:%s|order_id:%c", prefix, maxPriceKey67, 0xFF))

	iter, err := manager.Db.NewIter(&pebble.IterOptions{LowerBound: prefixBytes, UpperBound: prefixUpperBound(prefixBytes)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	count := 0
	if reverse {
		for iter.SeekLT(highSeek); iter.Valid() && bytes.HasPrefix(iter.Key(), prefixBytes); iter.Prev() {
			if limit > 0 && count >= limit {
				break
			}
			k := string(iter.Key())
			priceKey, ok := extractPriceKeyFromIndexKey(k)
			if !ok {
				continue
			}
			if priceKey < minPriceKey67 {
				break
			}
			if priceKey > maxPriceKey67 {
				continue
			}
			v := make([]byte, len(iter.Value()))
			copy(v, iter.Value())
			result[k] = v
			count++
		}
	} else {
		for iter.SeekGE(lowSeek); iter.Valid() && bytes.HasPrefix(iter.Key(), prefixBytes); iter.Next() {
			if limit > 0 && count >= limit {
				break
			}
			k := string(iter.Key())
			priceKey, ok := extractPriceKeyFromIndexKey(k)
			if !ok {
				continue
			}
			if priceKey < minPriceKey67 {
				continue
			}
			if priceKey > maxPriceKey67 {
				break
			}
			v := make([]byte, len(iter.Value()))
			copy(v, iter.Value())
			result[k] = v
			count++
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return result, nil
}

// ScanOrderPriceIndexRangeOrdered 扫描订单价格索引，按底层迭代器顺序返回有序结果。
func (manager *Manager) ScanOrderPriceIndexRangeOrdered(
	pair string, side pb.OrderSide, isFilled bool,
	minPriceKey67 string, maxPriceKey67 string, limit int, reverse bool,
) ([]interfaces.OrderIndexEntry, error) {
	result := make([]interfaces.OrderIndexEntry, 0, limit)
	if minPriceKey67 == "" || maxPriceKey67 == "" || minPriceKey67 > maxPriceKey67 {
		return result, nil
	}

	prefix := keys.KeyOrderPriceIndexPrefix(pair, side, isFilled)
	prefixBytes := []byte(prefix)
	lowSeek := []byte(fmt.Sprintf("%sprice:%s|order_id:", prefix, minPriceKey67))
	highSeek := []byte(fmt.Sprintf("%sprice:%s|order_id:%c", prefix, maxPriceKey67, 0xFF))

	iter, err := manager.Db.NewIter(&pebble.IterOptions{LowerBound: prefixBytes, UpperBound: prefixUpperBound(prefixBytes)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	count := 0
	if reverse {
		for iter.SeekLT(highSeek); iter.Valid() && bytes.HasPrefix(iter.Key(), prefixBytes); iter.Prev() {
			if limit > 0 && count >= limit {
				break
			}
			keyBytes := iter.Key()
			if bytes.Compare(keyBytes, lowSeek) < 0 {
				break
			}
			orderID, ok := extractOrderIDFromIndexKeyBytes(keyBytes)
			if !ok {
				continue
			}
			val := make([]byte, len(iter.Value()))
			copy(val, iter.Value())
			result = append(result, interfaces.OrderIndexEntry{OrderID: orderID, IndexData: val})
			count++
		}
	} else {
		for iter.SeekGE(lowSeek); iter.Valid() && bytes.HasPrefix(iter.Key(), prefixBytes); iter.Next() {
			if limit > 0 && count >= limit {
				break
			}
			keyBytes := iter.Key()
			if bytes.Compare(keyBytes, highSeek) > 0 {
				break
			}
			orderID, ok := extractOrderIDFromIndexKeyBytes(keyBytes)
			if !ok {
				continue
			}
			val := make([]byte, len(iter.Value()))
			copy(val, iter.Value())
			result = append(result, interfaces.OrderIndexEntry{OrderID: orderID, IndexData: val})
			count++
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return result, nil
}

// ScanOrdersByPairs 一次性扫描多个交易对的未成交订单
func (manager *Manager) ScanOrdersByPairs(pairs []string) (map[string]map[string][]byte, error) {
	result := make(map[string]map[string][]byte)
	unfilledMarker := "|is_filled:false|"

	snap := manager.Db.NewSnapshot()
	defer snap.Close()

	for _, pair := range pairs {
		prefix := keys.KeyOrderPriceIndexGeneralPrefix(pair, false)
		p := []byte(prefix)
		pairMap := make(map[string][]byte)
		iter, err := snap.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: prefixUpperBound(p)})
		if err != nil {
			return nil, err
		}
		for iter.SeekGE(p); iter.Valid(); iter.Next() {
			k := string(iter.Key())
			if !strings.Contains(k, unfilledMarker) {
				continue
			}
			v := make([]byte, len(iter.Value()))
			copy(v, iter.Value())
			pairMap[k] = v
		}
		if err := iter.Error(); err != nil {
			iter.Close()
			return nil, err
		}
		iter.Close()
		result[pair] = pairMap
	}
	return result, nil
}

// ScanByPrefix 扫描指定前缀的所有键值对
func (manager *Manager) ScanByPrefix(prefix string, limit int) (map[string]string, error) {
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

// EnqueueDel wraps EnqueueDelete for interface compatibility
func (manager *Manager) EnqueueDel(key string) {
	manager.EnqueueDelete(key)
}

// flushBatch 把 batch 提交到 PebbleDB。Pebble Batch 无大小限制，直接提交。
func (manager *Manager) flushBatch(batch []WriteTask) error {
	if len(batch) == 0 {
		return nil
	}
	b := manager.Db.NewBatch()
	defer b.Close()
	for _, task := range batch {
		var err error
		switch task.Op {
		case OpSet:
			err = b.Set(task.Key, task.Value, nil)
		case OpDelete:
			err = b.Delete(task.Key, nil)
		}
		if err != nil {
			logs.Error("[flushBatch] set/delete error: %v", err)
			return err
		}
	}
	if err := b.Commit(pebble.Sync); err != nil {
		logs.Error("[flushBatch] commit error: %v", err)
		return err
	}
	return nil
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

// 提供"投递写请求"的方法（替换原先的 Write/WriteBatch）

func (manager *Manager) EnqueueSet(key, value string) {
	bucket := normalizeWriteQueueKeyBucket(key)
	start := time.Now()
	manager.writeQueueChan <- WriteTask{Key: []byte(key), Value: []byte(value), Op: OpSet}
	atomic.AddUint64(&manager.writeQueueEnqueueTotal, 1)
	atomic.AddUint64(&manager.writeQueueEnqueueSetTotal, 1)
	manager.addCounter(&manager.writeQueueKeyCounters, bucket)
	blocked := time.Since(start)
	if blocked > 100*time.Microsecond {
		atomic.AddUint64(&manager.writeQueueEnqueueBlockedCount, 1)
		atomic.AddUint64(&manager.writeQueueEnqueueBlockedNs, uint64(blocked))
		manager.addCounter(&manager.writeQueueBlockedKeyCounters, bucket)
	}
	manager.observeQueueDepth()
}

func (manager *Manager) EnqueueDelete(key string) {
	bucket := normalizeWriteQueueKeyBucket(key)
	start := time.Now()
	manager.writeQueueChan <- WriteTask{Key: []byte(key), Op: OpDelete}
	atomic.AddUint64(&manager.writeQueueEnqueueTotal, 1)
	atomic.AddUint64(&manager.writeQueueEnqueueDeleteTotal, 1)
	manager.addCounter(&manager.writeQueueKeyCounters, bucket)
	blocked := time.Since(start)
	if blocked > 100*time.Microsecond {
		atomic.AddUint64(&manager.writeQueueEnqueueBlockedCount, 1)
		atomic.AddUint64(&manager.writeQueueEnqueueBlockedNs, uint64(blocked))
		manager.addCounter(&manager.writeQueueBlockedKeyCounters, bucket)
	}
	manager.observeQueueDepth()
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
	if manager.StateDB != nil {
		_ = manager.StateDB.Close()
		manager.StateDB = nil
	}
	if manager.Db != nil {
		_ = manager.Db.Close()
		manager.Db = nil
	}
}

// Read 读取键对应的值
func (manager *Manager) Read(key string) (string, error) {
	if keys.IsStatefulKey(key) && manager.StateDB != nil {
		if val, exists, err := manager.StateDB.Get(key); err == nil && exists && len(val) > 0 {
			return string(val), nil
		}
	}
	manager.mu.RLock()
	db := manager.Db
	manager.mu.RUnlock()
	if db == nil {
		return "", fmt.Errorf("database is not initialized or closed")
	}
	raw, closer, err := db.Get([]byte(key))
	if err != nil {
		return "", err
	}
	val := string(raw)
	closer.Close()
	return val, nil
}

// Get 实现 vm.DBManager 接口，返回 []byte
func (manager *Manager) Get(key string) ([]byte, error) {
	if keys.IsStatefulKey(key) && manager.StateDB != nil {
		val, exists, err := manager.StateDB.Get(key)
		if err == nil && exists && len(val) > 0 {
			return val, nil
		}
	}
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
	val := make([]byte, len(raw))
	copy(val, raw)
	closer.Close()
	return val, nil
}

// GetKV 直接从 KV 读取（绕过 StateDB）
func (manager *Manager) GetKV(key string) ([]byte, error) {
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
	val := make([]byte, len(raw))
	copy(val, raw)
	closer.Close()
	return val, nil
}

func (manager *Manager) GetKVs(keys []string) (map[string][]byte, error) {
	result := make(map[string][]byte, len(keys))
	if len(keys) == 0 {
		return result, nil
	}
	manager.mu.RLock()
	db := manager.Db
	manager.mu.RUnlock()
	if db == nil {
		return nil, fmt.Errorf("database is not initialized or closed")
	}
	for _, key := range keys {
		if key == "" {
			continue
		}
		raw, closer, err := db.Get([]byte(key))
		if err != nil {
			if err == pebble.ErrNotFound {
				continue
			}
			return nil, err
		}
		val := make([]byte, len(raw))
		copy(val, raw)
		closer.Close()
		result[key] = val
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
	addr, err := m.IndexMgr.GetAddressByIndex(index)
	if err != nil {
		return nil, err
	}
	return m.GetAccount(addr)
}

// ========== 索引重建接口 ==========

func RebuildOrderPriceIndexes(m *Manager) (int, error) {
	prefix := "v1_order_"
	count := 0
	type indexEntry struct {
		key   string
		value string
	}
	var indexes []indexEntry

	p := []byte(prefix)
	iter, err := m.Db.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: prefixUpperBound(p)})
	if err != nil {
		return 0, err
	}
	for iter.SeekGE(p); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if !strings.HasPrefix(key, prefix) {
			break
		}
		orderData := make([]byte, len(iter.Value()))
		copy(orderData, iter.Value())
		var order pb.OrderTx
		if err := proto.Unmarshal(orderData, &order); err != nil {
			continue
		}
		isFilled := false
		orderStateKey := keys.KeyOrderState(order.Base.TxId)
		if stateData, err := m.Get(orderStateKey); err == nil && len(stateData) > 0 {
			var state pb.OrderState
			if err := proto.Unmarshal(stateData, &state); err == nil {
				isFilled = state.IsFilled
			}
		}
		pair := utils.GeneratePairKey(order.BaseToken, order.QuoteToken)
		priceKey67, err := PriceToKey128(order.Price)
		if err != nil {
			continue
		}
		indexKey := keys.KeyOrderPriceIndex(pair, order.Side, isFilled, priceKey67, order.Base.TxId)
		indexData, _ := proto.Marshal(&pb.OrderPriceIndex{Ok: true})
		indexes = append(indexes, indexEntry{key: indexKey, value: string(indexData)})
		count++
	}
	if err := iter.Error(); err != nil {
		iter.Close()
		return 0, err
	}
	iter.Close()

	for _, idx := range indexes {
		m.EnqueueSet(idx.key, idx.value)
	}
	if err := m.ForceFlush(); err != nil {
		return 0, err
	}
	return count, nil
}

// ========== StateDB 同步接口 ==========

func (m *Manager) SyncToStateDB(height uint64, updates []interface{}) ([]byte, error) {
	_ = height
	_ = updates
	return nil, nil
}

func (m *Manager) GetStateRoot() []byte { return nil }

// GetChannelStats 返回 DB Manager 的 channel 状态
func (m *Manager) GetChannelStats() []stats.ChannelStat {
	if m.writeQueueChan == nil {
		return nil
	}
	return []stats.ChannelStat{
		stats.NewChannelStat("writeQueueChan", "DB", len(m.writeQueueChan), cap(m.writeQueueChan)),
	}
}
