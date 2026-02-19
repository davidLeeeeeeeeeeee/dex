package consensus

import (
	"context"
	"dex/interfaces"
	"dex/logs"
	"dex/pb"
	statedb "dex/stateDB"
	"dex/types"
	"errors"
	"fmt"
	"sort"
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
	nodeID    types.NodeID
	node      *Node // 新增
	transport interfaces.Transport
	store     interfaces.BlockStore
	config    *SyncConfig

	events       interfaces.EventBus
	Logger       logs.Logger
	SyncRequests map[uint32]time.Time
	nextSyncID   uint32
	Syncing      bool
	Mu           sync.RWMutex
	PeerHeights  map[types.NodeID]uint64
	lastPoll     time.Time

	// 采样验证相关字段
	sampling        bool                    // 是否正在采样验证
	sampleResponses map[types.NodeID]uint64 // 采样响应: nodeID -> acceptedHeight
	sampleStartTime time.Time               // 采样开始时间
	// 事件驱动同步相关
	pendingBlockBuffer    *PendingBlockBuffer  // 待处理区块缓冲区（用于补课）
	consecutiveStallCount uint32               // 连续同步停滞计数（高风险修复：死循环保护）
	InFlightSyncRanges    map[string]time.Time // 新增：正在进行的同步高度范围（去重）

	// Chits-trigger debounce state.
	chitPending          bool
	chitPendingHeight    uint64
	chitPendingFrom      types.NodeID
	chitPendingFirstSeen time.Time
	chitTimerArmed       bool
	lastChitTriggerAt    time.Time
}

type stateSnapshotFetcher interface {
	FetchStateSnapshotShards(peer types.NodeID, targetHeight uint64) (*types.StateSnapshotShardsResponse, error)
	FetchStateSnapshotPage(peer types.NodeID, snapshotHeight uint64, shard string, pageSize int, pageToken string) (*types.StateSnapshotPageResponse, error)
}

func NewSyncManager(id types.NodeID, transport interfaces.Transport, store interfaces.BlockStore, config *SyncConfig, events interfaces.EventBus, logger logs.Logger) *SyncManager {
	return &SyncManager{
		nodeID:             id,
		transport:          transport,
		store:              store,
		config:             config,
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
			sm.Logger.Warn("[Node %s] All sync requests timed out, resetting Syncing flag", sm.nodeID)
			sm.Syncing = false

		}
	}

	// 检查高度采样超时
	if sm.sampling {
		if now.Sub(sm.sampleStartTime) > sm.config.SampleTimeout {
			sm.Logger.Debug("[Node %s] Sample verification timed out, resetting sampling flag", sm.nodeID)
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
			sm.Logger.Debug("[Sync] Sample verification timed out, responses=%d", len(sm.sampleResponses))
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
			sm.Logger.Info("[Sync] ✅ Quorum verified: target height %d confirmed by %.0f%% nodes",
				quorumHeight, sm.config.QuorumRatio*100)
			sm.Mu.Unlock()

			heightDiff := quorumHeight - localAcceptedHeight
			if heightDiff > sm.deepLagStateSyncThreshold() {
				sm.performStateDBFirstSyncThenCatchUp(quorumHeight, localAcceptedHeight)
				return
			}
			sm.requestSync(localAcceptedHeight+1, minUint64(localAcceptedHeight+sm.config.BatchSize, quorumHeight))
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
		sm.Logger.Debug("[Sync] Detected lag of %d blocks, starting sample verification (target=%d)",
			heightDiff, maxPeerHeight)
		sm.startHeightSampling()
	}

	sm.Mu.Unlock()
}

func (sm *SyncManager) chitSoftGap() uint64 {
	if sm == nil || sm.config == nil || sm.config.ChitSoftGap == 0 {
		return 1
	}
	return sm.config.ChitSoftGap
}

func (sm *SyncManager) chitHardGap() uint64 {
	if sm == nil || sm.config == nil || sm.config.ChitHardGap == 0 {
		return 3
	}
	soft := sm.chitSoftGap()
	if sm.config.ChitHardGap < soft {
		return soft
	}
	return sm.config.ChitHardGap
}

