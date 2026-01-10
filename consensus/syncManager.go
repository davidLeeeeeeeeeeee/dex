package consensus

import (
	"context"
	"dex/interfaces"
	"dex/logs"
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
	Logger         logs.Logger
	SyncRequests   map[uint32]time.Time
	nextSyncID     uint32
	Syncing        bool
	Mu             sync.RWMutex
	PeerHeights    map[types.NodeID]uint64
	lastPoll       time.Time
	usingSnapshot  bool // 新增：标记是否正在使用快照同步
}

func NewSyncManager(id types.NodeID, transport interfaces.Transport, store interfaces.BlockStore, config *SyncConfig, snapshotConfig *SnapshotConfig, events interfaces.EventBus, logger logs.Logger) *SyncManager {
	return &SyncManager{
		nodeID:         id,
		transport:      transport,
		store:          store,
		config:         config,
		snapshotConfig: snapshotConfig,
		events:         events,
		Logger:         logger,
		SyncRequests:   make(map[uint32]time.Time),
		PeerHeights:    make(map[types.NodeID]uint64),
		lastPoll:       time.Now(),
	}
}

func (sm *SyncManager) Start(ctx context.Context) {
	go func() {
		logs.SetThreadNodeContext(string(sm.nodeID))
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

// 定时查询其他节点高度
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
	// 同步应该基于对方“已最终化/已接受”的高度，而不是其当前最大高度（可能包含大量未最终化块）。
	// 否则会导致本节点不断尝试同步一些其实全网都还没最终化的高度，出现重复 sync 且 added=0 的情况。
	sm.PeerHeights[types.NodeID(msg.From)] = msg.Height
}

// 检查是否有必要启动同步程序
func (sm *SyncManager) checkAndSync() {
	sm.Mu.Lock()
	if sm.Syncing {
		// 检查是否有同步请求超时
		now := time.Now()
		hasTimeout := false
		for syncID, startTime := range sm.SyncRequests {
			if now.Sub(startTime) > sm.config.Timeout {
				Logf("[Node %s] ⚠️ Sync request %d timed out (started at %v)\n",
					sm.nodeID, syncID, startTime.Format("15:04:05"))
				delete(sm.SyncRequests, syncID)
				hasTimeout = true
			}
		}

		if hasTimeout && len(sm.SyncRequests) == 0 {
			logs.Warn("[Node %s] All sync requests timed out, resetting Syncing flag", sm.nodeID)
			sm.Syncing = false
			sm.usingSnapshot = false
		}

		if sm.Syncing {
			sm.Mu.Unlock()
			return
		}
	}

	maxPeerHeight := uint64(0)
	for _, height := range sm.PeerHeights {
		if height > maxPeerHeight {
			maxPeerHeight = height
		}
	}
	sm.Mu.Unlock()

	_, localAcceptedHeight := sm.store.GetLastAccepted()
	localCurrentHeight := sm.store.GetCurrentHeight()
	if localCurrentHeight > localAcceptedHeight {
		logs.Debug("[Sync] Local height gap detected (accepted=%d, current=%d)",
			localAcceptedHeight, localCurrentHeight)
	}
	heightDiff := uint64(0)
	if maxPeerHeight > localAcceptedHeight {
		heightDiff = maxPeerHeight - localAcceptedHeight
	}

	// 判断是否需要使用快照同步
	if sm.snapshotConfig.Enabled && heightDiff > sm.config.SnapshotThreshold {
		// 使用快照同步
		sm.requestSnapshotSync(maxPeerHeight)
	} else if heightDiff > sm.config.BehindThreshold {
		// 使用普通同步
		logs.Debug("[Sync] Behind by %d (peer=%d, accepted=%d, current=%d)",
			heightDiff, maxPeerHeight, localAcceptedHeight, localCurrentHeight)
		sm.requestSync(localAcceptedHeight+1, minUint64(localAcceptedHeight+sm.config.BatchSize, maxPeerHeight))
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
		Logf("[Node %s] 📸 Requesting SNAPSHOT sync from Node %s (behind by %d blocks)\n",
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
		Logf("[Node %s] Requesting sync from Node %s for heights %d-%d\n",
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

	Logf("[Node %s] 📸 Sending snapshot (height %d) to Node %s\n",
		sm.nodeID, snapshot.Height, msg.From)

	// 更新统计
	if sm.node != nil {
		sm.node.Stats.Mu.Lock()
		sm.node.Stats.SnapshotsServed++
		sm.node.Stats.Mu.Unlock()
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
		Logf("[Node %s] Failed to load snapshot: %v\n", sm.nodeID, err)
		sm.Syncing = false
		sm.usingSnapshot = false
		return
	}

	// 更新统计
	if sm.node != nil {
		sm.node.Stats.Mu.Lock()
		sm.node.Stats.SnapshotsUsed++
		sm.node.Stats.Mu.Unlock()
	}

	Logf("[Node %s] 📸 Successfully loaded snapshot at height %d\n",
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

	Logf("[Node %s] Sending %d blocks to Node %s for sync\n",
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
	if _, ok := sm.SyncRequests[msg.SyncID]; !ok {
		sm.Mu.Unlock()
		return
	}
	delete(sm.SyncRequests, msg.SyncID)
	sm.Syncing = false
	sm.Mu.Unlock()

	added := 0
	addErrs := 0
	var firstAddErr error
	var firstAddErrBlockID string
	for _, block := range msg.Blocks {
		isNew, err := sm.store.Add(block)
		if err != nil {
			addErrs++
			if firstAddErr == nil {
				firstAddErr = err
				if block != nil {
					firstAddErrBlockID = block.ID
				}
			}
			continue
		}
		if isNew {
			added++
		}
	}

	if added > 0 {
		Logf("[Node %s] 📦 Successfully synced %d new blocks (heights %d-%d)\n",
			sm.nodeID, added, msg.FromHeight, msg.ToHeight)
	}
	if addErrs > 0 {
		logs.Warn("[Node %s] Sync received %d blocks, %d failed to add (first=%s err=%v)",
			sm.nodeID, len(msg.Blocks), addErrs, firstAddErrBlockID, firstAddErr)
	}
	if added == 0 && len(msg.Blocks) > 0 && addErrs == 0 {
		logs.Warn("[Node %s] Sync received %d blocks but none were new (heights %d-%d)",
			sm.nodeID, len(msg.Blocks), msg.FromHeight, msg.ToHeight)
	}

	// 加速追块：如果收到的是对方“已接受高度”范围内的区块，则可直接按父链关系推进本地 lastAccepted。
	// 这能解决“本地已拥有区块但共识迟迟无法在该高度收敛”导致的长期停滞（反复 sync added=0）。
	finalized := 0
	acceptedID, acceptedHeight := sm.store.GetLastAccepted()
	blocksByHeight := make(map[uint64][]*types.Block, len(msg.Blocks))
	for _, b := range msg.Blocks {
		if b == nil {
			continue
		}
		blocksByHeight[b.Height] = append(blocksByHeight[b.Height], b)
	}

	for {
		nextHeight := acceptedHeight + 1
		cands := blocksByHeight[nextHeight]
		if len(cands) == 0 {
			break
		}
		chosen := cands[0]
		for _, c := range cands {
			if c != nil && c.ParentID == acceptedID {
				chosen = c
				break
			}
		}

		// 尝试最终化该高度（RealBlockStore 内部会做父链接安全检查；失败会保持 lastAccepted 不变）
		sm.store.SetFinalized(nextHeight, chosen.ID)

		newAcceptedID, newAcceptedHeight := sm.store.GetLastAccepted()
		if newAcceptedHeight != nextHeight || newAcceptedID != chosen.ID {
			break
		}

		finalized++
		acceptedID, acceptedHeight = newAcceptedID, newAcceptedHeight

		// 主动发布最终化事件，驱动 ProposalManager/SnapshotManager 等组件状态前进
		if blk, ok := sm.store.Get(chosen.ID); ok && blk != nil {
			sm.events.PublishAsync(types.BaseEvent{
				EventType: types.EventBlockFinalized,
				EventData: blk,
			})
		}
	}

	if finalized > 0 {
		logs.Info("[Node %s] ✅ Fast-finalized %d block(s) via sync (accepted=%d)",
			sm.nodeID, finalized, acceptedHeight)
	}

	sm.events.PublishAsync(types.BaseEvent{
		EventType: types.EventSyncComplete,
		EventData: added,
	})
}
