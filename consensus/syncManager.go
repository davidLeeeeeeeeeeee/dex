package consensus

import (
	"context"
	"dex/interfaces"
	"dex/logs"
	"dex/pb"
	"dex/types"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"
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
	usingSnapshot  bool // 标记是否正在使用快照同步
	// 采样验证相关字段
	sampling        bool                    // 是否正在采样验证
	sampleResponses map[types.NodeID]uint64 // 采样响应: nodeID -> acceptedHeight
	sampleStartTime time.Time               // 采样开始时间
	// 事件驱动同步相关
	pendingBlockBuffer    *PendingBlockBuffer  // 待处理区块缓冲区（用于补课）
	consecutiveStallCount uint32               // 连续同步停滞计数（高风险修复：死循环保护）
	InFlightSyncRanges    map[string]time.Time // 新增：正在进行的同步高度范围（去重）
}

func NewSyncManager(id types.NodeID, transport interfaces.Transport, store interfaces.BlockStore, config *SyncConfig, snapshotConfig *SnapshotConfig, events interfaces.EventBus, logger logs.Logger) *SyncManager {
	return &SyncManager{
		nodeID:             id,
		transport:          transport,
		store:              store,
		config:             config,
		snapshotConfig:     snapshotConfig,
		events:             events,
		Logger:             logger,
		SyncRequests:       make(map[uint32]time.Time),
		PeerHeights:        make(map[types.NodeID]uint64),
		lastPoll:           time.Now(),
		sampleResponses:    make(map[types.NodeID]uint64),
		InFlightSyncRanges: make(map[string]time.Time),
	}
}

// SetPendingBlockBuffer 设置 PendingBlockBuffer（在初始化后注入）
func (sm *SyncManager) SetPendingBlockBuffer(buffer *PendingBlockBuffer) {
	sm.pendingBlockBuffer = buffer
}

func (sm *SyncManager) Start(ctx context.Context) {
	// 1. 每隔 config.CheckInterval 进行一次常规同步检查（兜底）
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

	// 2. 每隔 config.CheckInterval 进行一次常规高度采样（兜底）
	go func() {
		ticker := time.NewTicker(sm.config.CheckInterval)
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

	// 3. 新增：高频健康检查循环（每1秒），用于处理超时清理和丢包恢复
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				sm.processTimeouts()
			case <-ctx.Done():
				return
			}
		}
	}()
}

// 定时查询其他节点高度
func (sm *SyncManager) pollPeerHeights() {
	// 如果正在同步中，暂停轮询高度，避免网络拥堵
	sm.Mu.RLock()
	if sm.Syncing {
		sm.Mu.RUnlock()
		return
	}
	sm.Mu.RUnlock()

	// 限制轮询频率，落后时增加探测频率
	cooldown := 2 * time.Second
	sm.Mu.RLock()
	_, accepted := sm.store.GetLastAccepted()
	maxPeer := uint64(0)
	for _, h := range sm.PeerHeights {
		if h > maxPeer {
			maxPeer = h
		}
	}
	if maxPeer > accepted+2 {
		cooldown = 500 * time.Millisecond // 落后较多时，每 500ms 探测一次
	}
	sm.Mu.RUnlock()

	if time.Since(sm.lastPoll) < cooldown {
		return
	}
	sm.lastPoll = time.Now()

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

	// 如果正在采样验证，收集响应
	if sm.sampling {
		sm.sampleResponses[types.NodeID(msg.From)] = msg.Height
	}
}

// processTimeouts 快速清理超时的请求（由 1s 的健康循环调用）
func (sm *SyncManager) processTimeouts() {
	sm.Mu.Lock()
	defer sm.Mu.Unlock()

	if !sm.Syncing && !sm.sampling {
		return
	}

	now := time.Now()

	// 检查同步请求超时
	if sm.Syncing {
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
	}

	// 检查高度采样超时
	if sm.sampling {
		if now.Sub(sm.sampleStartTime) > sm.config.SampleTimeout {
			logs.Debug("[Node %s] Sample verification timed out, resetting sampling flag", sm.nodeID)
			sm.sampling = false
		}
	}

	// 新增：检查并清理过期的处理中同步范围（兜底）
	for rangeKey, startTime := range sm.InFlightSyncRanges {
		if now.Sub(startTime) > sm.config.Timeout*2 {
			delete(sm.InFlightSyncRanges, rangeKey)
		}
	}
}

