package consensus

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// ============================================
// 同步管理器 - 增强版（支持快照）
// ============================================

type SyncManager struct {
	nodeID         NodeID
	node           *Node // 新增
	transport      Transport
	store          BlockStore
	config         *SyncConfig
	snapshotConfig *SnapshotConfig // 新增
	events         EventBus
	syncRequests   map[uint32]time.Time
	nextSyncID     uint32
	syncing        bool
	mu             sync.RWMutex
	peerHeights    map[NodeID]uint64
	lastPoll       time.Time
	usingSnapshot  bool // 新增：标记是否正在使用快照同步
}

func NewSyncManager(nodeID NodeID, transport Transport, store BlockStore, config *SyncConfig, snapshotConfig *SnapshotConfig, events EventBus) *SyncManager {
	return &SyncManager{
		nodeID:         nodeID,
		transport:      transport,
		store:          store,
		config:         config,
		snapshotConfig: snapshotConfig,
		events:         events,
		syncRequests:   make(map[uint32]time.Time),
		peerHeights:    make(map[NodeID]uint64),
		lastPoll:       time.Now(),
	}
}

func (sm *SyncManager) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(sm.config.CheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				sm.checkAndSync()
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				sm.pollPeerHeights()
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (sm *SyncManager) pollPeerHeights() {
	peers := sm.transport.SamplePeers(sm.nodeID, 10)
	for _, peer := range peers {
		sm.transport.Send(peer, Message{
			Type: MsgHeightQuery,
			From: sm.nodeID,
		})
	}
}

func (sm *SyncManager) HandleHeightQuery(msg Message) {
	_, height := sm.store.GetLastAccepted()
	currentHeight := sm.store.GetCurrentHeight()

	sm.transport.Send(NodeID(msg.From), Message{
		Type:          MsgHeightResponse,
		From:          sm.nodeID,
		Height:        height,
		CurrentHeight: currentHeight,
	})
}

func (sm *SyncManager) HandleHeightResponse(msg Message) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.peerHeights[NodeID(msg.From)] = msg.CurrentHeight
}

func (sm *SyncManager) checkAndSync() {
	sm.mu.Lock()
	if sm.syncing {
		sm.mu.Unlock()
		return
	}

	maxPeerHeight := uint64(0)
	for _, height := range sm.peerHeights {
		if height > maxPeerHeight {
			maxPeerHeight = height
		}
	}
	sm.mu.Unlock()

	localCurrentHeight := sm.store.GetCurrentHeight()
	heightDiff := uint64(0)
	if maxPeerHeight > localCurrentHeight {
		heightDiff = maxPeerHeight - localCurrentHeight
	}

	// 判断是否需要使用快照同步
	if sm.snapshotConfig.Enabled && heightDiff > sm.config.SnapshotThreshold {
		// 使用快照同步
		sm.requestSnapshotSync(maxPeerHeight)
	} else if heightDiff > sm.config.BehindThreshold {
		// 使用普通同步
		sm.requestSync(localCurrentHeight+1, minUint64(localCurrentHeight+sm.config.BatchSize, maxPeerHeight))
	}
}

// 请求快照同步（新增）
func (sm *SyncManager) requestSnapshotSync(targetHeight uint64) {
	sm.mu.Lock()
	if sm.syncing {
		sm.mu.Unlock()
		return
	}
	sm.syncing = true
	sm.usingSnapshot = true
	syncID := atomic.AddUint32(&sm.nextSyncID, 1)
	sm.syncRequests[syncID] = time.Now()
	sm.mu.Unlock()

	// 找一个高度足够的节点
	sm.mu.RLock()
	var targetPeer NodeID = -1
	for peer, height := range sm.peerHeights {
		if height >= targetHeight {
			targetPeer = peer
			break
		}
	}
	sm.mu.RUnlock()

	if targetPeer == -1 {
		peers := sm.transport.SamplePeers(sm.nodeID, 5)
		if len(peers) > 0 {
			targetPeer = peers[0]
		}
	}

	if targetPeer != -1 {
		Logf("[Node %d] 📸 Requesting SNAPSHOT sync from Node %d (behind by %d blocks)\n",
			sm.nodeID, targetPeer, targetHeight-sm.store.GetCurrentHeight())

		msg := Message{
			Type:            MsgSnapshotRequest,
			From:            sm.nodeID,
			SyncID:          syncID,
			RequestSnapshot: true,
			Height:          targetHeight,
		}
		sm.transport.Send(targetPeer, msg)
	} else {
		sm.mu.Lock()
		sm.syncing = false
		sm.usingSnapshot = false
		delete(sm.syncRequests, syncID)
		sm.mu.Unlock()
	}
}

