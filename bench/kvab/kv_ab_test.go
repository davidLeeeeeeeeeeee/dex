package kvab

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/dgraph-io/badger/v2"
	badgerOpts "github.com/dgraph-io/badger/v2/options"
)

// BenchmarkKVAB_OrderIndexReadHeavy compares Badger and Pebble in one run
// using one order-index style workload:
// 1) Seed a dataset with order-index keys.
// 2) Run 80% point gets + 20% prefix scans.
// 3) Sample process RSS and report rss_peak_mb.
//
// Example:
// go test ./bench/kvab -run '^$' -bench BenchmarkKVAB_OrderIndexReadHeavy -benchmem -benchtime=5s
//
// Optional env overrides:
// KVAB_KEYS=120000 KVAB_VALUE_SIZE=128 KVAB_SCAN_LIMIT=64 KVAB_PAIRS=8 KVAB_RSS_SAMPLE_MS=200
func BenchmarkKVAB_OrderIndexReadHeavy(b *testing.B) {
	b.Run("badger", func(b *testing.B) {
		runOrderIndexReadHeavyBenchmark(b, "badger", openBadgerBackend)
	})
	b.Run("pebble", func(b *testing.B) {
		runOrderIndexReadHeavyBenchmark(b, "pebble", openPebbleBackend)
	})
}

type workloadConfig struct {
	KeyCount    int
	ValueSize   int
	ScanLimit   int
	PairCount   int
	RSSSampleMs int
}

func loadConfigFromEnv() workloadConfig {
	return workloadConfig{
		KeyCount:    envInt("KVAB_KEYS", 120_000),
		ValueSize:   envInt("KVAB_VALUE_SIZE", 128),
		ScanLimit:   envInt("KVAB_SCAN_LIMIT", 64),
		PairCount:   envInt("KVAB_PAIRS", 8),
		RSSSampleMs: envInt("KVAB_RSS_SAMPLE_MS", 200),
	}
}

func envInt(name string, def int) int {
	v, ok := os.LookupEnv(name)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// Run one backend in an isolated benchmark invocation to compare peak RSS
// without cross-backend contamination:
// go test ./bench/kvab -run '^$' -bench '^BenchmarkKVAB_OrderIndexReadHeavy_Badger$' -benchmem -benchtime=5s
func BenchmarkKVAB_OrderIndexReadHeavy_Badger(b *testing.B) {
	runOrderIndexReadHeavyBenchmark(b, "badger", openBadgerBackend)
}

// Run one backend in an isolated benchmark invocation to compare peak RSS
// without cross-backend contamination:
// go test ./bench/kvab -run '^$' -bench '^BenchmarkKVAB_OrderIndexReadHeavy_Pebble$' -benchmem -benchtime=5s
func BenchmarkKVAB_OrderIndexReadHeavy_Pebble(b *testing.B) {
	runOrderIndexReadHeavyBenchmark(b, "pebble", openPebbleBackend)
}

func runOrderIndexReadHeavyBenchmark(b *testing.B, backendName string, open func(path string) (kvBackend, error)) {
	cfg := loadConfigFromEnv()
	keys, scanPrefixes := buildOrderIndexDataset(cfg)
	value := bytes.Repeat([]byte{'v'}, cfg.ValueSize)

	dbPath := filepath.Join(b.TempDir(), backendName)
	backend, err := open(dbPath)
	if err != nil {
		b.Fatalf("open %s: %v", backendName, err)
	}
	defer func() {
		if err := backend.Close(); err != nil {
			b.Fatalf("close %s: %v", backendName, err)
		}
	}()

	var sampler *rssSampler
	if cfg.RSSSampleMs > 0 {
		sampler = newRSSSampler(time.Duration(cfg.RSSSampleMs) * time.Millisecond)
		sampler.Start()
	}

	seedStart := time.Now()
	if err := backend.Seed(keys, value); err != nil {
		b.Fatalf("seed %s: %v", backendName, err)
	}
	seedElapsed := time.Since(seedStart)
	if seedElapsed > 0 {
		b.ReportMetric(float64(len(keys))/seedElapsed.Seconds(), "seed_kv/s")
	}

	rng := rand.New(rand.NewSource(42))
	getOps := 0
	scanOps := 0
	totalScanned := 0

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i%5 == 0 {
			prefix := scanPrefixes[rng.Intn(len(scanPrefixes))]
			n, err := backend.ScanPrefix(prefix, cfg.ScanLimit)
			if err != nil {
				b.Fatalf("scan: %v", err)
			}
			totalScanned += n
			scanOps++
			continue
		}

		key := keys[rng.Intn(len(keys))]
		if _, err := backend.Get(key); err != nil {
			b.Fatalf("get: %v", err)
		}
		getOps++
	}
	b.StopTimer()

	if scanOps > 0 {
		b.ReportMetric(float64(totalScanned)/float64(scanOps), "scan_keys/op")
	}
	b.ReportMetric(float64(getOps), "get_ops")
	b.ReportMetric(float64(scanOps), "scan_ops")

	if sampler != nil {
		peak := sampler.Stop()
		if peak > 0 {
			b.ReportMetric(float64(peak)/1024.0/1024.0, "rss_peak_mb")
		}
	}
}

