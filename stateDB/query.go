package statedb

import "errors"

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
func (s *DB) GetMany(keys []string) (map[string][]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string][]byte, len(keys))
	if len(keys) == 0 {
		return out, nil
	}

	err := s.store.View(func(tx kvReader) error {
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