func (sm *SyncManager) requestSync(fromHeight, toHeight uint64) {
	sm.mu.Lock()
	if sm.syncing {
		sm.mu.Unlock()
		return
	}
	sm.syncing = true
	syncID := atomic.AddUint32(&sm.nextSyncID, 1)
	sm.syncRequests[syncID] = time.Now()
	sm.mu.Unlock()

	sm.mu.RLock()
	var targetPeer NodeID = -1
	for peer, height := range sm.peerHeights {
		if height >= toHeight {
			targetPeer = peer
			break
		}
	}
	sm.mu.RUnlock()

	if targetPeer == -1 {
		peers := sm.transport.SamplePeers(sm.nodeID, 5)
		if len(peers) > 0 {
			targetPeer = peers[0]
		}
	}

	if targetPeer != -1 {
		Logf("[Node %d] Requesting sync from Node %d for heights %d-%d\n",
			sm.nodeID, targetPeer, fromHeight, toHeight)

		msg := Message{
			Type:       MsgSyncRequest,
			From:       sm.nodeID,
			SyncID:     syncID,
			FromHeight: fromHeight,
			ToHeight:   toHeight,
		}
		sm.transport.Send(targetPeer, msg)
	} else {
		sm.mu.Lock()
		sm.syncing = false
		delete(sm.syncRequests, syncID)
		sm.mu.Unlock()
	}
}

// 处理快照请求（新增）
func (sm *SyncManager) HandleSnapshotRequest(msg Message) {
	// 获取最近的快照
	snapshot, exists := sm.store.GetLatestSnapshot()
	if !exists {
		// 如果没有快照，降级到普通同步
		sm.HandleSyncRequest(Message{
			Type:       MsgSyncRequest,
			From:       msg.From,
			SyncID:     msg.SyncID,
			FromHeight: 1,
			ToHeight:   minUint64(100, sm.store.GetCurrentHeight()),
		})
		return
	}

	Logf("[Node %d] 📸 Sending snapshot (height %d) to Node %d\n",
		sm.nodeID, snapshot.Height, msg.From)

	// 更新统计
	if sm.node != nil {
		sm.node.stats.mu.Lock()
		sm.node.stats.snapshotsServed++
		sm.node.stats.mu.Unlock()
	}

	response := Message{
		Type:           MsgSnapshotResponse,
		From:           sm.nodeID,
		SyncID:         msg.SyncID,
		Snapshot:       snapshot,
		SnapshotHeight: snapshot.Height,
	}

	sm.transport.Send(NodeID(msg.From), response)
}

// 处理快照响应（新增）
func (sm *SyncManager) HandleSnapshotResponse(msg Message) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if _, ok := sm.syncRequests[msg.SyncID]; !ok {
		return
	}

	delete(sm.syncRequests, msg.SyncID)

	if msg.Snapshot == nil {
		sm.syncing = false
		sm.usingSnapshot = false
		return
	}

	// 加载快照
	err := sm.store.LoadSnapshot(msg.Snapshot)
	if err != nil {
		Logf("[Node %d] Failed to load snapshot: %v\n", sm.nodeID, err)
		sm.syncing = false
		sm.usingSnapshot = false
		return
	}

	// 更新统计
	if sm.node != nil {
		sm.node.stats.mu.Lock()
		sm.node.stats.snapshotsUsed++
		sm.node.stats.mu.Unlock()
	}

	Logf("[Node %d] 📸 Successfully loaded snapshot at height %d\n",
		sm.nodeID, msg.SnapshotHeight)

	// 发布快照加载事件
	sm.events.PublishAsync(BaseEvent{
		eventType: EventSnapshotLoaded,
		data:      msg.Snapshot,
	})

	// 继续同步快照之后的区块
	currentHeight := sm.store.GetCurrentHeight()
	maxPeerHeight := uint64(0)
	for _, height := range sm.peerHeights {
		if height > maxPeerHeight {
			maxPeerHeight = height
		}
	}

	sm.syncing = false
	sm.usingSnapshot = false

	// 如果还需要更多区块，继续普通同步
	if maxPeerHeight > currentHeight+1 {
		go func() {
			time.Sleep(100 * time.Millisecond)
			sm.requestSync(currentHeight+1, minUint64(currentHeight+sm.config.BatchSize, maxPeerHeight))
		}()
	}
}

func (sm *SyncManager) HandleSyncRequest(msg Message) {
	blocks := sm.store.GetBlocksFromHeight(msg.FromHeight, msg.ToHeight)

	if len(blocks) == 0 {
		return
	}

	Logf("[Node %d] Sending %d blocks to Node %d for sync\n",
		sm.nodeID, len(blocks), msg.From)

	response := Message{
		Type:       MsgSyncResponse,
		From:       sm.nodeID,
		SyncID:     msg.SyncID,
		Blocks:     blocks,
		FromHeight: msg.FromHeight,
		ToHeight:   msg.ToHeight,
	}

	sm.transport.Send(NodeID(msg.From), response)
}

func (sm *SyncManager) HandleSyncResponse(msg Message) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if _, ok := sm.syncRequests[msg.SyncID]; !ok {
		return
	}

	delete(sm.syncRequests, msg.SyncID)
	sm.syncing = false

	added := 0
	for _, block := range msg.Blocks {
		isNew, err := sm.store.Add(block)
		if err != nil {
			continue
		}
		if isNew {
			added++
		}
	}

	if added > 0 {
		Logf("[Node %d] 📦 Successfully synced %d new blocks (heights %d-%d)\n",
			sm.nodeID, added, msg.FromHeight, msg.ToHeight)
	}

	sm.events.PublishAsync(BaseEvent{
		eventType: EventSyncComplete,
		data:      added,
	})
}
