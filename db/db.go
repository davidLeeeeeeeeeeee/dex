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
	"errors"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dgraph-io/badger/v2"
	"github.com/dgraph-io/badger/v2/options"
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

// Manager 封装 BadgerDB 的管理器
type Manager struct {
	Db      *badger.DB
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

	// 还可以增加两个参数，用来控制"写多少/多长时间"就落库
	maxBatchSize  int                // 累计多少条就写一次
	flushInterval time.Duration      // 间隔多久强制写一次
	IndexMgr      *MinerIndexManager // 新增
	seq           *badger.Sequence   //自增发号器
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
	opts := badger.DefaultOptions(path).WithLogger(nil)
	// 应用调优参数
	opts.ValueLogFileSize = cfg.Database.ValueLogFileSize
	// Badger v2 没有独立 MemTableSize 选项，MaxTableSize 是最接近的可调参数。
	if cfg.Database.MemTableSize > 0 {
		opts.MaxTableSize = cfg.Database.MemTableSize
	} else {
		opts.MaxTableSize = cfg.Database.BaseTableSize
	}
	opts.NumMemtables = cfg.Database.NumMemtables
	// Real node path: disable compactor workers to reduce background CPU spikes.
	opts.NumCompactors = 0
	opts.BlockCacheSize = cfg.Database.BlockCacheSizeDB
	opts.IndexCacheSize = cfg.Database.IndexCacheSize
	// 使用 FileIO 模式减少 mmap 内存占用
	opts.TableLoadingMode = options.FileIO
	opts.ValueLogLoadingMode = options.FileIO
	//
	// 可选：让Badger启动时自动截断不完整的日志，能避免某些不一致问题
	// badger v2 不自动创建父目录，需要手动创建
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("failed to create db dir: %w", err)
	}
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open badger db: %w", err)
	}

	indexMgr, err := NewMinerIndexManager(db, logger)
	if err != nil {
		_ = db.Close() // 清理已打开的数据库
		return nil, fmt.Errorf("failed to create index manager: %w", err)
	}

	// ① 创建 Sequence（一次预取 1 000 个号段，可按业务量调大/调小）
	seq, err := db.GetSequence([]byte("meta:max_index"), cfg.Database.SequenceBandwidth)
	if err != nil {
		_ = db.Close() // 清理已打开的数据库
		return nil, fmt.Errorf("failed to create sequence: %w", err)
	}

	manager := &Manager{
		Db:              db,
		StateDB:         nil,
		IndexMgr:        indexMgr,
		seq:             seq,
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
	manager.writeQueueChan = make(chan WriteTask, cfg.Database.WriteQueueSize) // 缓冲区大小可酌情调大

	// 新建 forceFlushChan
	manager.forceFlushChan = make(chan flushRequest, 1)

	manager.stopChan = make(chan struct{})

	manager.wg.Add(1)
	go manager.runWriteQueue()
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

	// 用于临时收集写请求
	var batch []WriteTask
	batch = make([]WriteTask, 0, manager.maxBatchSize)

	// 定时器：到了 flushInterval 就要提交
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
		err := manager.flushBatch(batch)
		atomic.AddUint64(&manager.writeQueueFlushBatchTotal, 1)
		atomic.AddUint64(&manager.writeQueueFlushedTaskTotal, uint64(count))
		atomic.AddUint64(&manager.writeQueueFlushDurationNsTotal, uint64(time.Since(start)))
		if err != nil {
			atomic.AddUint64(&manager.writeQueueFlushErrTotal, 1)
		}
		batch = batch[:0]
		return err
	}

	for {
		select {
		case <-manager.stopChan:
			// 退出前先排空队列，再刷掉最后一批
			batch = manager.drainWriteQueue(batch)
			err := flushCurrentBatch()
			manager.resolvePendingForceFlush(err)
			return

		case task := <-manager.writeQueueChan:
			// 收到一条写请求，加入 batch
			atomic.AddUint64(&manager.writeQueueDequeuedTotal, 1)
			batch = append(batch, task)
			if len(batch) >= manager.maxBatchSize {
				// 超过阈值，立即 flush
				if err := flushCurrentBatch(); err != nil {
					logs.Error("[runWriteQueue] flush by size failed: %v", err)
				}
			}

		case <-ticker.C:
			// 定时触发时先排空当前队列积压，避免频繁小批次 flush
			batch = manager.drainWriteQueue(batch)
			// 到了时间间隔，也要 flush
			if err := flushCurrentBatch(); err != nil {
				logs.Error("[runWriteQueue] flush by ticker failed: %v", err)
			}

		case <-metricsTicker.C:
			metricsPrev = manager.logWriteQueueStats(metricsPrev, time.Since(lastMetricsAt))
			lastMetricsAt = time.Now()

		case req := <-manager.forceFlushChan:
			// 同步 flush：排空已入队写请求并等待落盘完成
			atomic.AddUint64(&manager.writeQueueForceFlushTotal, 1)
			batch = manager.drainWriteQueue(batch)
			err := flushCurrentBatch()
			manager.finishForceFlush(req, err)

			// 依次处理已排队的其他 force flush 请求，保持强一致语义
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
	return &dbSession{
		manager:    m,
		verkleSess: stateSess,
	}, nil
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

	// 对于状态数据，优先尝试从 Verkle 会话读取
	if keys.IsStatefulKey(key) {
		val, exists, err := s.verkleSess.Get(key)
		if err == nil && exists {
			return val, nil
		}
	}

	// 否则从会话底层事务读取（共享事务）
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
			kvUpdates = append(kvUpdates, stateKVUpdate{
				Key:     key,
				Value:   writeOp.GetValue(),
				Deleted: writeOp.IsDel(),
			})
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

// Scan scans all keys with the given prefix and returns a map of key-value pairs
func (manager *Manager) Scan(prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)

	err := manager.Db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		it := txn.NewIterator(opts)
		defer it.Close()

		p := []byte(prefix)
		for it.Seek(p); it.ValidForPrefix(p); it.Next() {
			item := it.Item()
			k := item.KeyCopy(nil)
			v, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			result[string(k)] = v
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ScanKVWithLimit 扫描指定前缀的所有键值对，最多返回 limit 条记录
// 返回 map[key]value，由 vm.DBManager 接口调用
func (manager *Manager) ScanKVWithLimit(prefix string, limit int) (map[string][]byte, error) {
	result := make(map[string][]byte)

	err := manager.Db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		it := txn.NewIterator(opts)
		defer it.Close()

		p := []byte(prefix)
		count := 0
		for it.Seek(p); it.ValidForPrefix(p); it.Next() {
			if limit > 0 && count >= limit {
				break
			}
			item := it.Item()
			k := item.KeyCopy(nil)
			v, err := item.ValueCopy(nil)
			if err != nil {
				continue
			}
			result[string(k)] = v
			count++
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return result, nil
}

// ScanKVWithLimitReverse 反向扫描指定前缀的所有键值对，最多返回 limit 条记录
// 适用于买盘索引（最高价排在最后，需要反向扫描以获取市场前沿）
func (manager *Manager) ScanKVWithLimitReverse(prefix string, limit int) (map[string][]byte, error) {
	result := make(map[string][]byte)

	err := manager.Db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Reverse = true
		it := txn.NewIterator(opts)
		defer it.Close()

		p := []byte(prefix)
		// 对于反向扫描，Seek 需要指向前缀范围的最末端
		// 在 Badger 中，反向迭代器 Seek(k) 会找到 <= k 的第一个键
		pEnd := make([]byte, len(p)+1)
		copy(pEnd, p)
		pEnd[len(p)] = 0xFF

		count := 0
		for it.Seek(pEnd); it.ValidForPrefix(p); it.Next() {
			if limit > 0 && count >= limit {
				break
			}
			item := it.Item()
			k := item.KeyCopy(nil)
			v, err := item.ValueCopy(nil)
			if err != nil {
				continue
			}
			result[string(k)] = v
			count++
		}
		return nil
	})

	if err != nil {
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
	pair string,
	side pb.OrderSide,
	isFilled bool,
	minPriceKey67 string,
	maxPriceKey67 string,
	limit int,
	reverse bool,
) (map[string][]byte, error) {
	result := make(map[string][]byte)
	if minPriceKey67 == "" || maxPriceKey67 == "" {
		return result, nil
	}
	if minPriceKey67 > maxPriceKey67 {
		return result, nil
	}

	prefix := keys.KeyOrderPriceIndexPrefix(pair, side, isFilled)
	prefixBytes := []byte(prefix)
	lowSeek := []byte(fmt.Sprintf("%sprice:%s|order_id:", prefix, minPriceKey67))
	highSeek := []byte(fmt.Sprintf("%sprice:%s|order_id:%c", prefix, maxPriceKey67, 0xFF))

	err := manager.Db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Reverse = reverse
		it := txn.NewIterator(opts)
		defer it.Close()

		if reverse {
			it.Seek(highSeek)
		} else {
			it.Seek(lowSeek)
		}

		count := 0
		for ; it.ValidForPrefix(prefixBytes); it.Next() {
			if limit > 0 && count >= limit {
				break
			}

			item := it.Item()
			k := string(item.KeyCopy(nil))
			priceKey, ok := extractPriceKeyFromIndexKey(k)
			if !ok {
				continue
			}

			if priceKey < minPriceKey67 {
				if reverse {
					break
				}
				continue
			}
			if priceKey > maxPriceKey67 {
				if !reverse {
					break
				}
				continue
			}

			v, err := item.ValueCopy(nil)
			if err != nil {
				continue
			}
			result[k] = v
			count++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ScanOrderPriceIndexRangeOrdered 扫描订单价格索引，按底层迭代器顺序返回有序结果。
// 该接口避免 map + sort 的额外分配，提供确定性顺序供 VM 直接消费。
func (manager *Manager) ScanOrderPriceIndexRangeOrdered(
	pair string,
	side pb.OrderSide,
	isFilled bool,
	minPriceKey67 string,
	maxPriceKey67 string,
	limit int,
	reverse bool,
) ([]interfaces.OrderIndexEntry, error) {
	result := make([]interfaces.OrderIndexEntry, 0, limit)
	if minPriceKey67 == "" || maxPriceKey67 == "" {
		return result, nil
	}
	if minPriceKey67 > maxPriceKey67 {
		return result, nil
	}

	prefix := keys.KeyOrderPriceIndexPrefix(pair, side, isFilled)
	prefixBytes := []byte(prefix)
	lowSeek := []byte(fmt.Sprintf("%sprice:%s|order_id:", prefix, minPriceKey67))
	highSeek := []byte(fmt.Sprintf("%sprice:%s|order_id:%c", prefix, maxPriceKey67, 0xFF))

	err := manager.Db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Reverse = reverse
		it := txn.NewIterator(opts)
		defer it.Close()

		if reverse {
			it.Seek(highSeek)
		} else {
			it.Seek(lowSeek)
		}

		count := 0
		for ; it.ValidForPrefix(prefixBytes); it.Next() {
			if limit > 0 && count >= limit {
				break
			}

			item := it.Item()
			keyBytes := item.Key()

			// Key 格式是固定的，使用低/高边界直接裁剪，避免逐条解析 price 字段。
			if reverse {
				if bytes.Compare(keyBytes, lowSeek) < 0 {
					break
				}
			} else {
				if bytes.Compare(keyBytes, highSeek) > 0 {
					break
				}
			}

			orderID, ok := extractOrderIDFromIndexKeyBytes(keyBytes)
			if !ok {
				continue
			}

			val, err := item.ValueCopy(nil)
			if err != nil {
				continue
			}

			result = append(result, interfaces.OrderIndexEntry{
				OrderID:   orderID,
				IndexData: val,
			})
			count++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// ScanOrdersByPairs 一次性扫描多个交易对的未成交订单
// 返回：map[pair]map[indexKey][]byte
func (manager *Manager) ScanOrdersByPairs(pairs []string) (map[string]map[string][]byte, error) {
	result := make(map[string]map[string][]byte)
	unfilledMarker := "|is_filled:false|"

	// 单个 txn 内完成所有扫描
	err := manager.Db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions

		for _, pair := range pairs {
			// 生成该交易对的未成交订单索引通用前缀 (不分 Side)
			prefix := keys.KeyOrderPriceIndexGeneralPrefix(pair, false)
			p := []byte(prefix)

			pairMap := make(map[string][]byte)
			it := txn.NewIterator(opts)

			for it.Seek(p); it.ValidForPrefix(p); it.Next() {
				item := it.Item()
				k := item.KeyCopy(nil)
				if !strings.Contains(string(k), unfilledMarker) {
					continue
				}
				v, err := item.ValueCopy(nil)
				if err != nil {
					it.Close()
					return err
				}
				pairMap[string(k)] = v
			}
			it.Close()

			result[pair] = pairMap
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return result, nil
}

// ScanByPrefix 扫描指定前缀的所有键值对
// 返回 map[key]value，最多返回 limit 条记录
func (manager *Manager) ScanByPrefix(prefix string, limit int) (map[string]string, error) {
	result := make(map[string]string)

	err := manager.Db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		it := txn.NewIterator(opts)
		defer it.Close()

		p := []byte(prefix)
		count := 0
		for it.Seek(p); it.ValidForPrefix(p); it.Next() {
			if limit > 0 && count >= limit {
				break
			}
			item := it.Item()
			k := item.KeyCopy(nil)
			v, err := item.ValueCopy(nil)
			if err != nil {
				continue
			}
			result[string(k)] = string(v)
			count++
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return result, nil
}

// EnqueueDel wraps EnqueueDelete for interface compatibility
func (manager *Manager) EnqueueDel(key string) {
	manager.EnqueueDelete(key)
}

// 这里 flushBatch 会把 batch 分段后提交到 BadgerDB。
func (manager *Manager) flushBatch(batch []WriteTask) error {
	if len(batch) == 0 {
		return nil
	}
	cfg := manager.cfg
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	// 保守软上限，留出 Badger 元数据开销余量
	softLimitBytes := cfg.Database.WriteBatchSoftLimit // 8 MiB
	maxCountPerTxn := cfg.Database.MaxCountPerTxn      // 也保留条数上限，双重保险
	perEntryOverhead := cfg.Database.PerEntryOverhead  // 估算每条附加开销

	// 1) 先按“字节+条数”把batch切成若干 sub-batch
	type sliceRange struct{ i, j int }
	subRanges := make([]sliceRange, 0, (len(batch)+maxCountPerTxn-1)/maxCountPerTxn)

	curStart, curBytes, curCount := 0, 0, 0
	for idx, t := range batch {
		entryBytes := len(t.Key) + len(t.Value) + perEntryOverhead
		// 如果加上当前条会超过限制，就先封口开新段
		if curCount > 0 && (int64(curBytes+entryBytes) > softLimitBytes || curCount >= maxCountPerTxn) {
			subRanges = append(subRanges, sliceRange{curStart, idx})
			curStart, curBytes, curCount = idx, 0, 0
		}
		curBytes += entryBytes
		curCount++
	}
	// 收尾
	if curStart < len(batch) {
		subRanges = append(subRanges, sliceRange{curStart, len(batch)})
	}

	var firstErr error

	// 2) 提交每个 sub-batch；若仍报过大，二分退让
	for _, r := range subRanges {
		if err := manager.flushRangeWithSplit(batch, r.i, r.j); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

func (manager *Manager) flushRangeWithSplit(batch []WriteTask, start, end int) error {
	type sliceRange struct{ i, j int }

	stack := []sliceRange{{i: start, j: end}}
	var firstErr error

	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if cur.i >= cur.j {
			continue
		}

		ok, err := manager.tryFlushRange(batch, cur.i, cur.j)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if ok {
			continue
		}

		if cur.j-cur.i <= 1 {
			continue
		}

		mid := cur.i + (cur.j-cur.i)/2
		stack = append(stack, sliceRange{i: mid, j: cur.j}, sliceRange{i: cur.i, j: mid})
	}

	return firstErr
}

// 返回是否提交成功；若返回 false，调用方应继续拆分范围重试。
func (manager *Manager) tryFlushRange(batch []WriteTask, start, end int) (bool, error) {
	if start >= end {
		return true, nil
	}
	sub := batch[start:end]

	wb := manager.Db.NewWriteBatch()
	defer wb.Cancel()

	for _, task := range sub {
		var err error
		switch task.Op {
		case OpSet:
			err = wb.Set(task.Key, task.Value)
		case OpDelete:
			err = wb.Delete(task.Key)
		}
		if err != nil {
			// ErrTxnTooBig 时交给外层继续切分
			if errors.Is(err, badger.ErrTxnTooBig) || strings.Contains(err.Error(), "Txn is too big") {
				if end-start == 1 {
					key := string(sub[0].Key)
					valSz := len(sub[0].Value)
					msg := fmt.Errorf("single entry too big for badger: key=%q size=%d bytes", key, valSz)
					manager.Logger.Error("[flushBatch] %v; consider compressing, chunking, or storing out-of-DB", msg)
					return true, msg
				}
				return false, nil
			}
			logs.Error("[flushBatch] subBatch [%d:%d] set/delete error: %v", start, end, err)
			return true, err
		}
	}

	err := wb.Flush()
	if err == nil {
		return true, nil
	}

	// Badger 的典型报错文案里包含 "Txn is too big"
	if errors.Is(err, badger.ErrTxnTooBig) || strings.Contains(err.Error(), "Txn is too big") {
		if end-start == 1 {
			// 单条仍过大：给出清晰提示
			key := string(sub[0].Key)
			valSz := len(sub[0].Value)
			msg := fmt.Errorf("single entry still too big: key=%q size=%d bytes", key, valSz)
			manager.Logger.Error("[flushBatch] %v; consider compressing, chunking, or storing out-of-DB", msg)
			return true, msg
		}
		// 交给上层继续二分
		return false, nil
	}

	// 其他错误：记录并继续
	logs.Error("[flushBatch] subBatch [%d:%d] error: %v", start, end, err)
	return true, err // 避免卡死：把它当“已处理”，不中断后续
}

func (manager *Manager) View(fn func(txn *TransactionView) error) error {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.Db.View(func(badgerTxn *badger.Txn) error {
		return fn(&TransactionView{badgerTxn})
	})
}

// TransactionView 包装 badger.Txn
type TransactionView struct {
	Txn *badger.Txn
}

func (tv *TransactionView) NewIterator() *badger.Iterator {
	return tv.Txn.NewIterator(badger.DefaultIteratorOptions)
}

// 提供"投递写请求"的方法（替换原先的 Write/WriteBatch）

func (manager *Manager) EnqueueSet(key, value string) {
	start := time.Now()
	manager.writeQueueChan <- WriteTask{
		Key:   []byte(key),
		Value: []byte(value),
		Op:    OpSet,
	}
	atomic.AddUint64(&manager.writeQueueEnqueueTotal, 1)
	atomic.AddUint64(&manager.writeQueueEnqueueSetTotal, 1)
	blocked := time.Since(start)
	if blocked > 100*time.Microsecond {
		atomic.AddUint64(&manager.writeQueueEnqueueBlockedCount, 1)
		atomic.AddUint64(&manager.writeQueueEnqueueBlockedNs, uint64(blocked))
	}
	manager.observeQueueDepth()
}

func (manager *Manager) EnqueueDelete(key string) {
	start := time.Now()
	manager.writeQueueChan <- WriteTask{
		Key: []byte(key),
		Op:  OpDelete,
	}
	atomic.AddUint64(&manager.writeQueueEnqueueTotal, 1)
	atomic.AddUint64(&manager.writeQueueEnqueueDeleteTotal, 1)
	blocked := time.Since(start)
	if blocked > 100*time.Microsecond {
		atomic.AddUint64(&manager.writeQueueEnqueueBlockedCount, 1)
		atomic.AddUint64(&manager.writeQueueEnqueueBlockedNs, uint64(blocked))
	}
	manager.observeQueueDepth()
}

func (manager *Manager) Close() {
	// 1. 先做一次同步 flush，确保已经入队的写请求全部落盘
	if err := manager.ForceFlush(); err != nil {
		logs.Error("[db.Close] force flush failed: %v", err)
	}

	// 2. 通知写队列 goroutine 停止
	if manager.stopChan != nil {
		select {
		case <-manager.stopChan:
			// already closed
		default:
			close(manager.stopChan)
		}
	}

	// 3. 等待 goroutine 退出
	manager.wg.Wait()
	manager.stopChan = nil
	manager.forceFlushChan = nil

	// 4. 这时所有队列里的数据都已经flush完了，可以安全关闭DB
	manager.mu.Lock()
	defer manager.mu.Unlock()

	if manager.seq != nil {
		_ = manager.seq.Release() // 无须处理返回值；Close() 时 Badger 仍会安全落盘
		manager.seq = nil
	}

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
// 对于状态数据（账户、订单状态、Token、Witness 等）优先从 StateDB 读取
// 对于流水数据（区块、交易原文、历史记录）直接从 KV 读取
func (manager *Manager) Read(key string) (string, error) {
	// 1. 对于状态数据，优先尝试从 StateDB 读取
	if keys.IsStatefulKey(key) && manager.StateDB != nil {
		if val, exists, err := manager.StateDB.Get(key); err == nil && exists && len(val) > 0 {
			return string(val), nil
		}
		// StateDB 没有找到，继续回退到 KV
	}

	// 2. 从 KV 读取
	manager.mu.RLock()
	db := manager.Db
	manager.mu.RUnlock()

	if db == nil {
		return "", fmt.Errorf("database is not initialized or closed")
	}

	var value string
	err := db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		val, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		value = string(val)
		return nil
	})
	if err != nil {
		return "", err
	}
	return value, nil
}

// Get 实现 vm.DBManager 接口，返回 []byte
// 对于状态数据（账户、订单状态、Token、Witness 等）优先从 StateDB 读取
// 对于流水数据（区块、交易原文、历史记录）直接从 KV 读取
func (manager *Manager) Get(key string) ([]byte, error) {
	// 1. 对于状态数据，优先尝试从 StateDB 读取
	if keys.IsStatefulKey(key) && manager.StateDB != nil {
		val, exists, err := manager.StateDB.Get(key)
		if err == nil && exists && len(val) > 0 {
			return val, nil
		}
		// StateDB 没有找到，继续回退到 KV
	}

	// 2. 从 KV 读取
	manager.mu.RLock()
	db := manager.Db
	manager.mu.RUnlock()

	if db == nil {
		return nil, fmt.Errorf("database is not initialized or closed")
	}

	var value []byte
	err := db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		val, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		value = val
		return nil
	})
	if err != nil {
		return nil, err
	}
	return value, nil
}

// GetKV 直接从 KV 读取（绕过 StateDB）
func (manager *Manager) GetKV(key string) ([]byte, error) {
	manager.mu.RLock()
	db := manager.Db
	manager.mu.RUnlock()

	if db == nil {
		return nil, fmt.Errorf("database is not initialized or closed")
	}

	var value []byte
	err := db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		val, err := item.ValueCopy(nil)
		if err != nil {
			return err
		}
		value = val
		return nil
	})
	if err != nil {
		return nil, err
	}
	return value, nil
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

	err := db.View(func(txn *badger.Txn) error {
		for _, key := range keys {
			if key == "" {
				continue
			}
			item, err := txn.Get([]byte(key))
			if err != nil {
				if err == badger.ErrKeyNotFound {
					continue
				}
				return err
			}
			val, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			result[key] = val
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// 将 db.Transaction 序列化为 [][]byte
func SerializeAllTransactions(txCopy []*pb.AnyTx) [][]byte {

	// 1) 按 TxId 排序
	sort.Slice(txCopy, func(i, j int) bool {
		return txCopy[i].GetTxId() < txCopy[j].GetTxId()
	})

	// 2) 逐笔序列化
	var result [][]byte
	for _, tx := range txCopy {
		result = append(result, serializeTransaction(tx))
	}
	return result
}

// 根据 db.Transaction 的字段进行序列化
// 实际中应根据业务需求将交易变成字节切片，这里只作简单示例
func serializeTransaction(tx *pb.AnyTx) []byte {
	data := []byte(tx.GetTxId() + "|" + tx.GetBase().FromAddress)
	// 可以增加更多字段序列化逻辑
	return data
}

// NextIndex 获取下一个自增索引
func (m *Manager) NextIndex() (uint64, error) {
	id, err := m.seq.Next() // Badger 自动并发安全
	if err != nil {
		return 0, err
	}
	return id + 1, nil // 让索引依旧 from 1 开始
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

//	从订单数据重建价格索引
//
// 用于轻节点同步后重建索引，提升查询性能
// 返回重建的索引数量
func RebuildOrderPriceIndexes(m *Manager) (int, error) {
	// 使用 withVer 获取正确的前缀
	prefix := "v1_order_"
	count := 0

	// 先收集所有需要写入的索引，避免在 View 事务中调用 EnqueueSet
	type indexEntry struct {
		key   string
		value string
	}
	var indexes []indexEntry

	err := m.Db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		it := txn.NewIterator(opts)
		defer it.Close()

		p := []byte(prefix)
		for it.Seek(p); it.ValidForPrefix(p); it.Next() {
			item := it.Item()
			key := string(item.Key())

			// 确保 key 以前缀开头
			if !strings.HasPrefix(key, prefix) {
				break
			}

			orderData, err := item.ValueCopy(nil)
			if err != nil {
				continue
			}

			// 反序列化订单
			var order pb.OrderTx
			if err := proto.Unmarshal(orderData, &order); err != nil {
				continue
			}

			// 尝试从 OrderState 获取最新的 is_filled 状态
			isFilled := false
			orderStateKey := keys.KeyOrderState(order.Base.TxId)
			if stateData, err := m.Get(orderStateKey); err == nil && len(stateData) > 0 {
				var state pb.OrderState
				if err := proto.Unmarshal(stateData, &state); err == nil {
					isFilled = state.IsFilled
				}
			}

			// 重建价格索引
			pair := utils.GeneratePairKey(order.BaseToken, order.QuoteToken)
			priceKey67, err := PriceToKey128(order.Price)
			if err != nil {
				continue
			}

			indexKey := keys.KeyOrderPriceIndex(pair, order.Side, isFilled, priceKey67, order.Base.TxId)
			indexData, _ := proto.Marshal(&pb.OrderPriceIndex{Ok: true})

			indexes = append(indexes, indexEntry{
				key:   indexKey,
				value: string(indexData),
			})
			count++
		}
		return nil
	})

	if err != nil {
		return 0, err
	}

	// 写入所有索引
	for _, idx := range indexes {
		m.EnqueueSet(idx.key, idx.value)
	}

	// 强制刷盘
	if err := m.ForceFlush(); err != nil {
		return 0, err
	}

	return count, nil
}

// ========== StateDB 同步接口 ==========

// SyncToStateDB 同步状态变化到 StateDB
// 这是 VM 的 applyResult 调用的接口，用于将 WriteOp 同步到 StateDB
// 注意：只有被 keys.IsStatefulKey 判定为状态数据的 key 才会被同步
// 返回值：(stateRoot, error) - stateRoot 是同步后的状态树根哈希
func (m *Manager) SyncToStateDB(height uint64, updates []interface{}) ([]byte, error) {
	_ = height
	_ = updates
	return nil, nil
}

// GetStateRoot 获取当前状态树根哈希
func (m *Manager) GetStateRoot() []byte {
	return nil
}

// GetChannelStats 返回 DB Manager 的 channel 状态
func (m *Manager) GetChannelStats() []stats.ChannelStat {
	if m.writeQueueChan == nil {
		return nil
	}
	return []stats.ChannelStat{
		stats.NewChannelStat("writeQueueChan", "DB", len(m.writeQueueChan), cap(m.writeQueueChan)),
	}
}