func (sm *SyncManager) chitGracePeriod() time.Duration {
	if sm == nil || sm.config == nil || sm.config.ChitGracePeriod <= 0 {
		return time.Second
	}
	return sm.config.ChitGracePeriod
}

func (sm *SyncManager) chitCooldown() time.Duration {
	if sm == nil || sm.config == nil || sm.config.ChitCooldown < 0 {
		return 0
	}
	return sm.config.ChitCooldown
}

func (sm *SyncManager) chitMinConfirmPeers() int {
	if sm == nil || sm.config == nil || sm.config.ChitMinConfirmPeers < 0 {
		return 0
	}
	return sm.config.ChitMinConfirmPeers
}

func (sm *SyncManager) deepLagStateSyncThreshold() uint64 {
	if sm == nil || sm.config == nil || sm.config.DeepLagStateSyncThreshold == 0 {
		return 100
	}
	return sm.config.DeepLagStateSyncThreshold
}

func (sm *SyncManager) stateSyncPeers() int {
	if sm == nil || sm.config == nil || sm.config.StateSyncPeers <= 0 {
		return 4
	}
	return sm.config.StateSyncPeers
}

func (sm *SyncManager) stateSyncShardConcurrency() int {
	if sm == nil || sm.config == nil || sm.config.StateSyncShardConcurrency <= 0 {
		return 8
	}
	return sm.config.StateSyncShardConcurrency
}

func (sm *SyncManager) stateSyncPageSize() int {
	if sm == nil || sm.config == nil || sm.config.StateSyncPageSize <= 0 {
		return 1000
	}
	return sm.config.StateSyncPageSize
}

func (sm *SyncManager) resetStaleSyncStateLocked() bool {
	if !sm.Syncing && !sm.sampling {
		return false
	}
	if len(sm.SyncRequests) > 0 {
		return true
	}
	sm.Logger.Debug("[SyncManager] Chit trigger: resetting stale sync state (Requests=0)")
	sm.Syncing = false
	sm.sampling = false
	return false
}

func (sm *SyncManager) triggerCooldownRemainingLocked(now time.Time) time.Duration {
	cooldown := sm.chitCooldown()
	if cooldown <= 0 || sm.lastChitTriggerAt.IsZero() {
		return 0
	}
	elapsed := now.Sub(sm.lastChitTriggerAt)
	if elapsed >= cooldown {
		return 0
	}
	return cooldown - elapsed
}

func (sm *SyncManager) markPendingChitLocked(height uint64, from types.NodeID) {
	now := time.Now()
	if !sm.chitPending {
		sm.chitPending = true
		sm.chitPendingHeight = height
		sm.chitPendingFrom = from
		sm.chitPendingFirstSeen = now
		return
	}
	if height > sm.chitPendingHeight {
		sm.chitPendingHeight = height
		sm.chitPendingFrom = from
		sm.chitPendingFirstSeen = now
	}
}

func (sm *SyncManager) clearPendingChitLocked() {
	sm.chitPending = false
	sm.chitPendingHeight = 0
	sm.chitPendingFrom = ""
	sm.chitPendingFirstSeen = time.Time{}
}

func (sm *SyncManager) countPeerConfirmationsLocked(targetHeight uint64, exclude types.NodeID) int {
	minConfirmedHeight := targetHeight
	if minConfirmedHeight > 0 {
		minConfirmedHeight--
	}
	confirmations := 0
	for peerID, h := range sm.PeerHeights {
		if peerID == exclude {
			continue
		}
		if h >= minConfirmedHeight {
			confirmations++
		}
	}
	return confirmations
}

func (sm *SyncManager) scheduleChitEvaluationLocked(delay time.Duration) {
	if !sm.chitPending {
		return
	}
	if delay <= 0 {
		delay = 50 * time.Millisecond
	}
	if sm.chitTimerArmed {
		return
	}
	sm.chitTimerArmed = true
	go func(wait time.Duration) {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		<-timer.C
		sm.evaluatePendingChitTrigger()
	}(delay)
}