func buildOrderIndexDataset(cfg workloadConfig) ([][]byte, [][]byte) {
	if cfg.PairCount < 1 {
		cfg.PairCount = 1
	}

	pairs := make([]string, 0, cfg.PairCount)
	for i := 0; i < cfg.PairCount; i++ {
		pairs = append(pairs, fmt.Sprintf("PAIR_%02d", i))
	}
	sides := []string{"BUY", "SELL"}

	keys := make([][]byte, 0, cfg.KeyCount)
	prefixSet := make(map[string]struct{}, cfg.PairCount*len(sides))

	for i := 0; i < cfg.KeyCount; i++ {
		pair := pairs[i%len(pairs)]
		side := sides[(i/len(pairs))%len(sides)]
		priceBucket := (i * 17) % 10000
		key := fmt.Sprintf("idx/order/%s/%s/%05d/%08d", pair, side, priceBucket, i)
		keys = append(keys, []byte(key))
		prefixSet[fmt.Sprintf("idx/order/%s/%s/", pair, side)] = struct{}{}
	}

	prefixes := make([][]byte, 0, len(prefixSet))
	for p := range prefixSet {
		prefixes = append(prefixes, []byte(p))
	}
	return keys, prefixes
}

type kvBackend interface {
	Seed(keys [][]byte, value []byte) error
	Put(key, value []byte) error
	Get(key []byte) ([]byte, error)
	ScanPrefix(prefix []byte, limit int) (int, error)
	Close() error
}

type badgerBackend struct {
	db *badger.DB
}

func openBadgerBackend(path string) (kvBackend, error) {
	opts := badger.DefaultOptions(path).WithLogger(nil)
	opts.SyncWrites = false
	opts.TableLoadingMode = badgerOpts.FileIO
	opts.ValueLogLoadingMode = badgerOpts.FileIO
	opts.BlockCacheSize = 32 << 20
	opts.IndexCacheSize = 16 << 20

	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}
	return &badgerBackend{db: db}, nil
}

func (b *badgerBackend) Seed(keys [][]byte, value []byte) error {
	wb := b.db.NewWriteBatch()
	defer wb.Cancel()
	for _, k := range keys {
		if err := wb.Set(k, value); err != nil {
			return err
		}
	}
	return wb.Flush()
}

func (b *badgerBackend) Put(key, value []byte) error {
	return b.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, value)
	})
}

func (b *badgerBackend) Get(key []byte) ([]byte, error) {
	txn := b.db.NewTransaction(false)
	defer txn.Discard()

	item, err := txn.Get(key)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return item.ValueCopy(nil)
}

