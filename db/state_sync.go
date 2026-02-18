package db

import (
	"dex/keys"
	statedb "dex/stateDB"
)

type stateWriteOp interface {
	GetKey() string
	GetValue() []byte
	IsDel() bool
}

// SyncStateUpdates mirrors VM diff writes into StateDB at finalized height.
// It returns the number of accepted updates forwarded to StateDB.
func (manager *Manager) SyncStateUpdates(height uint64, updates []interface{}) (int, error) {
	manager.mu.RLock()
	stateStore := manager.stateDB
	manager.mu.RUnlock()
	if stateStore == nil || len(updates) == 0 {
		return 0, nil
	}

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

	if len(kvs) == 0 {
		return 0, nil
	}
	if err := stateStore.ApplyAccountUpdate(height, kvs...); err != nil {
		return 0, err
	}
	return len(kvs), nil
}