func (sm *SyncManager) evaluatePendingChitTrigger() {
	sm.Mu.Lock()
	sm.chitTimerArmed = false
	if !sm.chitPending {
		sm.Mu.Unlock()
		return
	}

	targetHeight := sm.chitPendingHeight
	from := sm.chitPendingFrom
	firstSeen := sm.chitPendingFirstSeen
	_, localAccepted := sm.store.GetLastAccepted()
	if targetHeight <= localAccepted {
		sm.clearPendingChitLocked()
		sm.Mu.Unlock()
		return
	}

	if sm.resetStaleSyncStateLocked() {
		sm.scheduleChitEvaluationLocked(200 * time.Millisecond)
		sm.Mu.Unlock()
		return
	}

	now := time.Now()
	if remain := sm.triggerCooldownRemainingLocked(now); remain > 0 {
		sm.scheduleChitEvaluationLocked(remain)
		sm.Mu.Unlock()
		return
	}

	grace := sm.chitGracePeriod()
	if elapsed := now.Sub(firstSeen); elapsed < grace {
		sm.scheduleChitEvaluationLocked(grace - elapsed)
		sm.Mu.Unlock()
		return
	}

	minConfirm := sm.chitMinConfirmPeers()
	if minConfirm > 0 {
		confirmed := sm.countPeerConfirmationsLocked(targetHeight, from)
		if confirmed < minConfirm {
			recheck := grace / 2
			if recheck <= 0 {
				recheck = 200 * time.Millisecond
			}
			sm.scheduleChitEvaluationLocked(recheck)
			sm.Mu.Unlock()
			return
		}
	}

	sm.clearPendingChitLocked()
	sm.lastChitTriggerAt = now
	sm.Mu.Unlock()

	heightDiff := targetHeight - localAccepted
	if heightDiff >= sm.chitHardGap() && heightDiff > sm.deepLagStateSyncThreshold() {
		sm.Logger.Info("[SyncManager] TriggerSyncFromChit: delayed deep lag peer=%s peerHeight=%d localAccepted=%d diff=%d threshold=%d, use stateDB-first catch-up",
			from, targetHeight, localAccepted, heightDiff, sm.deepLagStateSyncThreshold())
		sm.performStateDBFirstSyncThenCatchUp(targetHeight, localAccepted)
		return
	}

	sm.Logger.Debug("[SyncManager] TriggerSyncFromChit: delayed trigger peer=%s peerHeight=%d localAccepted=%d diff=%d",
		from, targetHeight, localAccepted, heightDiff)
	sm.performTriggeredSync(targetHeight, localAccepted, heightDiff)
}

