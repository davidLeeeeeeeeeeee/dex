// consensus/realTransport.go
package consensus

import (
	"context"
	"dex/db"
	"dex/interfaces"
	"dex/logs"
	"dex/pb"
	"dex/sender"
	"dex/stats"
	"dex/types"
	"dex/utils"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// RealTransport 实现基于HTTP/3的真实网络传输
type RealTransport struct {
	nodeID        types.NodeID
	address       string
	dbManager     *db.Manager
	ctx           context.Context
	mu            sync.RWMutex
	senderManager *sender.SenderManager
	adapter       *ConsensusAdapter
	Logger        logs.Logger

	inbox          chan types.Message
	receiveQueue   chan types.Message
	receiveWorkers int

	stopOnce       sync.Once
	stopChan       chan struct{}
	wg             sync.WaitGroup
	Stats          *stats.Stats
	packetLossRate float64       // 丢包率，范围 0.0 到 1.0
	minLatency     time.Duration // 最小延迟
	maxLatency     time.Duration // 最大延迟
	nodeIPCache    map[types.NodeID]string
	cacheMu        sync.RWMutex

	controlEnqueueTimeoutDrops atomic.Uint64
	dataEnqueueFullDrops       atomic.Uint64
	inboxForwardTimeoutDrops   atomic.Uint64
	preprocessInvalidDrops     atomic.Uint64
	inboxTimeoutByType         sync.Map // map[string]*atomic.Uint64
	inboxTimeoutBySender       sync.Map // map[string]*atomic.Uint64
}

type RealTransportRuntimeStats struct {
	ControlEnqueueTimeoutDrops uint64
	DataEnqueueFullDrops       uint64
	InboxForwardTimeoutDrops   uint64
	PreprocessInvalidDrops     uint64
}

type RealTransportTopDrop struct {
	Key   string
	Count uint64
}

type NodeInfo struct {
	Address   string
	IP        string // 包含端口的完整地址，如 "127.0.0.1:6000"
	PublicKey string
	LastSeen  time.Time
	Index     uint64 // 节点索引
}

func NewRealTransport(nodeID types.NodeID, dbMgr *db.Manager, senderMgr *sender.SenderManager, ctx context.Context) interfaces.Transport {
	return NewRealTransportWithSimulation(nodeID, dbMgr, senderMgr, ctx, 0.0, 0, 0)
}

// NewRealTransportWithPacketLoss 创建带丢包率模拟的 RealTransport（兼容旧接口）
// packetLossRate: 丢包率，范围 0.0 到 1.0，例如 0.1 表示 10% 丢包率
func NewRealTransportWithPacketLoss(nodeID types.NodeID, dbMgr *db.Manager, senderMgr *sender.SenderManager, ctx context.Context, packetLossRate float64) interfaces.Transport {
	return NewRealTransportWithSimulation(nodeID, dbMgr, senderMgr, ctx, packetLossRate, 0, 0)
}

// NewRealTransportWithSimulation 创建带网络模拟的 RealTransport
// packetLossRate: 丢包率，范围 0.0 到 1.0，例如 0.1 表示 10% 丢包率
// minLatency, maxLatency: 随机延迟范围，例如 100ms 到 200ms
func NewRealTransportWithSimulation(nodeID types.NodeID, dbMgr *db.Manager, senderMgr *sender.SenderManager, ctx context.Context, packetLossRate float64, minLatency, maxLatency time.Duration) interfaces.Transport {
	keyMgr := utils.GetKeyManager()
	rt := &RealTransport{
		nodeID:         nodeID,
		address:        keyMgr.GetAddress(),
		inbox:          make(chan types.Message, 1000),
		receiveQueue:   make(chan types.Message, 1000),
		receiveWorkers: 64, // 1000 个 worker 太重了，降为 64 核心数相关的水平
		dbManager:      dbMgr,
		senderManager:  senderMgr,
		ctx:            ctx,
		adapter:        NewConsensusAdapter(dbMgr),
		stopChan:       make(chan struct{}),
		Stats:          stats.NewStats(),
		packetLossRate: packetLossRate,
		minLatency:     minLatency,
		maxLatency:     maxLatency,
		nodeIPCache:    make(map[types.NodeID]string),
		Logger:         logs.NewNodeLogger(string(nodeID)+":transport", 500),
	}

	rt.startReceiveWorkers()
	return rt
}

// 发送消息到指定节点
func (t *RealTransport) Send(to types.NodeID, msg types.Message) error {
	// 模拟网络丢包
	if t.packetLossRate > 0 && rand.Float64() < t.packetLossRate {
		// 丢包了，直接返回，不发送消息
		// 发送方不知道丢包，返回nil表示"发送成功"
		t.Logger.Trace("[RealTransport] Packet dropped: %s -> %s, MsgType=%v", t.nodeID, to, msg.Type)
		return nil
	}

	// 模拟网络延迟 (100~200ms 随机延迟) - 异步执行
	if t.maxLatency > 0 && t.maxLatency > t.minLatency {
		delay := t.minLatency + time.Duration(rand.Int63n(int64(t.maxLatency-t.minLatency)))
		go func() {
			time.Sleep(delay)
			t.doSend(to, msg)
		}()
		return nil
	}

	// 无延迟配置，直接发送
	return t.doSend(to, msg)
}

// doSend 实际执行发送逻辑
func (t *RealTransport) doSend(to types.NodeID, msg types.Message) error {
	start := time.Now()
	t.Stats.RecordAPICall(string(msg.Type))
	targetIP, err := t.getNodeIP(to)
	if err != nil {
		t.Logger.Debug("[RealTransport] Failed to get IP for node %s: %v", to, err)
		return err
	}

	var errSend error
	switch msg.Type {
	case types.MsgPushQuery:
		errSend = t.sendPushQuery(targetIP, msg)
	case types.MsgPullQuery:
		errSend = t.sendPullQuery(targetIP, msg)
	case types.MsgChits:
		errSend = t.sendChits(targetIP, msg)
	case types.MsgGet:
		errSend = t.sendGet(to, targetIP, msg)
	case types.MsgPut:
		errSend = t.sendBlock(targetIP, msg)
	case types.MsgGossip:
		errSend = t.sendGossip(targetIP, msg)
	case types.MsgSyncRequest:
		errSend = t.sendSyncRequest(to, targetIP, msg)
	case types.MsgHeightQuery:
		errSend = t.sendHeightQuery(to, targetIP, msg)

	default:
		errSend = fmt.Errorf("unknown message type: %v", msg.Type)
	}

	// 如果耗时超过 50ms，记录跟踪
	if duration := time.Since(start); duration > 50*time.Millisecond {
		t.Logger.Debug("[RealTransport] Send Latency: Type=%v, to=%s, duration=%v", msg.Type, to, duration)
	}

	return errSend
}

func (t *RealTransport) getNodeIP(nodeID types.NodeID) (string, error) {
	// 快速跳过空 NodeID
	if nodeID == "" {
		return "", fmt.Errorf("empty nodeID")
	}

	// 1. 尝试从缓存读取
	t.cacheMu.RLock()
	ip, ok := t.nodeIPCache[nodeID]
	t.cacheMu.RUnlock()
	if ok {
		return ip, nil
	}

	// 2. 缓存没有，查库
	acc, err := t.dbManager.GetAccount(string(nodeID))
	if err == nil && acc != nil && acc.Ip != "" {
		// 存入缓存
		t.cacheMu.Lock()
		t.nodeIPCache[nodeID] = acc.Ip
		t.cacheMu.Unlock()
		return acc.Ip, nil
	}

	// 3. 尝试解析为矿工索引（兼容模拟环境）
	nodeIDStr := string(nodeID)
	if len(nodeIDStr) < 5 {
		if index, parseErr := strconv.ParseUint(nodeIDStr, 10, 64); parseErr == nil {
			minerAcc, minerErr := t.dbManager.GetMinerByIndex(index)
			if minerErr == nil && minerAcc != nil && minerAcc.Ip != "" {
				t.cacheMu.Lock()
				t.nodeIPCache[nodeID] = minerAcc.Ip
				t.cacheMu.Unlock()
				return minerAcc.Ip, nil
			}
		}
	}

	return "", fmt.Errorf("no IP for address %s", nodeID)
}

// sendPushQuery 使用senderManager发送
func (t *RealTransport) sendPushQuery(targetIP string, msg types.Message) error {
	pq, err := t.adapter.ConsensusMessageToPushQuery(msg, t.address)
	if err != nil {
		return fmt.Errorf("failed to convert message to PushQuery: %v", err)
	}

	t.senderManager.PushQuery(targetIP, pq)

	return nil
}

// 使用senderManager发送
func (t *RealTransport) sendPullQuery(targetIP string, msg types.Message) error {
	pq := &pb.PullQuery{
		RequestId:       msg.RequestID,
		Address:         t.address,
		Deadline:        t.adapter.calculateDeadline(3),
		BlockId:         msg.BlockID,
		RequestedHeight: msg.Height,
	}

	t.senderManager.PullQuery(targetIP, pq)

	return nil
}

func (t *RealTransport) sendChits(targetIP string, msg types.Message) error {
	chits := t.adapter.ConsensusMessageToChits(msg)
	return t.senderManager.SendChits(targetIP, chits)
}

func (t *RealTransport) sendGet(peer types.NodeID, targetIP string, msg types.Message) error {
	// 使用新的PullBlockByID方法
	t.senderManager.PullGet(targetIP, msg.BlockID, func(block *pb.Block) {
		if block != nil {
			CacheBlock(block)
			// 转换为types.Block
			consensusBlock, err := t.adapter.DBBlockToConsensus(block)
			if err != nil {
				t.Logger.Error("[RealTransport] Failed to convert block: %v", err)
				return
			}

			// 构造Put消息响应
			putMsg := types.Message{
				RequestID: msg.RequestID,
				Type:      types.MsgPut,
				From:      peer,
				Block:     consensusBlock,
				Height:    consensusBlock.Header.Height,
				BlockID:   consensusBlock.ID,
				ShortTxs:  block.ShortTxs,
			}

			// 将消息放入接收队列
			if err := t.EnqueueReceivedMessage(putMsg); err != nil {
				t.Logger.Debug("[RealTransport] Failed to enqueue Put message: %v", err)
			}

			t.Logger.Debug("[RealTransport] Received block %s from %s", block.BlockHash, targetIP)
		}
	})
	return nil
}

func (t *RealTransport) sendBlock(targetIP string, msg types.Message) error {
	if msg.Block == nil {
		return fmt.Errorf("no block data to send")
	}

	dbBlock := t.adapter.ConsensusBlockToDB(msg.Block, nil)
	if dbBlock != nil && len(dbBlock.ShortTxs) == 0 && len(msg.ShortTxs) > 0 {
		dbBlock.ShortTxs = msg.ShortTxs
	}
	return t.senderManager.SendBlock(targetIP, dbBlock)
}

// 一个专门用于 Gossip 传输的类型

func (t *RealTransport) sendGossip(targetIP string, msg types.Message) error {
	if msg.Block == nil {
		return fmt.Errorf("no block data to send")
	}

	payload := &types.GossipPayload{
		Block:     t.adapter.ConsensusBlockToDB(msg.Block, nil),
		RequestID: msg.RequestID,
	}
	if payload.Block != nil && len(payload.Block.ShortTxs) == 0 && len(msg.ShortTxs) > 0 {
		payload.Block.ShortTxs = msg.ShortTxs
	}
	return t.senderManager.BroadcastGossipToTarget(targetIP, payload)
}
func (t *RealTransport) sendSyncRequest(to types.NodeID, targetIP string, msg types.Message) error {
	return t.senderManager.SendSyncRequest(targetIP, msg.FromHeight, msg.ToHeight, func(dbBlocks []*pb.Block) {
		var blocks []*types.Block
		for _, dbBlock := range dbBlocks {
			block, err := t.adapter.DBBlockToConsensus(dbBlock)
			if err == nil {
				blocks = append(blocks, block)
			}
		}

		if len(blocks) > 0 {
			t.inbox <- types.Message{
				Type:       types.MsgSyncResponse,
				From:       to,
				SyncID:     msg.SyncID,
				RequestID:  msg.RequestID,
				Blocks:     blocks,
				FromHeight: msg.FromHeight,
				ToHeight:   msg.ToHeight,
			}
		}
	})
}

func (t *RealTransport) sendHeightQuery(to types.NodeID, targetIP string, msg types.Message) error {
	return t.senderManager.SendHeightQuery(targetIP, func(resp *pb.HeightResponse) {
		if resp == nil {
			return
		}
		t.inbox <- types.Message{
			Type:          types.MsgHeightResponse,
			From:          to,
			Height:        resp.LastAcceptedHeight,
			CurrentHeight: resp.CurrentHeight,
			RequestID:     msg.RequestID,
		}
	})
}

func (t *RealTransport) FetchStateSnapshotShards(peer types.NodeID, targetHeight uint64) (*types.StateSnapshotShardsResponse, error) {
	if t.senderManager == nil {
		return nil, fmt.Errorf("sender manager is not initialized")
	}
	return t.senderManager.FetchStateSnapshotShards(string(peer), targetHeight)
}

func (t *RealTransport) FetchStateSnapshotPage(peer types.NodeID, snapshotHeight uint64, shard string, pageSize int, pageToken string) (*types.StateSnapshotPageResponse, error) {
	if t.senderManager == nil {
		return nil, fmt.Errorf("sender manager is not initialized")
	}
	return t.senderManager.FetchStateSnapshotPage(string(peer), snapshotHeight, shard, pageSize, pageToken)
}

func (t *RealTransport) GetReceiveQueueLen() int {
	return len(t.receiveQueue)
}

// GetChannelStats 返回 Transport 的 channel 状态
func (t *RealTransport) GetChannelStats() []stats.ChannelStat {
	return []stats.ChannelStat{
		stats.NewChannelStat("inbox", "Transport", len(t.inbox), cap(t.inbox)),
		stats.NewChannelStat("receiveQueue", "Transport", len(t.receiveQueue), cap(t.receiveQueue)),
	}
}
func (t *RealTransport) EnqueueReceivedMessage(msg types.Message) error {
	// 根据消息类型判断优先级
	isControlMessage := false
	switch msg.Type {
	case types.MsgPushQuery, types.MsgPullQuery, types.MsgChits:
		isControlMessage = true
	}

	if isControlMessage {
		// 控制面消息：短暂等待
		select {
		case t.receiveQueue <- msg:
			return nil
		case <-time.After(50 * time.Millisecond):
			t.controlEnqueueTimeoutDrops.Add(1)
			t.Logger.Warn(
				"[RealTransport] Control message queue full local=%s type=%s from=%s receiveQueue=%d/%d",
				t.nodeID, msg.Type, msg.From, len(t.receiveQueue), cap(t.receiveQueue),
			)
			return fmt.Errorf("receive queue full for control message")
		}
	} else {
		// 数据面消息：非阻塞
		select {
		case t.receiveQueue <- msg:
			return nil
		default:
			t.dataEnqueueFullDrops.Add(1)
			t.Logger.Debug(
				"[RealTransport] Data message queue full local=%s type=%s from=%s receiveQueue=%d/%d",
				t.nodeID, msg.Type, msg.From, len(t.receiveQueue), cap(t.receiveQueue),
			)
			return fmt.Errorf("receive queue full for data message")
		}
	}
}

func (t *RealTransport) Receive() <-chan types.Message {
	return t.inbox
}

func (t *RealTransport) Broadcast(msg types.Message, peers []types.NodeID) {
	// 共识查询消息改为同步发起，给发送端提供自然背压，避免每个 peer 启 goroutine 造成风暴。
	isConsensusQuery := msg.Type == types.MsgPullQuery || msg.Type == types.MsgPushQuery
	for _, peer := range peers {
		if isConsensusQuery {
			if err := t.Send(peer, msg); err != nil {
				t.Logger.Debug("[RealTransport] Failed to send to peer %s: %v", peer, err)
			}
			continue
		}
		go func(p types.NodeID) {
			if err := t.Send(p, msg); err != nil {
				t.Logger.Debug("[RealTransport] Failed to send to peer %s: %v", p, err)
			}
		}(peer)
	}
}

func (t *RealTransport) SamplePeers(exclude types.NodeID, count int) []types.NodeID {
	miners, err := t.dbManager.GetRandomMinersFast(count + 1)
	if err != nil {
		t.Logger.Error("[RealTransport] Failed to get random miners: %v", err)
		return nil
	}

	peers := make([]types.NodeID, 0, count)
	for _, m := range miners {
		id := types.NodeID(m.Address)
		if id == exclude {
			continue
		}
		peers = append(peers, id)
		if len(peers) >= count {
			break
		}
	}
	return peers
}

// GetAllPeers 返回所有已知矿工节点（不含 exclude），用于 VRF 确定性采样
func (t *RealTransport) GetAllPeers(exclude types.NodeID) []types.NodeID {
	allMiners, err := t.dbManager.GetRandomMinersFast(1000) // 取足够多的矿工
	if err != nil {
		t.Logger.Error("[RealTransport] GetAllPeers failed: %v", err)
		return nil
	}

	peers := make([]types.NodeID, 0, len(allMiners))
	for _, m := range allMiners {
		id := types.NodeID(m.Address)
		if id != exclude {
			peers = append(peers, id)
		}
	}
	return peers
}

func (t *RealTransport) startReceiveWorkers() {
	for i := 0; i < t.receiveWorkers; i++ {
		t.wg.Add(1)
		go t.receiveWorker(i)
	}

	t.Logger.Info("[RealTransport] Started %d receive workers", t.receiveWorkers)
}

func (t *RealTransport) receiveWorker(workerID int) {
	defer t.wg.Done()

	for {
		select {
		case <-t.stopChan:
			return
		case msg := <-t.receiveQueue:
			if err := t.preprocessMessage(&msg); err != nil {
				t.preprocessInvalidDrops.Add(1)
				t.Logger.Debug("[RealTransport] Worker %d: Invalid message from %s: %v",
					workerID, msg.From, err)
				continue
			}

			select {
			case t.inbox <- msg:
				t.Logger.Trace("[RealTransport] Worker %d: Processed message type %d from %s",
					workerID, msg.Type, msg.From)
			case <-time.After(5 * time.Second):
				t.inboxForwardTimeoutDrops.Add(1)
				t.addCounter(&t.inboxTimeoutByType, normalizeCounterKey(string(msg.Type)))
				t.addCounter(&t.inboxTimeoutBySender, normalizeCounterKey(string(msg.From)))
				t.Logger.Warn(
					"[RealTransport] Worker %d: Timeout sending to inbox local=%s type=%s from=%s request=%d block=%s receiveQueue=%d/%d inbox=%d/%d",
					workerID,
					t.nodeID,
					msg.Type,
					msg.From,
					msg.RequestID,
					msg.BlockID,
					len(t.receiveQueue), cap(t.receiveQueue),
					len(t.inbox), cap(t.inbox),
				)
			case <-t.stopChan:
				return
			}
		}
	}
}

func (t *RealTransport) preprocessMessage(msg *types.Message) error {
	if msg == nil {
		return fmt.Errorf("nil message")
	}
	if msg.From == "" {
		return fmt.Errorf("empty sender ID")
	}
	return nil
}

func (t *RealTransport) Close() {
	t.stopOnce.Do(func() {
		close(t.stopChan)
		t.wg.Wait()
		close(t.receiveQueue)
		close(t.inbox)
		t.Logger.Info("[RealTransport] Closed")
	})
}

func (t *RealTransport) GetRuntimeStats() RealTransportRuntimeStats {
	if t == nil {
		return RealTransportRuntimeStats{}
	}
	return RealTransportRuntimeStats{
		ControlEnqueueTimeoutDrops: t.controlEnqueueTimeoutDrops.Load(),
		DataEnqueueFullDrops:       t.dataEnqueueFullDrops.Load(),
		InboxForwardTimeoutDrops:   t.inboxForwardTimeoutDrops.Load(),
		PreprocessInvalidDrops:     t.preprocessInvalidDrops.Load(),
	}
}

func (t *RealTransport) GetTopInboxTimeoutByType(reset bool, topN int) []RealTransportTopDrop {
	if t == nil {
		return nil
	}
	return snapshotCounterMap(&t.inboxTimeoutByType, reset, topN)
}

func (t *RealTransport) GetTopInboxTimeoutBySender(reset bool, topN int) []RealTransportTopDrop {
	if t == nil {
		return nil
	}
	return snapshotCounterMap(&t.inboxTimeoutBySender, reset, topN)
}

func (t *RealTransport) addCounter(counterMap *sync.Map, key string) {
	if key == "" {
		return
	}
	if existing, ok := counterMap.Load(key); ok {
		if counter, ok := existing.(*atomic.Uint64); ok && counter != nil {
			counter.Add(1)
			return
		}
	}

	counter := &atomic.Uint64{}
	actual, _ := counterMap.LoadOrStore(key, counter)
	if typed, ok := actual.(*atomic.Uint64); ok && typed != nil {
		typed.Add(1)
	}
}

func snapshotCounterMap(counterMap *sync.Map, reset bool, topN int) []RealTransportTopDrop {
	if counterMap == nil {
		return nil
	}

	result := make([]RealTransportTopDrop, 0, 8)
	counterMap.Range(func(k, v any) bool {
		key, ok := k.(string)
		if !ok {
			return true
		}
		counter, ok := v.(*atomic.Uint64)
		if !ok || counter == nil {
			return true
		}

		var value uint64
		if reset {
			value = counter.Swap(0)
		} else {
			value = counter.Load()
		}
		if value > 0 {
			result = append(result, RealTransportTopDrop{Key: key, Count: value})
		}
		return true
	})

	if len(result) == 0 {
		return nil
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Key < result[j].Key
	})

	if topN > 0 && len(result) > topN {
		result = result[:topN]
	}
	return result
}

func normalizeCounterKey(key string) string {
	if key == "" {
		return "unknown"
	}
	return key
}
