package consensus

import (
	"context"
	"dex/interfaces"
	"dex/types"
	"sync"
	"sync/atomic"
	"time"
)

// ============================================
// 同步管理器 - 增强版（支持快照）
// ============================================

type SyncManager struct {
	nodeID         types.NodeID
	node           *Node // 新增
	transport      interfaces.Transport
	store          interfaces.BlockStore
	config         *SyncConfig
	snapshotConfig *SnapshotConfig // 新增
	events         interfaces.EventBus
	SyncRequests   map[uint32]time.Time
	nextSyncID     uint32
	Syncing        bool
	Mu             sync.RWMutex
	PeerHeights    map[types.NodeID]uint64
	lastPoll       time.Time
	usingSnapshot  bool // 新增：标记是否正在使用快照同步
}

func NewSyncManager(nodeID types.NodeID, transport interfaces.Transport, store interfaces.BlockStore, config *SyncConfig, snapshotConfig *SnapshotConfig, events interfaces.EventBus) *SyncManager {
	return &SyncManager{
		nodeID:         nodeID,
		transport:      transport,
		store:          store,
		config:         config,
		snapshotConfig: snapshotConfig,
		events:         events,
		SyncRequests:   make(map[uint32]time.Time),
		PeerHeights:    make(map[types.NodeID]uint64),
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
		sm.transport.Send(peer, types.Message{
			Type: types.MsgHeightQuery,
			From: sm.nodeID,
		})
	}
}

func (sm *SyncManager) HandleHeightQuery(msg types.Message) {
	_, height := sm.store.GetLastAccepted()
	currentHeight := sm.store.GetCurrentHeight()

	err := sm.transport.Send(types.NodeID(msg.From), types.Message{
		Type:          types.MsgHeightResponse,
		From:          sm.nodeID,
		Height:        height,
		CurrentHeight: currentHeight,
	})
	if err != nil {
		return
	}
}

func (sm *SyncManager) HandleHeightResponse(msg types.Message) {
	sm.Mu.Lock()
	defer sm.Mu.Unlock()
	sm.PeerHeights[types.NodeID(msg.From)] = msg.CurrentHeight
}