// TriggerSyncFromChit is an event-driven sync trigger path with delay, threshold, and debounce.
func (sm *SyncManager) TriggerSyncFromChit(peerAcceptedHeight uint64, from types.NodeID) {
	sm.Mu.Lock()

	if peerAcceptedHeight > sm.PeerHeights[from] {
		sm.PeerHeights[from] = peerAcceptedHeight
	}

	_, localAccepted := sm.store.GetLastAccepted()
	if peerAcceptedHeight <= localAccepted {
		sm.Mu.Unlock()
		return
	}

	heightDiff := peerAcceptedHeight - localAccepted
	now := time.Now()

	// Hard gap path: trigger fast, still respecting stale-state guard and cooldown debounce.
	if heightDiff >= sm.chitHardGap() {
		if sm.resetStaleSyncStateLocked() {
			sm.markPendingChitLocked(peerAcceptedHeight, from)
			sm.scheduleChitEvaluationLocked(200 * time.Millisecond)
			sm.Mu.Unlock()
			return
		}
		if remain := sm.triggerCooldownRemainingLocked(now); remain > 0 {
			sm.markPendingChitLocked(peerAcceptedHeight, from)
			sm.scheduleChitEvaluationLocked(remain)
			sm.Mu.Unlock()
			return
		}

		sm.clearPendingChitLocked()
		sm.lastChitTriggerAt = now
		sm.Mu.Unlock()

		if heightDiff > sm.deepLagStateSyncThreshold() {
			sm.Logger.Info("[SyncManager] TriggerSyncFromChit: deep lag detected peer=%s peerHeight=%d localAccepted=%d diff=%d threshold=%d, use stateDB-first catch-up",
				from, peerAcceptedHeight, localAccepted, heightDiff, sm.deepLagStateSyncThreshold())
			sm.performStateDBFirstSyncThenCatchUp(peerAcceptedHeight, localAccepted)
			return
		}

		sm.Logger.Debug("[SyncManager] TriggerSyncFromChit: hard trigger peer=%s peerHeight=%d localAccepted=%d diff=%d",
			from, peerAcceptedHeight, localAccepted, heightDiff)
		sm.performTriggeredSync(peerAcceptedHeight, localAccepted, heightDiff)
		return
	}

	// Soft gap path: collect and wait for grace period + confirmations.
	if heightDiff < sm.chitSoftGap() {
		sm.Mu.Unlock()
		return
	}

	sm.markPendingChitLocked(peerAcceptedHeight, from)
	grace := sm.chitGracePeriod()
	if elapsed := now.Sub(sm.chitPendingFirstSeen); elapsed >= grace {
		sm.scheduleChitEvaluationLocked(10 * time.Millisecond)
	} else {
		sm.scheduleChitEvaluationLocked(grace - elapsed)
	}
	sm.Mu.Unlock()
}

// performTriggeredSync 执行被触发的同步动作
func (sm *SyncManager) performTriggeredSync(peerAcceptedHeight, localAccepted, heightDiff uint64) {
	// 使用分片并行同步
	sm.requestSyncParallel(localAccepted+1, minUint64(localAccepted+sm.config.BatchSize, peerAcceptedHeight))
}

// performStateDBFirstSyncThenCatchUp 深度落后时先执行 stateDB-first 追赶，再进入常规追块流水线。
// 优先尝试“分片 + 多 peer”并行下载 stateDB 快照，分担单节点压力；失败时回退到仅区块追赶。
func (sm *SyncManager) performStateDBFirstSyncThenCatchUp(peerAcceptedHeight, localAccepted uint64) {
	if peerAcceptedHeight <= localAccepted {
		return
	}

	if sm.performDistributedStateDBSync(peerAcceptedHeight) {
		sm.Logger.Info("[SyncManager] StateDB-first catch-up: distributed state snapshot synced, continue block catch-up")
	} else {
		sm.Logger.Warn("[SyncManager] StateDB-first catch-up: distributed state snapshot unavailable/failed, fallback to block-only catch-up")
	}

	window := sm.deepLagStateSyncThreshold()
	toHeight := minUint64(localAccepted+window, peerAcceptedHeight)
	if toHeight < localAccepted+1 {
		return
	}

	sm.Logger.Info("[SyncManager] StateDB-first catch-up: syncing heights %d-%d before normal pipeline",
		localAccepted+1, toHeight)
	sm.requestSync(localAccepted+1, toHeight)
}

