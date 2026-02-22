package db

import (
	"dex/keys"
	"dex/logs"
	statedb "dex/stateDB"
	"time"
)

const stateSyncSlowThreshold = 300 * time.Millisecond

type stateWriteOp interface {
	GetKey() string
	GetValue() []byte
	IsDel() bool
}

// SyncStateUpdates mirrors VM diff writes into StateDB at finalized height.
// It returns the number of accepted updates forwarded to StateDB.
func (manager *Manager) SyncStateUpdates(height uint64, updates []interface{}) (int, error) {
	startAt := time.Now()
	var (
		buildKVsDur time.Duration
		applyDur    time.Duration
	)

	manager.mu.RLock()
	stateStore := manager.stateDB
	manager.mu.RUnlock()
	if stateStore == nil || len(updates) == 0 {
		return 0, nil
	}

	buildAt := time.Now()
	kvs := make([]statedb.KVUpdate, 0, len(updates))
	for _, u := range updates {
		op, ok := u.(stateWriteOp)
		if !ok || op == nil {
			continue
		}
		key := op.GetKey()
		if key == "" {
			continue
		}
		if !keys.IsStatefulKey(key) {
			continue
		}
		val := op.GetValue()
		if len(val) > 0 {
			cp := make([]byte, len(val))
			copy(cp, val)
			val = cp
		}
		kvs = append(kvs, statedb.KVUpdate{
			Key:     key,
			Value:   val,
			Deleted: op.IsDel(),
		})
	}
	buildKVsDur = time.Since(buildAt)

	if len(kvs) == 0 {
		return 0, nil
	}

	applyAt := time.Now()
	err := stateStore.ApplyAccountUpdate(height, kvs...)
	applyDur = time.Since(applyAt)
	if err != nil {
		return 0, err
	}

	totalDur := time.Since(startAt)
	cfg := stateStore.Config()
	checkpointEvery := cfg.CheckpointEvery
	checkpointBoundary := checkpointEvery > 0 && height > 0 && height%checkpointEvery == 0
	if totalDur >= stateSyncSlowThreshold || (checkpointBoundary && totalDur >= 100*time.Millisecond) {
		logs.Warn(
			"[StateSync][Slow] height=%d updates=%d stateful=%d total=%s build=%s apply=%s checkpointEvery=%d checkpointBoundary=%t",
			height, len(updates), len(kvs), totalDur, buildKVsDur, applyDur, checkpointEvery, checkpointBoundary,
		)
	}
	return len(kvs), nil
}