func (sm *SyncManager) checkAndSync() {
	sm.Mu.Lock()
	if sm.Syncing {
		sm.Mu.Unlock()
		return
	}

	maxPeerHeight := uint64(0)
	for _, height := range sm.PeerHeights {
		if height > maxPeerHeight {
			maxPeerHeight = height
		}
	}
	sm.Mu.Unlock()

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

// 请求快照同步
func (sm *SyncManager) requestSnapshotSync(targetHeight uint64) {
	sm.Mu.Lock()
	if sm.Syncing {
		sm.Mu.Unlock()
		return
	}
	sm.Syncing = true
	sm.usingSnapshot = true
	syncID := atomic.AddUint32(&sm.nextSyncID, 1)
	sm.SyncRequests[syncID] = time.Now()
	sm.Mu.Unlock()

	// 找一个高度足够的节点
	sm.Mu.RLock()
	var targetPeer types.NodeID = "-1"
	for peer, height := range sm.PeerHeights {
		if height >= targetHeight {
			targetPeer = peer
			break
		}
	}
	sm.Mu.RUnlock()

	if targetPeer == "-1" {
		peers := sm.transport.SamplePeers(sm.nodeID, 5)
		if len(peers) > 0 {
			targetPeer = peers[0]
		}
	}

	if targetPeer != "-1" {
		Logf("[Node %d] 📸 Requesting SNAPSHOT sync from Node %d (behind by %d blocks)\n",
			sm.nodeID, targetPeer, targetHeight-sm.store.GetCurrentHeight())

		msg := types.Message{
			Type:            types.MsgSnapshotRequest,
			From:            sm.nodeID,
			SyncID:          syncID,
			RequestSnapshot: true,
			Height:          targetHeight,
		}
		sm.transport.Send(targetPeer, msg)
	} else {
		sm.Mu.Lock()
		sm.Syncing = false
		sm.usingSnapshot = false
		delete(sm.SyncRequests, syncID)
		sm.Mu.Unlock()
	}
}

func (sm *SyncManager) requestSync(fromHeight, toHeight uint64) {
	sm.Mu.Lock()
	if sm.Syncing {
		sm.Mu.Unlock()
		return
	}
	sm.Syncing = true
	syncID := atomic.AddUint32(&sm.nextSyncID, 1)
	sm.SyncRequests[syncID] = time.Now()
	sm.Mu.Unlock()

	sm.Mu.RLock()
	var targetPeer types.NodeID = "-1"
	for peer, height := range sm.PeerHeights {
		if height >= toHeight {
			targetPeer = peer
			break
		}
	}
	sm.Mu.RUnlock()

	if targetPeer == "-1" {
		peers := sm.transport.SamplePeers(sm.nodeID, 5)
		if len(peers) > 0 {
			targetPeer = peers[0]
		}
	}

	if targetPeer != "-1" {
		Logf("[Node %d] Requesting sync from Node %d for heights %d-%d\n",
			sm.nodeID, targetPeer, fromHeight, toHeight)

		msg := types.Message{
			Type:       types.MsgSyncRequest,
			From:       sm.nodeID,
			SyncID:     syncID,
			FromHeight: fromHeight,
			ToHeight:   toHeight,
		}
		sm.transport.Send(targetPeer, msg)
	} else {
		sm.Mu.Lock()
		sm.Syncing = false
		delete(sm.SyncRequests, syncID)
		sm.Mu.Unlock()
	}
}

// 处理快照请求（新增）
func (sm *SyncManager) HandleSnapshotRequest(msg types.Message) {
	// 获取最近的快照
	snapshot, exists := sm.store.GetLatestSnapshot()
	if !exists {
		// 如果没有快照，降级到普通同步
		sm.HandleSyncRequest(types.Message{
			Type:       types.MsgSyncRequest,
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
		sm.node.stats.Mu.Lock()
		sm.node.stats.snapshotsServed++
		sm.node.stats.Mu.Unlock()
	}

	response := types.Message{
		Type:           types.MsgSnapshotResponse,
		From:           sm.nodeID,
		SyncID:         msg.SyncID,
		Snapshot:       snapshot,
		SnapshotHeight: snapshot.Height,
	}

	sm.transport.Send(types.NodeID(msg.From), response)
}

// 处理快照响应（新增）
func (sm *SyncManager) HandleSnapshotResponse(msg types.Message) {
	sm.Mu.Lock()
	defer sm.Mu.Unlock()

	if _, ok := sm.SyncRequests[msg.SyncID]; !ok {
		return
	}

	delete(sm.SyncRequests, msg.SyncID)

	if msg.Snapshot == nil {
		sm.Syncing = false
		sm.usingSnapshot = false
		return
	}

	// 加载快照
	err := sm.store.LoadSnapshot(msg.Snapshot)
	if err != nil {
		Logf("[Node %d] Failed to load snapshot: %v\n", sm.nodeID, err)
		sm.Syncing = false
		sm.usingSnapshot = false
		return
	}

	// 更新统计
	if sm.node != nil {
		sm.node.stats.Mu.Lock()
		sm.node.stats.snapshotsUsed++
		sm.node.stats.Mu.Unlock()
	}

	Logf("[Node %d] 📸 Successfully loaded snapshot at height %d\n",
		sm.nodeID, msg.SnapshotHeight)

	// 发布快照加载事件
	sm.events.PublishAsync(types.BaseEvent{
		EventType: types.EventSnapshotLoaded,
		EventData: msg.Snapshot,
	})

	// 继续同步快照之后的区块
	currentHeight := sm.store.GetCurrentHeight()
	maxPeerHeight := uint64(0)
	for _, height := range sm.PeerHeights {
		if height > maxPeerHeight {
			maxPeerHeight = height
		}
	}

	sm.Syncing = false
	sm.usingSnapshot = false

	// 如果还需要更多区块，继续普通同步
	if maxPeerHeight > currentHeight+1 {
		go func() {
			time.Sleep(100 * time.Millisecond)
			sm.requestSync(currentHeight+1, minUint64(currentHeight+sm.config.BatchSize, maxPeerHeight))
		}()
	}
}

func (sm *SyncManager) HandleSyncRequest(msg types.Message) {
	blocks := sm.store.GetBlocksFromHeight(msg.FromHeight, msg.ToHeight)

	if len(blocks) == 0 {
		return
	}

	Logf("[Node %d] Sending %d blocks to Node %d for sync\n",
		sm.nodeID, len(blocks), msg.From)

	response := types.Message{
		Type:       types.MsgSyncResponse,
		From:       sm.nodeID,
		SyncID:     msg.SyncID,
		Blocks:     blocks,
		FromHeight: msg.FromHeight,
		ToHeight:   msg.ToHeight,
	}

	sm.transport.Send(types.NodeID(msg.From), response)
}

func (sm *SyncManager) HandleSyncResponse(msg types.Message) {
	sm.Mu.Lock()
	defer sm.Mu.Unlock()

	if _, ok := sm.SyncRequests[msg.SyncID]; !ok {
		return
	}

	delete(sm.SyncRequests, msg.SyncID)
	sm.Syncing = false

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

	sm.events.PublishAsync(types.BaseEvent{
		EventType: types.EventSyncComplete,
		EventData: added,
	})
}
