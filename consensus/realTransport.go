// consensus/realTransport.go
package consensus

import (
	"context"
	"dex/db"
	"dex/interfaces"
	"dex/logs"
	"dex/sender"
	"dex/types"
	"dex/utils"
	"fmt"
	"sync"
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

	inbox          chan types.Message
	receiveQueue   chan types.Message
	receiveWorkers int

	stopOnce sync.Once
	stopChan chan struct{}
	wg       sync.WaitGroup
}

type NodeInfo struct {
	Address   string
	IP        string // 包含端口的完整地址，如 "127.0.0.1:6000"
	PublicKey string
	LastSeen  time.Time
	Index     uint64 // 节点索引
}

func NewRealTransport(nodeID types.NodeID, dbMgr *db.Manager, senderMgr *sender.SenderManager, ctx context.Context) interfaces.Transport {
	keyMgr := utils.GetKeyManager()
	rt := &RealTransport{
		nodeID:         nodeID,
		address:        keyMgr.GetAddress(),
		inbox:          make(chan types.Message, 100000),
		receiveQueue:   make(chan types.Message, 100000),
		receiveWorkers: 1000,
		dbManager:      dbMgr,
		senderManager:  senderMgr,
		ctx:            ctx,
		adapter:        NewConsensusAdapter(dbMgr),
		stopChan:       make(chan struct{}),
	}

	rt.startReceiveWorkers()
	return rt
}

// 发送消息到指定节点
func (t *RealTransport) Send(to types.NodeID, msg types.Message) error {
	targetIP, err := t.getNodeIP(to)
	if err != nil {
		logs.Debug("[RealTransport] Failed to get IP for node %s: %v", to, err)
		return err
	}

	switch msg.Type {
	case types.MsgPushQuery:
		return t.sendPushQuery(targetIP, msg)
	case types.MsgPullQuery:
		return t.sendPullQuery(targetIP, msg)
	case types.MsgChits:
		return t.sendChits(targetIP, msg)
	case types.MsgGet:
		return t.sendGet(targetIP, msg)
	case types.MsgPut:
		return t.sendPut(targetIP, msg)
	case types.MsgGossip:
		return t.sendGossip(targetIP, msg)
	case types.MsgSyncRequest:
		return t.sendSyncRequest(targetIP, msg)
	case types.MsgHeightQuery:
		return t.sendHeightQuery(targetIP, msg)
	case types.MsgSnapshotRequest:
		return t.sendSnapshotRequest(targetIP, msg)
	default:
		return fmt.Errorf("unknown message type: %v", msg.Type)
	}
}

