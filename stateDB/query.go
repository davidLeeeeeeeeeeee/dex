package statedb

import (
	"bytes"
	"dex/logs"
	"errors"
	"sort"
	"time"
)

const (
	stateDBGetManyProbeDebugThreshold = 5 * time.Millisecond
	stateDBGetManyProbeWarnThreshold  = 50 * time.Millisecond
)

// Get returns one key from stateDB live storage.
func (s *DB) Get(key string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sh := shardOf(key, s.conf.ShardHexWidth)
	stateKey := kState(sh, key)
	v, err := s.store.Get(stateKey)
	if err != nil {
		if s.store.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return v, true, nil
}

// GetMany returns state keys in one snapshot view.
func (s *DB) GetMany(keys []string) (out map[string][]byte, err error) {
	startAt := time.Now()
	mode := "init"
	shardGroups := 0
	defer func() {
		cost := time.Since(startAt)
		if err == nil && cost < stateDBGetManyProbeDebugThreshold {
			return
		}
		if err != nil || cost >= stateDBGetManyProbeWarnThreshold {
			logs.Warn(
				"[StateDB][GetMany] mode=%s keys=%d hits=%d shards=%d cost=%s err=%v",
				mode, len(keys), len(out), shardGroups, cost, err,
			)
			return
		}
		logs.Debug(
			"[StateDB][GetMany] mode=%s keys=%d hits=%d shards=%d cost=%s",
			mode, len(keys), len(out), shardGroups, cost,
		)
	}()

	s.mu.RLock()
	defer s.mu.RUnlock()

	out = make(map[string][]byte, len(keys))
	if len(keys) == 0 {
		mode = "empty"
		return out, nil
	}

	// Pebble fast path: group by shard + sorted seek on a single snapshot/iterator.
	if ps, ok := s.store.(*pebbleStore); ok {
		mode = "pebble_seek"
		keysByShard := make(map[string][]string, len(s.shards))
		for _, key := range keys {
			if key == "" {
				continue
			}
			sh := shardOf(key, s.conf.ShardHexWidth)
			keysByShard[sh] = append(keysByShard[sh], key)
		}
		shardGroups = len(keysByShard)
		if len(keysByShard) == 0 {
			mode = "empty_filtered"
			return out, nil
		}

		shards := make([]string, 0, len(keysByShard))
		for sh := range keysByShard {
			shards = append(shards, sh)
		}
		sort.Strings(shards)

		snap := ps.db.NewSnapshot()
		defer snap.Close()
		iter, err := snap.NewIter(nil)
		if err != nil {
			return nil, err
		}
		defer iter.Close()

		for _, sh := range shards {
			shardKeys := keysByShard[sh]
			sort.Strings(shardKeys)
			uniq := shardKeys[:0]
			prev := ""
			for _, key := range shardKeys {
				if key == prev {
					continue
				}
				uniq = append(uniq, key)
				prev = key
			}
			for _, key := range uniq {
				stateKey := []byte(kState(sh, key))
				if !iter.SeekGE(stateKey) {
					break
				}
				if !bytes.Equal(iter.Key(), stateKey) {
					continue
				}
				val := iter.Value()
				copied := make([]byte, len(val))
				copy(copied, val)
				out[key] = copied
			}
		}
		if err := iter.Error(); err != nil {
			return nil, err
		}
		return out, nil
	}

	// Generic fallback for non-pebble backends.
	mode = "fallback_get"
	err = s.store.View(func(tx kvReader) error {
		for _, key := range keys {
			if key == "" {
				continue
			}
			sh := shardOf(key, s.conf.ShardHexWidth)
			stateKey := kState(sh, key)
			v, err := tx.Get(stateKey)
			if err != nil {
				if s.store.IsNotFound(err) {
					continue
				}
				return err
			}
			out[key] = v
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Exists checks whether a key exists.
func (s *DB) Exists(key string) (bool, error) {
	_, exists, err := s.Get(key)
	return exists, err
}

// PageCurrentDiff keeps backward API shape but now pages current live state
// within one shard.
func (s *DB) PageCurrentDiff(shard string, pageSize int, pageToken string) (Page, error) {
	if pageSize <= 0 {
		pageSize = s.conf.PageSize
	}

	startAfter := decodeToken(pageToken)
	prefix := kStatePrefix(shard)
	prefixStr := string(prefix)

	stopScan := errors.New("stop scan")
	out := Page{Items: make([]KVUpdate, 0, pageSize)}
	last := ""

	err := s.store.View(func(tx kvReader) error {
		return tx.IteratePrefix(prefix, []byte(startAfter), func(key []byte, value []byte) error {
			k := string(key)
			if len(k) <= len(prefixStr) || k[:len(prefixStr)] != prefixStr {
				return nil
			}
			bizKey := k[len(prefixStr):]
			out.Items = append(out.Items, KVUpdate{
				Key:     bizKey,
				Value:   append([]byte(nil), value...),
				Deleted: false,
			})
			last = bizKey
			if len(out.Items) >= pageSize {
				return stopScan
			}
			return nil
		})
	})
	if err != nil && !errors.Is(err, stopScan) {
		return Page{}, err
	}
	if last != "" {
		out.NextPageToken = encodeToken(last)
	}
	return out, nil
}

// IterateLatestByPrefix iterates live state keys under one business prefix.
func (s *DB) IterateLatestByPrefix(prefix string, fn func(key string, value []byte) error) error {
	for _, shard := range s.shards {
		statePrefix := kStatePrefix(shard)
		statePrefixLen := len(statePrefix)
		scanPrefix := kState(shard, prefix)

		err := s.store.View(func(tx kvReader) error {
			return tx.IteratePrefix(scanPrefix, nil, func(key []byte, value []byte) error {
				if len(key) <= statePrefixLen {
					return nil
				}
				return fn(string(key[statePrefixLen:]), append([]byte(nil), value...))
			})
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// IterateLatestSnapshot iterates live state.
func (s *DB) IterateLatestSnapshot(fn func(key string, value []byte) error) error {
	return s.IterateLatestByPrefix("", fn)
}
