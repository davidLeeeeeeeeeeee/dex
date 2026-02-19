package db

import (
	"dex/config"
	"dex/keys"
	"dex/logs"
	statedb "dex/stateDB"
	"dex/stats"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble"
)

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
	stateUpdates := make([]statedb.KVUpdate, 0, len(batch))
	kvOps := 0
	for _, task := range batch {
		key := string(task.Key)
		if keys.IsStatefulKey(key) {
			update := statedb.KVUpdate{
				Key:     key,
				Deleted: task.Op == OpDelete,
			}
			if task.Op == OpSet {
				valCopy := make([]byte, len(task.Value))
				copy(valCopy, task.Value)
				update.Value = valCopy
			}
			stateUpdates = append(stateUpdates, update)
			continue
		}

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
		kvOps++
	}
	if kvOps > 0 {
		if err := b.Commit(pebble.Sync); err != nil {
			logs.Error("[flushBatch] commit error: %v", err)
			return err
		}
	}

	if len(stateUpdates) > 0 {
		manager.mu.RLock()
		stateStore := manager.stateDB
		manager.mu.RUnlock()
		if stateStore == nil {
			return fmt.Errorf("stateDB is not initialized")
		}
		// height=0 means direct live-state writes (bootstrap/legacy direct DB writes),
		// without creating checkpoint metadata.
		if err := stateStore.ApplyAccountUpdate(0, stateUpdates...); err != nil {
			logs.Error("[flushBatch] stateDB apply error: %v", err)
			return err
		}
	}
	return nil
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

// GetChannelStats 返回 DB Manager 的 channel 状态
func (m *Manager) GetChannelStats() []stats.ChannelStat {
	if m.writeQueueChan == nil {
		return nil
	}
	return []stats.ChannelStat{
		stats.NewChannelStat("writeQueueChan", "DB", len(m.writeQueueChan), cap(m.writeQueueChan)),
	}
}