func (b *badgerBackend) ScanPrefix(prefix []byte, limit int) (int, error) {
	txn := b.db.NewTransaction(false)
	defer txn.Discard()

	opts := badger.DefaultIteratorOptions
	opts.PrefetchValues = false
	it := txn.NewIterator(opts)
	defer it.Close()

	n := 0
	for it.Seek(prefix); it.ValidForPrefix(prefix) && n < limit; it.Next() {
		n++
	}
	return n, nil
}

func (b *badgerBackend) Close() error {
	return b.db.Close()
}

type pebbleBackend struct {
	db    *pebble.DB
	cache *pebble.Cache
}

func openPebbleBackend(path string) (kvBackend, error) {
	cache := pebble.NewCache(64 << 20)
	db, err := pebble.Open(path, &pebble.Options{
		Cache:        cache,
		MaxOpenFiles: 256,
	})
	if err != nil {
		cache.Unref()
		return nil, err
	}
	return &pebbleBackend{db: db, cache: cache}, nil
}

func (p *pebbleBackend) Seed(keys [][]byte, value []byte) error {
	const batchSize = 2000
	batch := p.db.NewBatch()

	for i, k := range keys {
		if err := batch.Set(k, value, pebble.NoSync); err != nil {
			_ = batch.Close()
			return err
		}
		if (i+1)%batchSize == 0 {
			if err := batch.Commit(pebble.NoSync); err != nil {
				_ = batch.Close()
				return err
			}
			if err := batch.Close(); err != nil {
				return err
			}
			batch = p.db.NewBatch()
		}
	}

	if err := batch.Commit(pebble.NoSync); err != nil {
		_ = batch.Close()
		return err
	}
	if err := batch.Close(); err != nil {
		return err
	}
	return p.db.Flush()
}

func (p *pebbleBackend) Put(key, value []byte) error {
	return p.db.Set(key, value, pebble.NoSync)
}

func (p *pebbleBackend) Get(key []byte) ([]byte, error) {
	v, closer, err := p.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

func (p *pebbleBackend) ScanPrefix(prefix []byte, limit int) (int, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixUpperBound(prefix),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	n := 0
	for ok := iter.First(); ok && n < limit; ok = iter.Next() {
		n++
	}
	return n, iter.Error()
}

func (p *pebbleBackend) Close() error {
	err := p.db.Close()
	p.cache.Unref()
	return err
}

func prefixUpperBound(prefix []byte) []byte {
	if len(prefix) == 0 {
		return nil
	}
	ub := make([]byte, len(prefix))
	copy(ub, prefix)
	for i := len(ub) - 1; i >= 0; i-- {
		if ub[i] < 0xFF {
			ub[i]++
			return ub[:i+1]
		}
	}
	return nil
}

type rssSampler struct {
	interval time.Duration
	stopCh   chan struct{}
	doneCh   chan struct{}
	once     sync.Once
	peak     atomic.Uint64
}

func newRSSSampler(interval time.Duration) *rssSampler {
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}
	return &rssSampler{
		interval: interval,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

func (s *rssSampler) Start() {
	if rss, err := currentRSSBytes(); err == nil {
		s.peak.Store(rss)
	}

	go func() {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		defer close(s.doneCh)

		for {
			select {
			case <-ticker.C:
				if rss, err := currentRSSBytes(); err == nil {
					s.observe(rss)
				}
			case <-s.stopCh:
				if rss, err := currentRSSBytes(); err == nil {
					s.observe(rss)
				}
				return
			}
		}
	}()
}

func (s *rssSampler) Stop() uint64 {
	s.once.Do(func() {
		close(s.stopCh)
		<-s.doneCh
	})
	return s.peak.Load()
}

func (s *rssSampler) observe(rss uint64) {
	for {
		cur := s.peak.Load()
		if rss <= cur {
			return
		}
		if s.peak.CompareAndSwap(cur, rss) {
			return
		}
	}
}

func currentRSSBytes() (uint64, error) {
	pid := strconv.Itoa(os.Getpid())
	out, err := exec.Command("ps", "-o", "rss=", "-p", pid).Output()
	if err != nil {
		return 0, err
	}

	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty rss output")
	}
	// `ps rss` is KiB on macOS and Linux.
	rssKB, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, err
	}
	return rssKB * 1024, nil
}