func (sm *SyncManager) performDistributedStateDBSync(targetHeight uint64) bool {
	fetcher, ok := sm.transport.(stateSnapshotFetcher)
	if !ok {
		return false
	}
	realStore, ok := sm.store.(*RealBlockStore)
	if !ok || realStore == nil || realStore.dbManager == nil {
		return false
	}

	peers := sm.selectStateSyncPeers(targetHeight, sm.stateSyncPeers())
	if len(peers) == 0 {
		sm.Logger.Warn("[SyncManager] StateDB sync: no peers available for snapshot download")
		return false
	}

	var (
		shardResp *types.StateSnapshotShardsResponse
		metaPeer  types.NodeID
	)
	for _, peer := range peers {
		resp, err := fetcher.FetchStateSnapshotShards(peer, targetHeight)
		if err != nil {
			sm.Logger.Warn("[SyncManager] StateDB sync: fetch shards from %s failed: %v", peer, err)
			continue
		}
		if resp == nil {
			continue
		}
		shardResp = resp
		metaPeer = peer
		break
	}
	if shardResp == nil {
		sm.Logger.Warn("[SyncManager] StateDB sync: no shard metadata available")
		return false
	}

	snapshotHeight := shardResp.SnapshotHeight
	if snapshotHeight == 0 {
		snapshotHeight = targetHeight
	}

	type shardTask struct {
		shard string
		count int64
	}
	tasks := make([]shardTask, 0, len(shardResp.Shards))
	for _, shard := range shardResp.Shards {
		if shard.Shard == "" || shard.Count <= 0 {
			continue
		}
		tasks = append(tasks, shardTask{shard: shard.Shard, count: shard.Count})
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].count == tasks[j].count {
			return tasks[i].shard < tasks[j].shard
		}
		return tasks[i].count > tasks[j].count
	})

	if len(tasks) == 0 {
		sm.Logger.Info("[SyncManager] StateDB sync: metadata from %s has no shard items, applying empty snapshot at height %d", metaPeer, snapshotHeight)
		if err := realStore.dbManager.BuildStateSnapshotFromUpdates(context.Background(), snapshotHeight, nil); err != nil {
			sm.Logger.Warn("[SyncManager] StateDB sync: apply empty snapshot failed: %v", err)
			return false
		}
		return true
	}

	type shardResult struct {
		shard   string
		peer    types.NodeID
		updates []statedb.KVUpdate
		err     error
	}
	results := make(chan shardResult, len(tasks))
	sem := make(chan struct{}, sm.stateSyncShardConcurrency())
	pageSize := sm.stateSyncPageSize()

	for i, task := range tasks {
		primaryIdx := i % len(peers)
		go func(tk shardTask, startIdx int) {
			sem <- struct{}{}
			defer func() { <-sem }()

			updates, usedPeer, err := sm.fetchSnapshotShardWithFallback(fetcher, peers, startIdx, snapshotHeight, tk.shard, pageSize)
			results <- shardResult{
				shard:   tk.shard,
				peer:    usedPeer,
				updates: updates,
				err:     err,
			}
		}(task, primaryIdx)
	}

	allUpdates := make([]statedb.KVUpdate, 0, 4096)
	perPeerItems := make(map[types.NodeID]int)
	var firstErr error
	failed := 0

	for i := 0; i < len(tasks); i++ {
		res := <-results
		if res.err != nil {
			failed++
			if firstErr == nil {
				firstErr = res.err
			}
			sm.Logger.Warn("[SyncManager] StateDB sync: shard %s failed: %v", res.shard, res.err)
			continue
		}
		allUpdates = append(allUpdates, res.updates...)
		perPeerItems[res.peer] += len(res.updates)
	}

	if failed > 0 {
		sm.Logger.Warn("[SyncManager] StateDB sync: %d/%d shard(s) failed (first err: %v)", failed, len(tasks), firstErr)
		return false
	}

	if err := realStore.dbManager.BuildStateSnapshotFromUpdates(context.Background(), snapshotHeight, allUpdates); err != nil {
		sm.Logger.Warn("[SyncManager] StateDB sync: build local snapshot failed: %v", err)
		return false
	}

	for peer, cnt := range perPeerItems {
		sm.Logger.Info("[SyncManager] StateDB sync: peer %s served %d kv item(s)", peer, cnt)
	}
	sm.Logger.Info("[SyncManager] StateDB sync complete: height=%d shards=%d totalItems=%d metadataPeer=%s",
		snapshotHeight, len(tasks), len(allUpdates), metaPeer)
	return true
}