func (t *RealTransport) getNodeIP(nodeID types.NodeID) (string, error) {
	acc, err := t.dbManager.GetAccount(string(nodeID))
	if err != nil || acc == nil || acc.Ip == "" {
		return "", fmt.Errorf("no IP for address %s", nodeID)
	}
	return acc.Ip, nil
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
	pq := &db.PullQuery{
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

func (t *RealTransport) sendGet(targetIP string, msg types.Message) error {
	// 使用新的PullBlockByID方法
	t.senderManager.PullBlockByID(targetIP, msg.BlockID, func(block *db.Block) {
		if block != nil {
			// 转换为types.Block
			consensusBlock, err := t.adapter.DBBlockToConsensus(block)
			if err != nil {
				logs.Error("[RealTransport] Failed to convert block: %v", err)
				return
			}

			// 构造Put消息响应
			putMsg := types.Message{
				Type:    types.MsgPut,
				From:    t.nodeID,
				Block:   consensusBlock,
				Height:  consensusBlock.Height,
				BlockID: consensusBlock.ID,
			}

			// 将消息放入接收队列
			if err := t.EnqueueReceivedMessage(putMsg); err != nil {
				logs.Debug("[RealTransport] Failed to enqueue Put message: %v", err)
			}

			logs.Debug("[RealTransport] Received block %s from %s", block.BlockHash, targetIP)
		}
	})
	return nil
}

func (t *RealTransport) sendPut(targetIP string, msg types.Message) error {
	if msg.Block == nil {
		return fmt.Errorf("no block data to send")
	}

	dbBlock := t.adapter.ConsensusBlockToDB(msg.Block, nil)
	return t.senderManager.SendBlock(targetIP, dbBlock)
}

func (t *RealTransport) sendGossip(targetIP string, msg types.Message) error {
	if msg.Block == nil {
		return fmt.Errorf("no block to gossip")
	}

	blockData, _ := msg.Block.ToMsg()
	t.senderManager.BroadcastToRandomMiners(blockData, 5)
	return nil
}

func (t *RealTransport) sendSyncRequest(targetIP string, msg types.Message) error {
	for h := msg.FromHeight; h <= msg.ToHeight; h++ {
		t.senderManager.PullBlock(targetIP, h, func(dbBlock *db.Block) {
			block, err := t.adapter.DBBlockToConsensus(dbBlock)
			if err != nil {
				logs.Error("[RealTransport] Failed to convert DB block: %v", err)
				return
			}

			t.inbox <- types.Message{
				Type:   types.MsgSyncResponse,
				From:   msg.From,
				Blocks: []*types.Block{block},
			}
		})
	}
	return nil
}

func (t *RealTransport) sendHeightQuery(targetIP string, msg types.Message) error {
	return t.senderManager.SendHeightQuery(targetIP, func(resp *db.HeightResponse) {
		t.inbox <- types.Message{
			Type:          types.MsgHeightResponse,
			From:          msg.From,
			Height:        resp.LastAcceptedHeight,
			CurrentHeight: resp.CurrentHeight,
		}
	})
}

func (t *RealTransport) sendSnapshotRequest(targetIP string, msg types.Message) error {
	t.senderManager.PullBlock(targetIP, msg.Height, func(block *db.Block) {
		logs.Info("[RealTransport] Received snapshot at height %d", block.Height)
	})
	return nil
}
func (t *RealTransport) GetReceiveQueueLen() int {
	return len(t.receiveQueue)
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
			logs.Warn("[RealTransport] Control message queue full, dropping from %s", msg.From)
			return fmt.Errorf("receive queue full for control message")
		}
	} else {
		// 数据面消息：非阻塞
		select {
		case t.receiveQueue <- msg:
			return nil
		default:
			logs.Debug("[RealTransport] Data message queue full, dropping from %s", msg.From)
			return fmt.Errorf("receive queue full for data message")
		}
	}
}

func (t *RealTransport) Receive() <-chan types.Message {
	return t.inbox
}

func (t *RealTransport) Broadcast(msg types.Message, peers []types.NodeID) {
	for _, peer := range peers {
		go func(p types.NodeID) {
			if err := t.Send(p, msg); err != nil {
				logs.Debug("[RealTransport] Failed to send to peer %s: %v", p, err)
			}
		}(peer)
	}
}

func (t *RealTransport) SamplePeers(exclude types.NodeID, count int) []types.NodeID {
	miners, err := t.dbManager.GetRandomMinersFast(count + 1)
	if err != nil {
		logs.Error("[RealTransport] Failed to get random miners: %v", err)
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

func (t *RealTransport) startReceiveWorkers() {
	for i := 0; i < t.receiveWorkers; i++ {
		t.wg.Add(1)
		go t.receiveWorker(i)
	}

	logs.Info("[RealTransport] Started %d receive workers", t.receiveWorkers)
}

func (t *RealTransport) receiveWorker(workerID int) {
	defer t.wg.Done()

	for {
		select {
		case <-t.stopChan:
			return
		case msg := <-t.receiveQueue:
			if err := t.preprocessMessage(&msg); err != nil {
				logs.Debug("[RealTransport] Worker %d: Invalid message from %s: %v",
					workerID, msg.From, err)
				continue
			}

			select {
			case t.inbox <- msg:
				logs.Trace("[RealTransport] Worker %d: Processed message type %d from %s",
					workerID, msg.Type, msg.From)
			case <-time.After(5 * time.Second):
				logs.Warn("[RealTransport] Worker %d: Timeout sending to inbox, dropping message from %s",
					workerID, msg.From)
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
		logs.Info("[RealTransport] Closed")
	})
}