// TestKVAB_BadgerWrite100MBEvery5sFor3m writes 100MiB to Badger every 5 seconds
// for 3 minutes (36 rounds by default).
//
// Default behavior is skip; enable explicitly:
// KVAB_RUN_BADGER_WRITE_100MB_5S_3M=1 go test ./bench/kvab -run '^TestKVAB_BadgerWrite100MBEvery5sFor3m$' -v -count=1 -timeout 30m
func TestKVAB_BadgerWrite100MBEvery5sFor3m(t *testing.T) {
	if os.Getenv("KVAB_RUN_BADGER_WRITE_100MB_5S_3M") != "1" {
		t.Skip("set KVAB_RUN_BADGER_WRITE_100MB_5S_3M=1 to run badger periodic write test")
	}

	const (
		defaultBytesPerRound = 100 * 1024 * 1024
		defaultIntervalSec   = 1
		defaultDurationSec   = 180
		defaultValueSize     = 1 * 1024 * 1024
	)

	bytesPerRound := envInt("KVAB_BADGER_WRITE_BYTES_PER_ROUND", defaultBytesPerRound)
	intervalSec := envInt("KVAB_BADGER_WRITE_INTERVAL_SEC", defaultIntervalSec)
	durationSec := envInt("KVAB_BADGER_WRITE_DURATION_SEC", defaultDurationSec)
	valueSize := envInt("KVAB_BADGER_WRITE_VALUE_SIZE", defaultValueSize)

	if bytesPerRound <= 0 || intervalSec <= 0 || durationSec <= 0 || valueSize <= 0 {
		t.Fatalf(
			"invalid config: bytes_per_round=%d interval_sec=%d duration_sec=%d value_size=%d",
			bytesPerRound, intervalSec, durationSec, valueSize,
		)
	}

	rounds := durationSec / intervalSec
	if durationSec%intervalSec != 0 {
		rounds++
	}
	if rounds <= 0 {
		t.Fatalf("invalid rounds=%d from duration=%ds interval=%ds", rounds, durationSec, intervalSec)
	}

	backend, err := openBadgerBackend(filepath.Join(t.TempDir(), "badger"))
	if err != nil {
		t.Fatalf("open badger: %v", err)
	}
	defer func() {
		if err := backend.Close(); err != nil {
			t.Fatalf("close badger: %v", err)
		}
	}()

	bdb, ok := backend.(*badgerBackend)
	if !ok {
		t.Fatalf("unexpected backend type %T", backend)
	}

	fullValues := bytesPerRound / valueSize
	tailBytes := bytesPerRound % valueSize
	if fullValues == 0 && tailBytes == 0 {
		t.Fatalf("invalid write size config: bytes_per_round=%d value_size=%d", bytesPerRound, valueSize)
	}

	value := bytes.Repeat([]byte{'w'}, valueSize)
	interval := time.Duration(intervalSec) * time.Second
	start := time.Now()
	var totalBytes int64

	t.Logf(
		"badger write config: rounds=%d bytes_per_round=%.1fMB interval=%v duration=%ds value_size=%dB",
		rounds,
		float64(bytesPerRound)/1024.0/1024.0,
		interval,
		durationSec,
		valueSize,
	)

	for round := 0; round < rounds; round++ {
		roundStart := time.Now()
		wb := bdb.db.NewWriteBatch()

		err := func() error {
			defer wb.Cancel()

			for i := 0; i < fullValues; i++ {
				key := []byte(fmt.Sprintf("kvab/badger-write/%06d/%06d", round, i))
				if err := wb.Set(key, value); err != nil {
					return err
				}
			}
			if tailBytes > 0 {
				key := []byte(fmt.Sprintf("kvab/badger-write/%06d/tail", round))
				if err := wb.Set(key, value[:tailBytes]); err != nil {
					return err
				}
			}
			return wb.Flush()
		}()
		if err != nil {
			t.Fatalf("round %d write: %v", round+1, err)
		}

		wrote := int64(fullValues*valueSize + tailBytes)
		totalBytes += wrote
		elapsed := time.Since(roundStart)

		t.Logf(
			"round=%d/%d wrote=%.1fMB elapsed=%v total=%.1fGB",
			round+1,
			rounds,
			float64(wrote)/1024.0/1024.0,
			elapsed,
			float64(totalBytes)/1024.0/1024.0/1024.0,
		)

		sleepFor := interval - elapsed
		if sleepFor > 0 {
			time.Sleep(sleepFor)
			continue
		}
		t.Logf("round=%d exceeded interval by %v", round+1, -sleepFor)
	}

	expectedTotal := int64(rounds) * int64(bytesPerRound)
	if totalBytes != expectedTotal {
		t.Fatalf("unexpected total bytes: got=%d want=%d", totalBytes, expectedTotal)
	}

	t.Logf(
		"badger periodic write done: rounds=%d total=%.1fGB elapsed=%v",
		rounds,
		float64(totalBytes)/1024.0/1024.0/1024.0,
		time.Since(start),
	)
}