func (sm *SyncManager) fetchSnapshotShardWithFallback(
	fetcher stateSnapshotFetcher,
	peers []types.NodeID,
	startIdx int,
	snapshotHeight uint64,
	shard string,
	pageSize int,
) ([]statedb.KVUpdate, types.NodeID, error) {
	if len(peers) == 0 {
		return nil, "", fmt.Errorf("no peers available")
	}

	var lastErr error
	for i := 0; i < len(peers); i++ {
		peer := peers[(startIdx+i)%len(peers)]
		token := ""
		updates := make([]statedb.KVUpdate, 0, pageSize)

		for page := 0; page < 1_000_000; page++ {
			resp, err := fetcher.FetchStateSnapshotPage(peer, snapshotHeight, shard, pageSize, token)
			if err != nil {
				lastErr = err
				updates = nil
				break
			}
			if resp == nil {
				lastErr = fmt.Errorf("nil page response")
				updates = nil
				break
			}

			for _, item := range resp.Items {
				if item.Key == "" {
					continue
				}
				valCopy := make([]byte, len(item.Value))
				copy(valCopy, item.Value)
				updates = append(updates, statedb.KVUpdate{
					Key:     item.Key,
					Value:   valCopy,
					Deleted: false,
				})
			}

			if resp.NextPageToken == "" || resp.NextPageToken == token {
				return updates, peer, nil
			}
			token = resp.NextPageToken
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("all peers exhausted")
	}
	return nil, "", fmt.Errorf("fetch shard %s failed: %w", shard, lastErr)
}

func (sm *SyncManager) selectStateSyncPeers(targetHeight uint64, maxPeers int) []types.NodeID {
	if maxPeers <= 0 {
		maxPeers = 1
	}

	type peerHeight struct {
		id     types.NodeID
		height uint64
	}

	sm.Mu.RLock()
	candidates := make([]peerHeight, 0, len(sm.PeerHeights))
	for peerID, h := range sm.PeerHeights {
		if peerID == "" || peerID == sm.nodeID {
			continue
		}
		if targetHeight == 0 || h >= targetHeight {
			candidates = append(candidates, peerHeight{id: peerID, height: h})
		}
	}
	sm.Mu.RUnlock()

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].height == candidates[j].height {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].height > candidates[j].height
	})

	selected := make([]types.NodeID, 0, maxPeers)
	seen := make(map[types.NodeID]struct{}, maxPeers)
	for _, c := range candidates {
		if _, exists := seen[c.id]; exists {
			continue
		}
		seen[c.id] = struct{}{}
		selected = append(selected, c.id)
		if len(selected) >= maxPeers {
			return selected
		}
	}

	extraPeers := sm.transport.SamplePeers(sm.nodeID, maxPeers*2)
	for _, peer := range extraPeers {
		if peer == "" || peer == sm.nodeID {
			continue
		}
		if _, exists := seen[peer]; exists {
			continue
		}
		seen[peer] = struct{}{}
		selected = append(selected, peer)
		if len(selected) >= maxPeers {
			break
		}
	}
	return selected
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

	sm.Logger.Info("[SyncManager] Starting parallel sync: heights %d-%d across %d peers",
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
			sm.Logger.Debug("[SyncManager] Parallel shard: peer=%s heights=%d-%d shortMode=%v", p, s, e, shortMode)

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
		sm.Logger.Debug("[Sync] Quorum check: %d/%d nodes support height %d (required=%d)",
			len(sm.sampleResponses), sm.config.SampleSize, maxQuorumHeight, required)
		return maxQuorumHeight, true
	}

	return 0, false
}

func (sm *SyncManager) requestSync(fromHeight, toHeight uint64) {
	sm.Mu.Lock()
	if sm.Syncing {
		sm.Mu.Unlock()
		return
	}

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
		sm.transport.Send(targetPeer, types.Message{
			Type: types.MsgSyncRequest, From: sm.nodeID, SyncID: syncID,
			FromHeight: fromHeight, ToHeight: toHeight,
		})
	} else {
		sm.Mu.Lock()
		sm.Syncing = false
		delete(sm.SyncRequests, syncID)
		sm.Mu.Unlock()
	}
}

