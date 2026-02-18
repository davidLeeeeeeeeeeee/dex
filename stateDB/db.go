package statedb

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type DB struct {
	conf           Config
	store          kvStore
	liveDir        string
	checkpointsDir string

	mu sync.RWMutex

	// precomputed shards
	shards []string
}

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

	return &DB{
		conf:           cfg,
		store:          store,
		liveDir:        liveDir,
		checkpointsDir: checkpointsDir,
		shards:         shards,
	}, nil
}

func (s *DB) Close() error {
	if s.store == nil {
		return nil
	}
	return s.store.Close()
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

func (s *DB) createCheckpointLocked(height uint64) error {
	cpPath := s.checkpointPath(height)

	// If a stale directory already exists at the same height, replace it.
	if err := os.RemoveAll(cpPath); err != nil {
		return err
	}
	if err := os.MkdirAll(s.checkpointsDir, 0o755); err != nil {
		return err
	}

	if err := s.store.Checkpoint(cpPath, true); err != nil {
		return err
	}

	if err := s.store.Update(func(tx kvWriter) error {
		if err := tx.Set(kCpLatest(), u64ToBytes(height)); err != nil {
			return err
		}
		if err := tx.Set(kCpIndex(height), u64ToBytes(height)); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}

	return s.pruneOldCheckpointsLocked()
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
