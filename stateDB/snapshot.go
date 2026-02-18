package statedb

import (
	"context"
	"dex/keys"
	"encoding/binary"
	"errors"
	"fmt"
)

// BuildFullSnapshot imports provided state key-value pairs into live DB
// and creates a checkpoint at height E.
func (s *DB) BuildFullSnapshot(ctx context.Context, E uint64, iterAccount func(yield func(key string, val []byte) error) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	seq, err := s.store.NextSequence()
	if err != nil {
		return err
	}

	err = s.store.Update(func(tx kvWriter) error {
		// Replace snapshot base: clear existing state namespace first.
		for _, shard := range s.shards {
			prefix := kStatePrefix(shard)
			toDelete := make([][]byte, 0, 1024)
			if err := tx.IteratePrefix(prefix, nil, func(key []byte, value []byte) error {
				kc := make([]byte, len(key))
				copy(kc, key)
				toDelete = append(toDelete, kc)
				return nil
			}); err != nil {
				return err
			}
			for _, dk := range toDelete {
				if err := tx.Delete(dk); err != nil && !s.store.IsNotFound(err) {
					return err
				}
			}
		}

		err := iterAccount(func(key string, val []byte) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !keys.IsStatefulKey(key) {
				return nil
			}
			sh := shardOf(key, s.conf.ShardHexWidth)
			return tx.Set(kState(sh, key), val)
		})
		if err != nil {
			return err
		}

		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, seq)
		hb := make([]byte, 8)
		binary.BigEndian.PutUint64(hb, E)

		if err := tx.Set(kMetaH2Seq(E), buf); err != nil {
			return err
		}
		if err := tx.Set(kMetaSeq2H(seq), hb); err != nil {
			return err
		}
		if err := tx.Set(kMetaLastAppliedHeight(), hb); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	return s.createCheckpointLocked(E)
}

// ListSnapshotShards returns shard counts for the given checkpoint height.
// If E == 0, it reads from live state.
func (s *DB) ListSnapshotShards(E uint64) ([]ShardInfo, error) {
	var (
		store kvStore
		err   error
		close func() error
	)

	if E == 0 {
		store = s.store
		close = func() error { return nil }
	} else {
		store, err = s.openCheckpointStore(E)
		if err != nil {
			return nil, err
		}
		close = store.Close
	}
	defer func() { _ = close() }()

	out := make([]ShardInfo, 0, len(s.shards))
	for _, shard := range s.shards {
		prefix := kStatePrefix(shard)
		var cnt int64
		err := store.View(func(tx kvReader) error {
			return tx.IteratePrefix(prefix, nil, func(key []byte, value []byte) error {
				cnt++
				return nil
			})
		})
		if err != nil {
			return nil, err
		}
		out = append(out, ShardInfo{Shard: shard, Count: cnt})
	}
	return out, nil
}

// PageSnapshotShard returns one page for one shard from checkpoint E.
// If E == 0, it pages live state.
func (s *DB) PageSnapshotShard(E uint64, shard string, page, pageSize int, pageToken string) (Page, error) {
	if pageSize <= 0 {
		pageSize = s.conf.PageSize
	}

	var (
		store kvStore
		err   error
		close func() error
	)
	if E == 0 {
		store = s.store
		close = func() error { return nil }
	} else {
		store, err = s.openCheckpointStore(E)
		if err != nil {
			return Page{}, err
		}
		close = store.Close
	}
	defer func() { _ = close() }()

	prefix := kStatePrefix(shard)
	prefixStr := string(prefix)
	startAfter := decodeToken(pageToken)
	stopScan := errors.New("stop scan")

	out := Page{Items: make([]KVUpdate, 0, pageSize)}
	last := ""

	err = store.View(func(tx kvReader) error {
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

// Helper for debugging/inspection.
func (s *DB) LatestCheckpointHeight() (uint64, error) {
	var h uint64
	err := s.store.View(func(tx kvReader) error {
		v, err := tx.Get(kCpLatest())
		if err != nil {
			if s.store.IsNotFound(err) {
				return nil
			}
			return err
		}
		if len(v) < 8 {
			return fmt.Errorf("invalid latest checkpoint value")
		}
		h = binary.BigEndian.Uint64(v)
		return nil
	})
	return h, err
}