type stopWriteConfig struct {
	Backend         string
	KeyCount        int
	ValueSize       int
	PairCount       int
	ScanLimit       int
	WarmWriteSec    int
	StopWriteSec    int
	SampleMs        int
	ReadOpsPerTick  int
	ProgressEveryS  int
	ForceGCAtStart  bool
	ForceGCAtFinish bool
}

type memSample struct {
	elapsedSec  float64
	rssBytes    uint64
	heapAlloc   uint64
	heapInuse   uint64
	heapObjects uint64
}

// TestKVAB_StopWriteExperiment runs a configurable stop-write soak experiment.
// Default behavior is skip; enable explicitly:
// KVAB_RUN_STOPWRITE=1 go test ./bench/kvab -run '^TestKVAB_StopWriteExperiment$' -v -count=1 -timeout 30m
//
// Useful envs:
// KVAB_STOPWRITE_BACKEND=badger|pebble (default badger)
// KVAB_STOPWRITE_WARM_SEC=60          (write warmup)
// KVAB_STOPWRITE_SEC=600              (stop-write duration, 10m)
// KVAB_STOPWRITE_SAMPLE_MS=1000       (sampling interval)
func TestKVAB_StopWriteExperiment(t *testing.T) {
	if os.Getenv("KVAB_RUN_STOPWRITE") != "1" {
		t.Skip("set KVAB_RUN_STOPWRITE=1 to run stop-write experiment")
	}

	cfg := loadStopWriteConfigFromEnv()
	t.Logf(
		"stop-write config: backend=%s keys=%d value=%d warm=%ds stop=%ds sample=%dms read_ops/tick=%d force_gc_start=%v force_gc_finish=%v",
		cfg.Backend, cfg.KeyCount, cfg.ValueSize, cfg.WarmWriteSec, cfg.StopWriteSec, cfg.SampleMs, cfg.ReadOpsPerTick, cfg.ForceGCAtStart, cfg.ForceGCAtFinish,
	)

	openers := map[string]func(path string) (kvBackend, error){
		"badger": openBadgerBackend,
		"pebble": openPebbleBackend,
	}
	opener, ok := openers[cfg.Backend]
	if !ok {
		t.Fatalf("unsupported backend %q", cfg.Backend)
	}

	dbPath := filepath.Join(t.TempDir(), cfg.Backend)
	backend, err := opener(dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", cfg.Backend, err)
	}
	defer func() {
		if err := backend.Close(); err != nil {
			t.Fatalf("close %s: %v", cfg.Backend, err)
		}
	}()

	dsCfg := workloadConfig{
		KeyCount:  cfg.KeyCount,
		ValueSize: cfg.ValueSize,
		ScanLimit: cfg.ScanLimit,
		PairCount: cfg.PairCount,
	}
	keys, prefixes := buildOrderIndexDataset(dsCfg)
	value := bytes.Repeat([]byte{'v'}, cfg.ValueSize)

	seedStart := time.Now()
	if err := backend.Seed(keys, value); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Logf("seed done: keys=%d elapsed=%v", len(keys), time.Since(seedStart))

	// Warm write phase: create write pressure before stop-write interval.
	warmUntil := time.Now().Add(time.Duration(cfg.WarmWriteSec) * time.Second)
	rngWarm := rand.New(rand.NewSource(777))
	warmWrites := 0
	for time.Now().Before(warmUntil) {
		key := keys[rngWarm.Intn(len(keys))]
		if err := backend.Put(key, value); err != nil {
			t.Fatalf("warm put: %v", err)
		}
		warmWrites++
	}
	t.Logf("warm write done: writes=%d elapsed=%ds", warmWrites, cfg.WarmWriteSec)

	if cfg.ForceGCAtStart {
		runtime.GC()
		debug.FreeOSMemory()
	}

	interval := time.Duration(cfg.SampleMs) * time.Millisecond
	if interval <= 0 {
		interval = time.Second
	}
	progressEvery := cfg.ProgressEveryS
	if progressEvery <= 0 {
		progressEvery = 60
	}

	rngRead := rand.New(rand.NewSource(42))
	stopDeadline := time.Now().Add(time.Duration(cfg.StopWriteSec) * time.Second)
	start := time.Now()
	samples := make([]memSample, 0, maxInt(64, cfg.StopWriteSec))
	progressAt := time.Now().Add(time.Duration(progressEvery) * time.Second)

	for time.Now().Before(stopDeadline) {
		tickStart := time.Now()

		// Stop-write phase: read-only operations.
		for i := 0; i < cfg.ReadOpsPerTick; i++ {
			if i%5 == 0 {
				prefix := prefixes[rngRead.Intn(len(prefixes))]
				if _, err := backend.ScanPrefix(prefix, cfg.ScanLimit); err != nil {
					t.Fatalf("read scan: %v", err)
				}
			} else {
				key := keys[rngRead.Intn(len(keys))]
				if _, err := backend.Get(key); err != nil {
					t.Fatalf("read get: %v", err)
				}
			}
		}

		rss, err := currentRSSBytes()
		if err != nil {
			t.Fatalf("rss sample: %v", err)
		}
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)

		elapsed := time.Since(start).Seconds()
		samples = append(samples, memSample{
			elapsedSec:  elapsed,
			rssBytes:    rss,
			heapAlloc:   ms.HeapAlloc,
			heapInuse:   ms.HeapInuse,
			heapObjects: ms.HeapObjects,
		})

		if time.Now().After(progressAt) {
			t.Logf(
				"progress: elapsed=%.0fs rss=%.1fMB heap_alloc=%.1fMB heap_inuse=%.1fMB heap_obj=%d samples=%d",
				elapsed,
				float64(rss)/1024.0/1024.0,
				float64(ms.HeapAlloc)/1024.0/1024.0,
				float64(ms.HeapInuse)/1024.0/1024.0,
				ms.HeapObjects,
				len(samples),
			)
			progressAt = progressAt.Add(time.Duration(progressEvery) * time.Second)
		}

		remain := interval - time.Since(tickStart)
		if remain > 0 {
			time.Sleep(remain)
		}
	}

	if cfg.ForceGCAtFinish {
		runtime.GC()
		debug.FreeOSMemory()
		rss, err := currentRSSBytes()
		if err == nil {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			samples = append(samples, memSample{
				elapsedSec:  time.Since(start).Seconds(),
				rssBytes:    rss,
				heapAlloc:   ms.HeapAlloc,
				heapInuse:   ms.HeapInuse,
				heapObjects: ms.HeapObjects,
			})
		}
	}

	if len(samples) < 2 {
		t.Fatalf("insufficient samples: %d", len(samples))
	}

	first := samples[0]
	last := samples[len(samples)-1]
	peakRSS := first.rssBytes
	peakHeap := first.heapInuse
	for _, s := range samples {
		if s.rssBytes > peakRSS {
			peakRSS = s.rssBytes
		}
		if s.heapInuse > peakHeap {
			peakHeap = s.heapInuse
		}
	}

	rssSlope := slopeMBPerMinute(samples, func(s memSample) uint64 { return s.rssBytes })
	heapSlope := slopeMBPerMinute(samples, func(s memSample) uint64 { return s.heapInuse })

	t.Logf(
		"stop-write summary: backend=%s duration=%.0fs samples=%d rss_start=%.1fMB rss_end=%.1fMB rss_peak=%.1fMB rss_delta=%.1fMB rss_slope=%.3fMB/min heap_inuse_start=%.1fMB heap_inuse_end=%.1fMB heap_inuse_peak=%.1fMB heap_slope=%.3fMB/min",
		cfg.Backend,
		last.elapsedSec-first.elapsedSec,
		len(samples),
		toMB(first.rssBytes),
		toMB(last.rssBytes),
		toMB(peakRSS),
		toMB(last.rssBytes-first.rssBytes),
		rssSlope,
		toMB(first.heapInuse),
		toMB(last.heapInuse),
		toMB(peakHeap),
		heapSlope,
	)
}