// 检查是否有必要启动同步程序
func (sm *SyncManager) checkAndSync() {
	sm.Mu.Lock()

	// 正在同步期间不启动新的检查
	if sm.Syncing {
		sm.Mu.Unlock()
		return
	}

	// 1. 优先检查采样验证状态（采样发出后需要在此评估结果）
	if sm.sampling {
		// 检查采样是否超时
		if time.Since(sm.sampleStartTime) > sm.config.SampleTimeout {
			logs.Debug("[Sync] Sample verification timed out, responses=%d", len(sm.sampleResponses))
			sm.sampling = false
			sm.Mu.Unlock()
			return
		}

		// 评估 Quorum
		quorumHeight, ok := sm.evaluateSampleQuorum()
		if !ok {
			// 尚未达到 Quorum，等待更多响应
			sm.Mu.Unlock()
			return
		}

		// Quorum 达成，可以安全同步
		sm.sampling = false
		_, localAcceptedHeight := sm.store.GetLastAccepted()

		if quorumHeight > localAcceptedHeight {
			logs.Info("[Sync] ✅ Quorum verified: target height %d confirmed by %.0f%% nodes",
				quorumHeight, sm.config.QuorumRatio*100)
			sm.Mu.Unlock()

			// 判断使用快照还是普通同步
			heightDiff := quorumHeight - localAcceptedHeight
			if sm.snapshotConfig.Enabled && heightDiff > sm.config.SnapshotThreshold {
				sm.requestSnapshotSync(quorumHeight)
			} else {
				sm.requestSync(localAcceptedHeight+1, minUint64(localAcceptedHeight+sm.config.BatchSize, quorumHeight))
			}
			return
		}

		sm.Mu.Unlock()
		return
	}

	// 2. 初步检查是否落后
	maxPeerHeight := uint64(0)
	for _, height := range sm.PeerHeights {
		if height > maxPeerHeight {
			maxPeerHeight = height
		}
	}

	_, localAcceptedHeight := sm.store.GetLastAccepted()
	heightDiff := uint64(0)
	if maxPeerHeight > localAcceptedHeight {
		heightDiff = maxPeerHeight - localAcceptedHeight
	}

	// 3. 如果落后超过阈值，启动采样验证
	if heightDiff > sm.config.BehindThreshold {
		logs.Debug("[Sync] Detected lag of %d blocks, starting sample verification (target=%d)",
			heightDiff, maxPeerHeight)
		sm.startHeightSampling()
	}

	sm.Mu.Unlock()
}

