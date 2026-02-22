package statedb

import (
	"dex/logs"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type DB struct {
	conf           Config
	store          kvStore
	liveDir        string
	checkpointsDir string

	mu sync.RWMutex

	checkpointReqCh  chan uint64
	checkpointStopCh chan struct{}
	checkpointDoneCh chan struct{}

	closeOnce sync.Once
	closeErr  error

	// precomputed shards
	shards []string
}

const (
	stateDBCheckpointProbeWarnThreshold  = 100 * time.Millisecond
	stateDBCheckpointStageWarnThreshold  = 20 * time.Millisecond
	stateDBCheckpointProbeInfoThreshold  = 20 * time.Millisecond
	stateDBCheckpointWorkerWarnThreshold = 300 * time.Millisecond
)

func New(cfg Config) (*DB, error) {
	if cfg.Backend == "" {
		cfg.Backend = BackendPebble
	}
	if cfg.CheckpointEvery == 0 {
		cfg.CheckpointEvery = 1000
	}
	if cfg.ShardHexWidth != 1 && cfg.ShardHexWidth != 2 {
		cfg.ShardHexWidth = 1
	}
	if cfg.PageSize <= 0 {
		cfg.PageSize = 1000
	}
	if cfg.CheckpointKeep <= 0 {
		cfg.CheckpointKeep = 10
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}

	liveDir := filepath.Join(cfg.DataDir, "live")
	checkpointsDir := filepath.Join(cfg.DataDir, "checkpoints")
	if err := os.MkdirAll(liveDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(checkpointsDir, 0o755); err != nil {
		return nil, err
	}

	var (
		store kvStore
		err   error
	)
	switch cfg.Backend {
	case BackendPebble:
		store, err = newPebbleStore(liveDir)
	case BackendBadger:
		return nil, fmt.Errorf("stateDB backend %q is not available in this build", BackendBadger)
	default:
		return nil, fmt.Errorf("unsupported stateDB backend: %s", cfg.Backend)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to open %s store: %w", cfg.Backend, err)
	}

	var shards []string
	if cfg.ShardHexWidth == 1 {
		for i := 0; i < 16; i++ {
			shards = append(shards, fmt.Sprintf("%x", i))
		}
	} else {
		for i := 0; i < 256; i++ {
			shards = append(shards, fmt.Sprintf("%02x", i))
		}
	}

	db := &DB{
		conf:             cfg,
		store:            store,
		liveDir:          liveDir,
		checkpointsDir:   checkpointsDir,
		checkpointReqCh:  make(chan uint64, 1),
		checkpointStopCh: make(chan struct{}),
		checkpointDoneCh: make(chan struct{}),
		shards:           shards,
	}
	go db.checkpointWorker()
	return db, nil
}

func (s *DB) Close() error {
	s.closeOnce.Do(func() {
		if s.checkpointStopCh != nil {
			close(s.checkpointStopCh)
			if s.checkpointDoneCh != nil {
				<-s.checkpointDoneCh
			}
		}
		if s.store != nil {
			s.closeErr = s.store.Close()
			s.store = nil
		}
	})
	return s.closeErr
}

// Config returns a copy of current effective runtime config.
func (s *DB) Config() Config {
	return s.conf
}

func checkpointOf(height uint64, every uint64) bool {
	if every == 0 {
		return false
	}
	return height%every == 0
}

func (s *DB) checkpointEvery() uint64 {
	return s.conf.CheckpointEvery
}

func (s *DB) enqueueCheckpoint(height uint64) bool {
	if height == 0 {
		return false
	}
	for {
		select {
		case <-s.checkpointStopCh:
			return false
		default:
		}
		select {
		case s.checkpointReqCh <- height:
			return true
		case <-s.checkpointStopCh:
			return false
		default:
			// queue is full, coalesce queued height with latest height
			select {
			case queued := <-s.checkpointReqCh:
				if queued > height {
					height = queued
				}
			case <-s.checkpointStopCh:
				return false
			default:
			}
		}
	}
}

func (s *DB) checkpointWorker() {
	defer close(s.checkpointDoneCh)

	var (
		pendingHeight uint64
		hasPending    bool
	)
	for {
		if !hasPending {
			select {
			case h := <-s.checkpointReqCh:
				pendingHeight = h
				hasPending = true
			case <-s.checkpointStopCh:
				// Drain pending requests and coalesce to latest before exit.
				for {
					select {
					case h := <-s.checkpointReqCh:
						if !hasPending || h > pendingHeight {
							pendingHeight = h
						}
						hasPending = true
					default:
						if !hasPending {
							return
						}
						goto RUN
					}
				}
			}
		}

		// Coalesce burst requests to the latest height.
		for {
			select {
			case h := <-s.checkpointReqCh:
				if h > pendingHeight {
					pendingHeight = h
				}
			default:
				goto RUN
			}
		}

	RUN:
		startAt := time.Now()
		s.mu.Lock()
		err := s.createCheckpointLocked(pendingHeight)
		s.mu.Unlock()
		cost := time.Since(startAt)
		if err != nil || cost >= stateDBCheckpointWorkerWarnThreshold {
			logs.Warn(
				"[StateDB][CheckpointWorker] height=%d total=%s err=%v",
				pendingHeight, cost, err,
			)
		}
		pendingHeight = 0
		hasPending = false
	}
}

func (s *DB) checkpointPath(height uint64) string {
	return filepath.Join(s.checkpointsDir, fmt.Sprintf("h_%020d", height))
}

func (s *DB) parseCheckpointHeight(name string) (uint64, bool) {
	if !strings.HasPrefix(name, "h_") {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(name, "h_"), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func (s *DB) listCheckpointHeightsOnDisk() ([]uint64, error) {
	entries, err := os.ReadDir(s.checkpointsDir)
	if err != nil {
		return nil, err
	}
	heights := make([]uint64, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if h, ok := s.parseCheckpointHeight(entry.Name()); ok {
			heights = append(heights, h)
		}
	}
	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
	return heights, nil
}

func (s *DB) pruneOldCheckpointsLocked() error {
	if s.conf.CheckpointKeep <= 0 {
		return nil
	}
	heights, err := s.listCheckpointHeightsOnDisk()
	if err != nil {
		return err
	}
	if len(heights) <= s.conf.CheckpointKeep {
		return nil
	}

	toDelete := heights[:len(heights)-s.conf.CheckpointKeep]
	for _, h := range toDelete {
		if err := os.RemoveAll(s.checkpointPath(h)); err != nil {
			return err
		}
	}

	return s.store.Update(func(tx kvWriter) error {
		for _, h := range toDelete {
			if err := tx.Delete(kCpIndex(h)); err != nil && !s.store.IsNotFound(err) {
				return err
			}
		}
		if len(heights) > 0 {
			latest := heights[len(heights)-1]
			latestb := u64ToBytes(latest)
			if err := tx.Set(kCpLatest(), latestb); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *DB) createCheckpointLocked(height uint64) (err error) {
	startAt := time.Now()
	var (
		durRemove     time.Duration
		durMkdir      time.Duration
		durCheckpoint time.Duration
		durMeta       time.Duration
		durPrune      time.Duration
	)
	cpPath := s.checkpointPath(height)
	defer func() {
		total := time.Since(startAt)
		if err == nil &&
			total < stateDBCheckpointProbeInfoThreshold &&
			durCheckpoint < stateDBCheckpointStageWarnThreshold &&
			durPrune < stateDBCheckpointStageWarnThreshold {
			return
		}
		if err != nil ||
			total >= stateDBCheckpointProbeWarnThreshold ||
			durCheckpoint >= stateDBCheckpointStageWarnThreshold ||
			durPrune >= stateDBCheckpointStageWarnThreshold {
			logs.Warn(
				"[StateDB][Checkpoint] height=%d total=%s remove=%s mkdir=%s checkpoint=%s meta=%s prune=%s path=%s err=%v",
				height, total, durRemove, durMkdir, durCheckpoint, durMeta, durPrune, cpPath, err,
			)
		}
	}()

	// If a stale directory already exists at the same height, replace it.
	removeAt := time.Now()
	if err = os.RemoveAll(cpPath); err != nil {
		durRemove = time.Since(removeAt)
		return err
	}
	durRemove = time.Since(removeAt)

	mkdirAt := time.Now()
	if err = os.MkdirAll(s.checkpointsDir, 0o755); err != nil {
		durMkdir = time.Since(mkdirAt)
		return err
	}
	durMkdir = time.Since(mkdirAt)

	checkpointAt := time.Now()
	if err = s.store.Checkpoint(cpPath, true); err != nil {
		durCheckpoint = time.Since(checkpointAt)
		return err
	}
	durCheckpoint = time.Since(checkpointAt)

	metaAt := time.Now()
	err = s.store.Update(func(tx kvWriter) error {
		latest := height
		if latestRaw, getErr := tx.Get(kCpLatest()); getErr == nil {
			if len(latestRaw) >= 8 {
				if h := bytesToU64(latestRaw); h > latest {
					latest = h
				}
			}
		} else if !s.store.IsNotFound(getErr) {
			return getErr
		}

		if err := tx.Set(kCpLatest(), u64ToBytes(latest)); err != nil {
			return err
		}
		if err := tx.Set(kCpIndex(height), u64ToBytes(height)); err != nil {
			return err
		}
		return nil
	})
	durMeta = time.Since(metaAt)
	if err != nil {
		return err
	}

	pruneAt := time.Now()
	err = s.pruneOldCheckpointsLocked()
	durPrune = time.Since(pruneAt)
	if err != nil {
		return err
	}
	return nil
}

func (s *DB) openCheckpointStore(height uint64) (kvStore, error) {
	path := s.checkpointPath(height)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("checkpoint path is not directory: %s", path)
	}
	return newPebbleStoreReadOnly(path)
}
