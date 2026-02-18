package statedb

import "errors"

// Get returns one key from stateDB live storage.
func (s *DB) Get(key string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sh := shardOf(key, s.conf.ShardHexWidth)
	stateKey := kState(sh, key)

	var (
		result []byte
		found  bool
	)
	err := s.store.View(func(tx kvReader) error {
		v, err := tx.Get(stateKey)
		if err != nil {
			if s.store.IsNotFound(err) {
				return nil
			}
			return err
		}
		result = v
		found = true
		return nil
	})
	return result, found, err
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

// IterateLatestSnapshot iterates live state.
func (s *DB) IterateLatestSnapshot(fn func(key string, value []byte) error) error {
	for _, shard := range s.shards {
		prefix := kStatePrefix(shard)
		prefixStr := string(prefix)
		err := s.store.View(func(tx kvReader) error {
			return tx.IteratePrefix(prefix, nil, func(key []byte, value []byte) error {
				k := string(key)
				if len(k) <= len(prefixStr) || k[:len(prefixStr)] != prefixStr {
					return nil
				}
				return fn(k[len(prefixStr):], append([]byte(nil), value...))
			})
		})
		if err != nil {
			return err
		}
	}
	return nil
}