// TriggerSyncFromChit 由 Chits 消息驱动的同步触发入口（事件驱动模式）
// 无需复杂的采样验证，因为 Chits 本身就是共识采样的一部分
func (sm *SyncManager) TriggerSyncFromChit(peerAcceptedHeight uint64, from types.NodeID) {
	sm.Mu.Lock()

	// 更新 PeerHeights
	if peerAcceptedHeight > sm.PeerHeights[from] {
		sm.PeerHeights[from] = peerAcceptedHeight
	}

	// 智能判定是否需要新一轮同步：
	// 若已忙碌，则检查当前是否有正在进行的请求。如果所有请求都超时了，允许继续。
	if sm.Syncing || sm.sampling {
		if len(sm.SyncRequests) > 0 {
			// 有在途请求，等待完成
			sm.Mu.Unlock()
			return
		}
		// P0 修复：Syncing=true 但 SyncRequests 为空 → 上一轮同步已实质结束
		// （可能是响应丢包、超时清理后遗留的脏状态）。
		// 必须重置 Syncing，否则后续的 requestSync/requestSyncParallel
		// 会检查 Syncing=true 后直接 return，导致同步永久卡死。
		logs.Info("[SyncManager] TriggerSyncFromChit: resetting stale Syncing flag (Requests=0)")
		sm.Syncing = false
		sm.sampling = false
	}

	_, localAccepted := sm.store.GetLastAccepted()
	if peerAcceptedHeight <= localAccepted {
		sm.Mu.Unlock()
		return
	}

	heightDiff := peerAcceptedHeight - localAccepted
	sm.Mu.Unlock()

	logs.Debug("[SyncManager] TriggerSyncFromChit: peer=%s peerHeight=%d localAccepted=%d diff=%d",
		from, peerAcceptedHeight, localAccepted, heightDiff)

	// 中风险修复：轻量级高度采样验证
	// 采样 2 个随机节点（不包括发送者）来交叉确认高度。
	// 这能防止被单个恶意或故障节点拉入错误的同步轨道。
	go func() {
		logs.Debug("[SyncManager] Starting lightweight safety sampling for TriggerSyncFromChit (target=%d)", peerAcceptedHeight)

		peers := sm.transport.SamplePeers(sm.nodeID, 2)
		// 如果网络太小没法采样，则信任该 Chit（此时共识安全性由 BFT 保证）
		if len(peers) <= 1 {
			sm.performTriggeredSync(peerAcceptedHeight, localAccepted, heightDiff)
			return
		}

		// 发起高度查询
		for _, p := range peers {
			if p == from {
				continue
			}
			sm.transport.Send(p, types.Message{
				Type: types.MsgHeightQuery,
				From: sm.nodeID,
			})
		}

		// 等待 300ms 观察响应（通过 HandleHeightResponse 更新 PeerHeights）
		time.Sleep(300 * time.Millisecond)

		// 检查是否有其他节点也达到了该高度附近
		sm.Mu.RLock()
		maxOtherPeerHeight := uint64(0)
		for pID, h := range sm.PeerHeights {
			if pID != from && h > maxOtherPeerHeight {
				maxOtherPeerHeight = h
			}
		}
		sm.Mu.RUnlock()

		// 安全阈值：只要有任何一个其他节点也处于类似高度（或更高），即认为安全
		if maxOtherPeerHeight >= peerAcceptedHeight-1 {
			sm.performTriggeredSync(peerAcceptedHeight, localAccepted, heightDiff)
		} else {
			logs.Warn("[SyncManager] 🛡️ Lightweight sampling failed: no other peer confirmed height %d (maxOther=%d). Sync cancelled.",
				peerAcceptedHeight, maxOtherPeerHeight)
		}
	}()
}

// performTriggeredSync 执行被触发的同步动作
func (sm *SyncManager) performTriggeredSync(peerAcceptedHeight, localAccepted, heightDiff uint64) {
	if sm.snapshotConfig.Enabled && heightDiff > sm.config.SnapshotThreshold {
		sm.requestSnapshotSync(peerAcceptedHeight)
	} else {
		// 使用分片并行同步
		sm.requestSyncParallel(localAccepted+1, minUint64(localAccepted+sm.config.BatchSize, peerAcceptedHeight))
	}
}

// requestSyncParallel 分片并行同步：将高度范围分配给多个节点同时请求
func (sm *SyncManager) requestSyncParallel(fromHeight, toHeight uint64) {
	sm.Mu.Lock()
	if sm.Syncing {
		sm.Mu.Unlock()
		return
	}

	// 全局范围去重
	rangeKey := fmt.Sprintf("%d-%d", fromHeight, toHeight)
	if startTime, exists := sm.InFlightSyncRanges[rangeKey]; exists {
		if time.Since(startTime) < sm.config.Timeout {
			sm.Mu.Unlock()
			return
		}
	}

	sm.Syncing = true
	sm.InFlightSyncRanges[rangeKey] = time.Now()
	sm.Mu.Unlock()

	// 获取可用节点
	peers := sm.transport.SamplePeers(sm.nodeID, sm.config.ParallelPeers)
	if len(peers) == 0 {
		sm.Mu.Lock()
		sm.Syncing = false
		sm.Mu.Unlock()
		return
	}

	totalBlocks := toHeight - fromHeight + 1

	// 如果请求范围小或只有1个节点，退化到普通同步
	if totalBlocks <= 5 || len(peers) == 1 {
		sm.Mu.Lock()
		sm.Syncing = false
		delete(sm.InFlightSyncRanges, rangeKey) // 清理 key，否则 requestSync 会被同一个 key 阻塞
		sm.Mu.Unlock()
		sm.requestSync(fromHeight, toHeight)
		return
	}

	// 计算每个节点负责的高度范围
	rangePerPeer := totalBlocks / uint64(len(peers))

	logs.Info("[SyncManager] Starting parallel sync: heights %d-%d across %d peers",
		fromHeight, toHeight, len(peers))

	for i, peer := range peers {
		start := fromHeight + uint64(i)*rangePerPeer
		end := start + rangePerPeer - 1
		if i == len(peers)-1 {
			end = toHeight // 最后一个节点负责剩余
		}

		// 为每个分片创建独立的 SyncID
		syncID := atomic.AddUint32(&sm.nextSyncID, 1)
		sm.Mu.Lock()
		sm.SyncRequests[syncID] = time.Now()
		sm.Mu.Unlock()

		// 判断是否使用 ShortTxs 模式（基于总落后量而非分片大小）
		useShortMode := totalBlocks <= sm.config.ShortSyncThreshold

		go func(p types.NodeID, s, e uint64, id uint32, shortMode bool) {
			logs.Debug("[SyncManager] Parallel shard: peer=%s heights=%d-%d shortMode=%v", p, s, e, shortMode)

			msg := types.Message{
				Type:          types.MsgSyncRequest,
				From:          sm.nodeID,
				SyncID:        id,
				FromHeight:    s,
				ToHeight:      e,
				SyncShortMode: shortMode,
			}
			sm.transport.Send(p, msg)
		}(peer, start, end, syncID, useShortMode)
	}
}