func loadStopWriteConfigFromEnv() stopWriteConfig {
	return stopWriteConfig{
		Backend:         envString("KVAB_STOPWRITE_BACKEND", "badger"),
		KeyCount:        envInt("KVAB_STOPWRITE_KEYS", 120_000),
		ValueSize:       envInt("KVAB_STOPWRITE_VALUE_SIZE", 128),
		PairCount:       envInt("KVAB_STOPWRITE_PAIRS", 8),
		ScanLimit:       envInt("KVAB_STOPWRITE_SCAN_LIMIT", 64),
		WarmWriteSec:    envInt("KVAB_STOPWRITE_WARM_SEC", 60),
		StopWriteSec:    envInt("KVAB_STOPWRITE_SEC", 600),
		SampleMs:        envInt("KVAB_STOPWRITE_SAMPLE_MS", 1000),
		ReadOpsPerTick:  envInt("KVAB_STOPWRITE_READ_OPS_PER_TICK", 200),
		ProgressEveryS:  envInt("KVAB_STOPWRITE_PROGRESS_EVERY_SEC", 60),
		ForceGCAtStart:  envBool("KVAB_STOPWRITE_FORCE_GC_START", true),
		ForceGCAtFinish: envBool("KVAB_STOPWRITE_FORCE_GC_FINISH", true),
	}
}

func envString(name, def string) string {
	v, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}

func envBool(name string, def bool) bool {
	v, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return def
	}
}

func slopeMBPerMinute(samples []memSample, metric func(memSample) uint64) float64 {
	n := float64(len(samples))
	if n < 2 {
		return 0
	}

	var sumX, sumY, sumXY, sumXX float64
	for _, s := range samples {
		x := s.elapsedSec / 60.0 // minute
		y := toMB(metric(s))
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}

	den := n*sumXX - sumX*sumX
	if den == 0 {
		return 0
	}
	return (n*sumXY - sumX*sumY) / den
}

func toMB(v uint64) float64 {
	return float64(v) / 1024.0 / 1024.0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
