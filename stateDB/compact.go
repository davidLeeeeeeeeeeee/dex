package statedb

import (
	"fmt"
	"strings"
)

// CompactBusinessPrefix compacts one business-key family across all shards.
// Example business prefix: "v1_orderstate_".
func (s *DB) CompactBusinessPrefix(bizPrefix string) error {
	bizPrefix = strings.TrimSpace(bizPrefix)
	if bizPrefix == "" {
		return fmt.Errorf("business prefix is empty")
	}

	s.mu.RLock()
	store := s.store
	shards := append([]string(nil), s.shards...)
	backend := s.conf.Backend
	s.mu.RUnlock()

	if store == nil {
		return fmt.Errorf("stateDB is not initialized")
	}

	ps, ok := store.(*pebbleStore)
	if !ok {
		return fmt.Errorf("compact is not supported for backend: %s", backend)
	}

	for _, shard := range shards {
		start := kState(shard, bizPrefix)
		end := prefixUpperBound(start)
		if err := ps.db.Compact(start, end, true); err != nil {
			return fmt.Errorf("compact failed shard=%s prefix=%s: %w", shard, bizPrefix, err)
		}
	}
	return nil
}