func (sm *SyncManager) HandleSyncRequest(msg types.Message) {
	blocks := sm.store.GetBlocksFromHeight(msg.FromHeight, msg.ToHeight)

	Logf("[Node %s] Received sync request from Node %s for heights %d-%d (found %d blocks, shortMode=%v)\n",
		sm.nodeID, msg.From, msg.FromHeight, msg.ToHeight, len(blocks), msg.SyncShortMode)

	// 强制要求：同步响应中的区块必须携带可验证的 VRF 签名集合。
	// 对缺少签名集合或序列化失败的区块直接剔除，不发送给对端。
	responseBlocks := blocks
	sigSets := make(map[uint64][]byte)
	if realStore, ok := sm.store.(*RealBlockStore); ok {
		filtered := make([]*types.Block, 0, len(blocks))
		for _, block := range blocks {
			if block == nil {
				continue
			}
			sigSet, exists := realStore.GetSignatureSet(block.Header.Height)
			if !exists || sigSet == nil {
				sm.Logger.Warn("[SyncManager] Skip sync block %s at height %d for %s: missing VRF signature set",
					block.ID, block.Header.Height, msg.From)
				continue
			}
			data, err := proto.Marshal(sigSet)
			if err != nil {
				sm.Logger.Warn("[SyncManager] Skip sync block %s at height %d for %s: marshal signature set failed: %v",
					block.ID, block.Header.Height, msg.From, err)
				continue
			}
			sigSets[block.Header.Height] = data
			filtered = append(filtered, block)
		}
		responseBlocks = filtered
	} else {
		sm.Logger.Warn("[SyncManager] Store %T has no signature-set support, returning empty strict sync response", sm.store)
		responseBlocks = nil
	}

	response := types.Message{
		Type:          types.MsgSyncResponse,
		From:          sm.nodeID,
		SyncID:        msg.SyncID,
		Blocks:        responseBlocks,
		FromHeight:    msg.FromHeight,
		ToHeight:      msg.ToHeight,
		SyncShortMode: msg.SyncShortMode,
	}

	if len(sigSets) > 0 {
		response.SignatureSets = sigSets
	}

	// 自适应传输模式
	if msg.SyncShortMode {
		// 短期落后模式：附带 ShortTxs 用于快速还原
		response.BlocksShortTxs = make(map[string][]byte)
		for _, block := range responseBlocks {
			if block == nil {
				continue
			}
			if cachedBlock, exists := GetCachedBlock(block.ID); exists && cachedBlock != nil {
				response.BlocksShortTxs[block.ID] = cachedBlock.ShortTxs
			}
		}
	} else {
		for _, block := range responseBlocks {
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

	// 强制要求：每个参与同步处理/最终化的高度都必须先通过 VRF 签名集合验证。
	verifiedSigSets := make(map[uint64]*pb.ConsensusSignatureSet)
	seenHeights := make(map[uint64]struct{})
	for _, block := range msg.Blocks {
		if block == nil {
			continue
		}
		h := block.Header.Height
		if _, seen := seenHeights[h]; seen {
			continue
		}
		seenHeights[h] = struct{}{}

		sigData, hasSig := msg.SignatureSets[h]
		if !hasSig || len(sigData) == 0 {
			sm.Logger.Warn("[SyncManager] Reject sync height %d from %s: missing VRF signature set", h, msg.From)
			continue
		}

		var sigSet pb.ConsensusSignatureSet
		if err := proto.Unmarshal(sigData, &sigSet); err != nil {
			sm.Logger.Warn("[SyncManager] Reject sync height %d from %s: decode signature set failed: %v", h, msg.From, err)
			continue
		}
		if !VerifySignatureSet(&sigSet, sm.config.SyncAlpha, sm.config.SyncBeta, sm.transport, sm.nodeID) {
			sm.Logger.Warn("[SyncManager] Reject sync height %d from %s: signature set verification failed", h, msg.From)
			continue
		}
		verifiedSigSets[h] = &sigSet
	}

	added := 0
	addErrs := 0
	var firstAddErr error
	var firstAddErrBlockID string
	for _, block := range msg.Blocks {
		if block == nil {
			continue
		}

		if _, ok := verifiedSigSets[block.Header.Height]; !ok {
			addErrs++
			if firstAddErr == nil {
				firstAddErr = fmt.Errorf("missing/invalid VRF signature set")
				firstAddErrBlockID = block.ID
			}
			sm.Logger.Warn("[SyncManager] Skip block %s at height %d from %s: missing/invalid VRF signature set",
				block.ID, block.Header.Height, msg.From)
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
				sm.Logger.Debug("[SyncManager] Block %s incomplete, queueing for async resolution", block.ID)
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
		sm.Logger.Warn("[Node %s] Sync received %d blocks, %d failed to add (first=%s err=%v)",
			sm.nodeID, len(msg.Blocks), addErrs, firstAddErrBlockID, firstAddErr)
	}
	if added == 0 && len(msg.Blocks) > 0 && addErrs == 0 {
		// 优化：根据当前状态调整日志级别。如果是早于或等于当前高度的同步，使用 Debug。
		_, acceptedHeight := sm.store.GetLastAccepted()
		if msg.ToHeight <= acceptedHeight {
			sm.Logger.Debug("[Node %s] Sync received %d blocks but none were new (heights %d-%d, current accepted=%d)",
				sm.nodeID, len(msg.Blocks), msg.FromHeight, msg.ToHeight, acceptedHeight)
		} else {
			sm.Logger.Warn("[Node %s] Sync received %d blocks but none were new (heights %d-%d, current accepted=%d)",
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

		// 强制要求：最终化前必须有已验证的签名集合。
		sigSet, hasSig := verifiedSigSets[nextHeight]
		if !hasSig || sigSet == nil {
			sm.Logger.Warn("[SyncManager] Missing verified signature set for height %d, stop fast-finalize", nextHeight)
			break
		}
		// 验证通过，存储到本地
		if realStore, ok := sm.store.(*RealBlockStore); ok {
			realStore.SetSignatureSet(nextHeight, sigSet)
		}

		// 尝试最终化该高度（失败会保持 lastAccepted 不变）
		if err := sm.store.SetFinalized(nextHeight, chosen.ID); err != nil {
			if errors.Is(err, ErrAlreadyFinalized) {
				acceptedID, acceptedHeight = sm.store.GetLastAccepted()
				continue
			}
			sm.Logger.Warn("[SyncManager] Failed to finalize block %s at height %d: %v", chosen.ID, nextHeight, err)
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
		sm.Logger.Info("[Node %s] ✅ Fast-finalized %d block(s) via sync (accepted=%d)",
			sm.nodeID, finalized, acceptedHeight)
		atomic.StoreUint32(&sm.consecutiveStallCount, 0) // 重置停滞计数
	} else if len(msg.Blocks) > 0 {
		// 高风险修复：检测同步停滞
		// 优化：只有当同步范围跨越了当前已接受高度，且未能推进时，才计入 stall
		if msg.ToHeight > acceptedHeight {
			stalls := atomic.AddUint32(&sm.consecutiveStallCount, 1)
			if stalls >= 3 {
				sm.Logger.Debug("[Node %s] ⚠️ Sync stalled for %d rounds at height %d, breaking pipeline and switching peers",
					sm.nodeID, stalls, acceptedHeight)
				sm.Mu.Lock()
				delete(sm.PeerHeights, types.NodeID(msg.From)) // 清理该 Peer 高度信息，强制重新采样
				sm.Syncing = false
				sm.Mu.Unlock()
				atomic.StoreUint32(&sm.consecutiveStallCount, 0)
				return
			}
		} else {
			sm.Logger.Debug("[Node %s] Sync response for heights %d-%d is older than accepted %d, ignoring stall check",
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
			sm.Logger.Debug("[Node %s] Skipping EventSyncComplete signal: still behind by %d blocks",
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
