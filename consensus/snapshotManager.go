package consensus

import (
	"context"
	"dex/interfaces"
	"dex/logs"
	"dex/types"
	"sync"
)

// ============================================
// 快照管理器
// ============================================

type SnapshotManager struct {
	nodeID types.NodeID
	store  interfaces.BlockStore
	config *SnapshotConfig
	events interfaces.EventBus
	Logger logs.Logger
	mu     sync.Mutex
}

func NewSnapshotManager(id types.NodeID, store interfaces.BlockStore, config *SnapshotConfig, events interfaces.EventBus, logger logs.Logger) *SnapshotManager {
	return &SnapshotManager{
		nodeID: id,
		store:  store,
		config: config,
		events: events,
		Logger: logger,
	}
}

func (sm *SnapshotManager) Start(ctx context.Context) {
	if !sm.config.Enabled {
		return
	}

	// 监听区块最终化事件，定期创建快照
	sm.events.Subscribe(types.EventBlockFinalized, func(e interfaces.Event) {
		if block, ok := e.Data().(*types.Block); ok {
			sm.checkAndCreateSnapshot(block.Header.Height)
		}
	})
}

func (sm *SnapshotManager) checkAndCreateSnapshot(height uint64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// 检查是否到了创建快照的高度
	if height > 0 && height%sm.config.Interval == 0 {
		snapshot, err := sm.store.CreateSnapshot(height)
		if err != nil {
			Logf("[Node %s] Failed to create snapshot at height %d: %v\n",
				sm.nodeID, height, err)
			return
		}

		Logf("[Node %s] 📸 Created snapshot at height %d\n", sm.nodeID, height)

		sm.events.PublishAsync(types.BaseEvent{
			EventType: types.EventSnapshotCreated,
			EventData: snapshot,
		})
	}
}