// startHeightSampling 启动采样验证（必须持有写锁调用）
func (sm *SyncManager) startHeightSampling() {
	sm.sampling = true
	sm.sampleResponses = make(map[types.NodeID]uint64)
	sm.sampleStartTime = time.Now()

	// 采样 K 个节点
	peers := sm.transport.SamplePeers(sm.nodeID, sm.config.SampleSize)
	for _, peer := range peers {
		sm.transport.Send(peer, types.Message{
			Type: types.MsgHeightQuery,
			From: sm.nodeID,
		})
	}
}

// evaluateSampleQuorum 评估采样 Quorum（必须持有读锁调用）
// 返回满足 Quorum 的最高已最终化高度
func (sm *SyncManager) evaluateSampleQuorum() (uint64, bool) {
	responseCount := len(sm.sampleResponses)
	if responseCount == 0 {
		return 0, false
	}

	// 计算 Quorum 阈值
	required := int(float64(sm.config.SampleSize) * sm.config.QuorumRatio)
	if required < 1 {
		required = 1
	}

	// 如果响应不足，继续等待
	if responseCount < required {
		return 0, false
	}

	// 收集所有高度并排序
	heights := make([]uint64, 0, responseCount)
	for _, h := range sm.sampleResponses {
		heights = append(heights, h)
	}

	// 从高到低找到满足 Quorum 的最高高度
	// 对于每个候选高度，统计有多少节点的 acceptedHeight >= 该高度
	var maxQuorumHeight uint64
	for _, candidateHeight := range heights {
		supportCount := 0
		for _, h := range sm.sampleResponses {
			if h >= candidateHeight {
				supportCount++
			}
		}
		if supportCount >= required && candidateHeight > maxQuorumHeight {
			maxQuorumHeight = candidateHeight
		}
	}

	if maxQuorumHeight > 0 {
		logs.Debug("[Sync] Quorum check: %d/%d nodes support height %d (required=%d)",
			len(sm.sampleResponses), sm.config.SampleSize, maxQuorumHeight, required)
		return maxQuorumHeight, true
	}

	return 0, false
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
	// 记录处理中范围
	rangeKey := fmt.Sprintf("snapshot_%d", targetHeight)
	sm.InFlightSyncRanges[rangeKey] = time.Now()
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

	// 去重检查
	rangeKey := fmt.Sprintf("%d-%d", fromHeight, toHeight)
	if startTime, exists := sm.InFlightSyncRanges[rangeKey]; exists {
		if time.Since(startTime) < sm.config.Timeout {
			sm.Mu.Unlock()
			return
		}
	}

	sm.Syncing = true
	syncID := atomic.AddUint32(&sm.nextSyncID, 1)
	sm.SyncRequests[syncID] = time.Now()
	sm.InFlightSyncRanges[rangeKey] = time.Now()
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

	Logf("[Node %s] Received sync request from Node %s for heights %d-%d (found %d blocks, shortMode=%v)\n",
		sm.nodeID, msg.From, msg.FromHeight, msg.ToHeight, len(blocks), msg.SyncShortMode)

	response := types.Message{
		Type:          types.MsgSyncResponse,
		From:          sm.nodeID,
		SyncID:        msg.SyncID,
		Blocks:        blocks,
		FromHeight:    msg.FromHeight,
		ToHeight:      msg.ToHeight,
		SyncShortMode: msg.SyncShortMode,
	}

	// 附带签名集合（VRF 共识证据）
	if realStore, ok := sm.store.(*RealBlockStore); ok {
		sigSets := make(map[uint64][]byte)
		for _, block := range blocks {
			if block == nil {
				continue
			}
			if sigSet, exists := realStore.GetSignatureSet(block.Header.Height); exists {
				data, err := proto.Marshal(sigSet)
				if err == nil {
					sigSets[block.Header.Height] = data
				}
			}
		}
		if len(sigSets) > 0 {
			response.SignatureSets = sigSets
		}
	}

	// 自适应传输模式
	if msg.SyncShortMode {
		// 短期落后模式：附带 ShortTxs 用于快速还原
		response.BlocksShortTxs = make(map[string][]byte)
		for _, block := range blocks {
			if block == nil {
				continue
			}
			if cachedBlock, exists := GetCachedBlock(block.ID); exists && cachedBlock != nil {
				response.BlocksShortTxs[block.ID] = cachedBlock.ShortTxs
			}
		}
	} else {
		for _, block := range blocks {
			if block == nil {
				continue
			}
			if cachedBlock, exists := GetCachedBlock(block.ID); exists && cachedBlock != nil {
				_ = cachedBlock // 已有缓存数据
			}
		}
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
	// 处理完成后清理 range（或者保留一段时间由 timeout 清理）
	rangeKey := fmt.Sprintf("%d-%d", msg.FromHeight, msg.ToHeight)
	// 由于并行同步会有子范围，这里简单处理，实际推荐由 timeout 自动过期，这里仅示例
	delete(sm.InFlightSyncRanges, rangeKey)
	sm.Mu.Unlock()

	added := 0
	addErrs := 0
	var firstAddErr error
	var firstAddErrBlockID string
	for _, block := range msg.Blocks {
		if block == nil {
			continue
		}

		// 如果是 ShortMode 且有 ShortTxs 数据，提前交给 PendingBlockBuffer 处理
		if msg.SyncShortMode && len(msg.BlocksShortTxs) > 0 {
			shortTxs := msg.BlocksShortTxs[block.ID]
			if len(shortTxs) > 0 && sm.pendingBlockBuffer != nil {
				// 使用 ShortTxs 尝试还原区块
				sm.pendingBlockBuffer.AddPendingBlockForConsensus(block, shortTxs, types.NodeID(msg.From), 0, nil)
				added++ // 乐观计数，实际还原由 buffer 异步完成
				continue
			}
		}

		// 尝试直接添加
		isNew, err := sm.store.Add(block)
		if err != nil {
			addErrs++
			if firstAddErr == nil {
				firstAddErr = err
				firstAddErrBlockID = block.ID
			}
			// 接入补课机制：如果是数据不完整导致的失败，加入 PendingBlockBuffer
			if strings.Contains(err.Error(), "block data incomplete") && sm.pendingBlockBuffer != nil {
				logs.Debug("[SyncManager] Block %s incomplete, queueing for async resolution", block.ID)
				// 尝试使用响应中的 ShortTxs（如果有）
				shortTxs := msg.BlocksShortTxs[block.ID]
				sm.pendingBlockBuffer.AddPendingBlockForConsensus(block, shortTxs, types.NodeID(msg.From), 0, nil)
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
		// 优化：根据当前状态调整日志级别。如果是早于或等于当前高度的同步，使用 Debug。
		_, acceptedHeight := sm.store.GetLastAccepted()
		if msg.ToHeight <= acceptedHeight {
			logs.Debug("[Node %s] Sync received %d blocks but none were new (heights %d-%d, current accepted=%d)",
				sm.nodeID, len(msg.Blocks), msg.FromHeight, msg.ToHeight, acceptedHeight)
		} else {
			logs.Warn("[Node %s] Sync received %d blocks but none were new (heights %d-%d, current accepted=%d)",
				sm.nodeID, len(msg.Blocks), msg.FromHeight, msg.ToHeight, acceptedHeight)
		}
	}

	// 加速追块：如果收到的是对方"已接受高度"范围内的区块，则可直接按父链关系推进本地 lastAccepted。
	// 这能解决"本地已拥有区块但共识迟迟无法在该高度收敛"导致的长期停滞（反复 sync added=0）。
	//
	// 注意：此处只使用 sync 响应中的区块，不混入本地 store 的候选。
	// 原因：sync 响应来自已经最终化的 peer，代表它的最终化链。本地 store 可能有
	// 未被选中分支的候选区块（不同 Window/不同 parent），混入会导致选择到
	// 父链不兼容的区块，造成 SetFinalized 失败，链条中断。
	// 防分叉保护在投票层（selectBestCandidate 偏好低 Window + sendChits 一致性）
	// 和签名验证层（VerifySignatureSet）实现。
	finalized := 0
	acceptedID, acceptedHeight := sm.store.GetLastAccepted()
	blocksByHeight := make(map[uint64][]*types.Block, len(msg.Blocks))
	for _, b := range msg.Blocks {
		if b == nil {
			continue
		}
		blocksByHeight[b.Header.Height] = append(blocksByHeight[b.Header.Height], b)
	}

	for {
		nextHeight := acceptedHeight + 1
		cands := blocksByHeight[nextHeight]
		if len(cands) == 0 {
			break
		}
		// 从 sync 响应的候选中选择父链匹配的区块
		var chosen *types.Block
		for _, c := range cands {
			if c != nil && c.Header.ParentID == acceptedID {
				chosen = c
				break
			}
		}
		// 如果没有父链匹配的，退化为第一个候选（RealBlockStore.SetFinalized 内部会做安全检查）
		if chosen == nil {
			chosen = cands[0]
		}

		// VRF 签名集合验证（如果可用）
		if len(msg.SignatureSets) > 0 {
			if sigData, hasSig := msg.SignatureSets[nextHeight]; hasSig {
				var sigSet pb.ConsensusSignatureSet
				if err := proto.Unmarshal(sigData, &sigSet); err != nil {
					logs.Warn("[SyncManager] Failed to decode signature set for height %d: %v", nextHeight, err)
				} else {
					if !VerifySignatureSet(&sigSet, sm.config.SyncAlpha, sm.config.SyncBeta, sm.transport, sm.nodeID) {
						logs.Warn("[SyncManager] ⚠️ Signature set verification failed for height %d, skipping", nextHeight)
						break
					}
					// 验证通过，存储到本地
					if realStore, ok := sm.store.(*RealBlockStore); ok {
						realStore.SetSignatureSet(nextHeight, &sigSet)
					}
				}
			}
		}

		// 尝试最终化该高度（失败会保持 lastAccepted 不变）
		if err := sm.store.SetFinalized(nextHeight, chosen.ID); err != nil {
			logs.Warn("[SyncManager] Failed to finalize block %s at height %d: %v", chosen.ID, nextHeight, err)
			break
		}

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
		atomic.StoreUint32(&sm.consecutiveStallCount, 0) // 重置停滞计数
	} else if len(msg.Blocks) > 0 {
		// 高风险修复：检测同步停滞
		// 优化：只有当同步范围跨越了当前已接受高度，且未能推进时，才计入 stall
		if msg.ToHeight > acceptedHeight {
			stalls := atomic.AddUint32(&sm.consecutiveStallCount, 1)
			if stalls >= 3 {
				logs.Debug("[Node %s] ⚠️ Sync stalled for %d rounds at height %d, breaking pipeline and switching peers",
					sm.nodeID, stalls, acceptedHeight)
				sm.Mu.Lock()
				delete(sm.PeerHeights, types.NodeID(msg.From)) // 清理该 Peer 高度信息，强制重新采样
				sm.Syncing = false
				sm.Mu.Unlock()
				atomic.StoreUint32(&sm.consecutiveStallCount, 0)
				return
			}
		} else {
			logs.Debug("[Node %s] Sync response for heights %d-%d is older than accepted %d, ignoring stall check",
				sm.nodeID, msg.FromHeight, msg.ToHeight, acceptedHeight)
		}
	}

	// 低风险优化：信号精准化
	// 只有当本地高度与已知 Peer 最大高度差距小于 BatchSize 时才发布 EventSyncComplete。
	// 避免在长距离追块过程中，每一轮同步都唤醒 QueryManager 发起无效查询。
	if added > 0 || finalized > 0 {
		_, curAccepted := sm.store.GetLastAccepted()
		maxPeer := sm.getMaxPeerHeight()
		if maxPeer <= curAccepted+sm.config.BatchSize {
			sm.events.PublishAsync(types.BaseEvent{
				EventType: types.EventSyncComplete,
				EventData: added,
			})
		} else {
			logs.Debug("[Node %s] Skipping EventSyncComplete signal: still behind by %d blocks",
				sm.nodeID, maxPeer-curAccepted)
		}
	}

	// 中风险修复：处理完成后再解锁状态
	sm.Mu.Lock()
	sm.Syncing = false
	sm.Mu.Unlock()

	// 流水线续传：如果仍落后，立即发起下一轮同步
	if added > 0 || finalized > 0 {
		_, localAccepted := sm.store.GetLastAccepted()
		maxPeer := sm.getMaxPeerHeight()
		if maxPeer > localAccepted+1 {
			go func() {
				time.Sleep(50 * time.Millisecond) // 短暂延迟避免太激进
				sm.requestSync(localAccepted+1, minUint64(localAccepted+sm.config.BatchSize, maxPeer))
			}()
		}
	}
}

// getMaxPeerHeight 返回已知的最大对端高度
func (sm *SyncManager) getMaxPeerHeight() uint64 {
	sm.Mu.RLock()
	defer sm.Mu.RUnlock()

	maxHeight := uint64(0)
	for _, h := range sm.PeerHeights {
		if h > maxHeight {
			maxHeight = h
		}
	}
	return maxHeight
}

// VerifySignatureSet 验证共识签名集合的合法性（三步验证）
// 1. 轮次完整性：len(rounds) >= beta
// 2. 每轮签名充足：len(signatures) >= alpha
// 3. 采样合法性：重演 samplePeersDeterministic，确认 node_id 在合法采样集中
func VerifySignatureSet(sigSet *pb.ConsensusSignatureSet, alpha, beta int, transport interfaces.Transport, localNodeID types.NodeID) bool {
	if sigSet == nil {
		return false
	}

	// 如果 alpha/beta 未配置（为 0），使用宽松的默认值
	if alpha <= 0 {
		alpha = 1
	}
	if beta <= 0 {
		beta = 1
	}

	// 步骤 1：轮次完整性
	if len(sigSet.Rounds) < beta {
		logs.Debug("[VerifySignatureSet] Failed: rounds=%d < beta=%d", len(sigSet.Rounds), beta)
		return false
	}

	// 步骤 2：每轮签名充足
	for i, round := range sigSet.Rounds {
		if len(round.Signatures) < alpha {
			logs.Debug("[VerifySignatureSet] Failed: round %d signatures=%d < alpha=%d",
				i, len(round.Signatures), alpha)
			return false
		}
	}

	// 步骤 3：采样合法性（重演 VRF 确定性采样，确认每个签名者在合法采样集中）
	if transport != nil && len(sigSet.VrfSeed) > 0 {
		allPeers := transport.GetAllPeers(localNodeID)
		if len(allPeers) > 0 {
			// 用 K = len(allPeers) 作为上限（采样集不会超过全部节点数）
			k := len(allPeers)
			for _, round := range sigSet.Rounds {
				sampled := samplePeersDeterministic(sigSet.VrfSeed, round.SeqId, k, allPeers)
				sampledSet := make(map[string]bool, len(sampled))
				for _, p := range sampled {
					sampledSet[string(p)] = true
				}

				for _, sig := range round.Signatures {
					if !sampledSet[sig.NodeId] {
						logs.Debug("[VerifySignatureSet] Failed: node %s not in sampled set for round seq=%d",
							sig.NodeId, round.SeqId)
						return false
					}
				}
			}
		}
	}

	// 步骤 4：密码学验签（ECDSA 签名验证）
	// 对每个 ChitSignature 重算 digest 并验证签名
	// 当公钥注册表为空（如新节点首次同步）时跳过此步骤
	hasPublicKeys := false
	nodePublicKeysMu.RLock()
	hasPublicKeys = len(nodePublicKeys) > 0
	nodePublicKeysMu.RUnlock()

	if hasPublicKeys {
		for _, round := range sigSet.Rounds {
			for _, sig := range round.Signatures {
				if len(sig.Signature) == 0 {
					continue // 兼容旧版本无签名的记录
				}
				digest := ComputeChitDigest(sig.PreferredId, sigSet.Height, sigSet.VrfSeed, round.SeqId)
				if !VerifyChitSignature(sig.NodeId, digest, sig.Signature) {
					logs.Debug("[VerifySignatureSet] Failed: ECDSA signature verification failed for node %s round seq=%d",
						sig.NodeId, round.SeqId)
					return false
				}
			}
		}
	}

	return true
}
