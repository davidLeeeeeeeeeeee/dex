package db

import (
	"context"
	statedb "dex/stateDB"
	"fmt"
	"sort"
)

// EnsureStateCheckpoint creates (or refreshes) a checkpoint at height.
func (manager *Manager) EnsureStateCheckpoint(height uint64) error {
	manager.mu.RLock()
	stateStore := manager.stateDB
	manager.mu.RUnlock()
	if stateStore == nil {
		return fmt.Errorf("stateDB is not initialized")
	}
	if height == 0 {
		return nil
	}
	return stateStore.FlushAndRotate(height)
}

// ListStateSnapshotShards lists shards from checkpoint height (or live state when height=0).
func (manager *Manager) ListStateSnapshotShards(height uint64) ([]statedb.ShardInfo, error) {
	manager.mu.RLock()
	stateStore := manager.stateDB
	manager.mu.RUnlock()
	if stateStore == nil {
		return nil, fmt.Errorf("stateDB is not initialized")
	}
	return stateStore.ListSnapshotShards(height)
}

// PageStateSnapshotShard pages one shard from checkpoint height (or live state when height=0).
func (manager *Manager) PageStateSnapshotShard(height uint64, shard string, pageSize int, pageToken string) (statedb.Page, error) {
	manager.mu.RLock()
	stateStore := manager.stateDB
	manager.mu.RUnlock()
	if stateStore == nil {
		return statedb.Page{}, fmt.Errorf("stateDB is not initialized")
	}
	return stateStore.PageSnapshotShard(height, shard, 0, pageSize, pageToken)
}

// BuildStateSnapshotFromUpdates replaces local stateDB with the supplied snapshot content.
func (manager *Manager) BuildStateSnapshotFromUpdates(ctx context.Context, height uint64, updates []statedb.KVUpdate) error {
	manager.mu.RLock()
	stateStore := manager.stateDB
	manager.mu.RUnlock()
	if stateStore == nil {
		return fmt.Errorf("stateDB is not initialized")
	}

	kvMap := make(map[string][]byte, len(updates))
	for _, u := range updates {
		if u.Key == "" || u.Deleted {
			continue
		}
		valCopy := make([]byte, len(u.Value))
		copy(valCopy, u.Value)
		kvMap[u.Key] = valCopy
	}

	keys := make([]string, 0, len(kvMap))
	for k := range kvMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	return stateStore.BuildFullSnapshot(ctx, height, func(yield func(key string, val []byte) error) error {
		for _, k := range keys {
			if err := yield(k, kvMap[k]); err != nil {
				return err
			}
		}
		return nil
	})
}
