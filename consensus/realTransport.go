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

	"google.golang.org/protobuf/proto"
)

// RealTransport 实现基于HTTP/3的真实网络传输
type RealTransport struct {
	nodeID    types.NodeID
	address   string // 本节点的区块链地址
	inbox     chan types.Message
	dbManager *db.Manager
	ctx       context.Context
	mu        sync.RWMutex

	// 节点ID到IP地址的映射缓存
	nodeAddressCache map[types.NodeID]string
}

// NewRealTransport 创建真实的网络传输层
func NewRealTransport(nodeID types.NodeID, dbMgr *db.Manager, ctx context.Context) interfaces.Transport {
	keyMgr := utils.GetKeyManager()
	return &RealTransport{
		nodeID:           nodeID,
		address:          keyMgr.GetAddress(),
		inbox:            make(chan types.Message, 100000),
		dbManager:        dbMgr,
		ctx:              ctx,
		nodeAddressCache: make(map[types.NodeID]string),
	}
}

// Send 发送消息到指定节点
func (t *RealTransport) Send(to types.NodeID, msg types.Message) error {
	// 获取目标节点的地址
	targetAddr, err := t.getNodeAddress(to)
	if err != nil {
		logs.Debug("[RealTransport] Failed to get address for node %d: %v", to, err)
		return err
	}

	// 根据消息类型调用不同的发送方法
	switch msg.Type {
	case types.MsgPushQuery:
		return t.sendPushQuery(targetAddr, msg)
	case types.MsgPullQuery:
		return t.sendPullQuery(targetAddr, msg)
	case types.MsgChits:
		return t.sendChits(targetAddr, msg)
	case types.MsgGet:
		return t.sendGetData(targetAddr, msg)
	case types.MsgPut:
		return t.sendPutData(targetAddr, msg)
	case types.MsgGossip:
		return t.sendGossip(targetAddr, msg)
	case types.MsgSyncRequest:
		return t.sendSyncRequest(targetAddr, msg)
	case types.MsgHeightQuery:
		return t.sendHeightQuery(targetAddr, msg)
	case types.MsgSnapshotRequest:
		return t.sendSnapshotRequest(targetAddr, msg)
	default:
		return fmt.Errorf("unknown message type: %v", msg.Type)
	}
}

// Receive 返回接收消息的通道
func (t *RealTransport) Receive() <-chan types.Message {
	return t.inbox
}

// Broadcast 广播消息到多个节点
func (t *RealTransport) Broadcast(msg types.Message, peers []types.NodeID) {
	for _, peer := range peers {
		go func(p types.NodeID) {
			if err := t.Send(p, msg); err != nil {
				logs.Debug("[RealTransport] Failed to send to peer %d: %v", p, err)
			}
		}(peer)
	}
}

// SamplePeers 随机采样K个节点
func (t *RealTransport) SamplePeers(exclude types.NodeID, count int) []types.NodeID {
	// 从数据库获取活跃的矿工节点
	miners, err := db.GetRandomMinersFast(count + 1)
	if err != nil {
		logs.Error("[RealTransport] Failed to get random miners: %v", err)
		return []types.NodeID{}
	}

	var peers []types.NodeID
	for _, miner := range miners {
		// 使用矿工的Index作为NodeID
		nodeID := types.NodeID(miner.Index)

		// 排除自己
		if nodeID == exclude {
			continue
		}

		// 缓存IP地址映射，加速后续查询
		if miner.Ip != "" {
			t.mu.Lock()
			t.nodeAddressCache[nodeID] = miner.Ip
			t.mu.Unlock()
		}

		peers = append(peers, nodeID)

		if len(peers) >= count {
			break
		}
	}

	return peers
}

// sendPushQuery 发送PushQuery消息
func (t *RealTransport) sendPushQuery(targetAddr string, msg types.Message) error {
	// 准备容器数据
	var container []byte
	containerIsBlock := false

	if msg.Block != nil {
		// 如果有完整区块，序列化它
		if cachedBlock, exists := GetCachedBlock(msg.Block.ID); exists {
			// 有缓存的完整区块数据
			if len(cachedBlock.Body) < 2500 {
				// 交易数量少于2500，发送完整数据
				data, _ := proto.Marshal(cachedBlock)
				container = data
				containerIsBlock = true
			} else {
				// 交易数量多，只发送短哈希
				container = cachedBlock.ShortTxs
				containerIsBlock = false
			}
		} else {
			// 没有缓存，发送简单数据
			container = []byte(msg.Block.Data)
		}
	}

	pq := &db.PushQuery{
		Address:          t.address,
		Deadline:         uint64(time.Now().Add(3 * time.Second).UnixNano()),
		ContainerIsBlock: containerIsBlock,
		Container:        container,
		RequestedHeight:  msg.Height,
		BlockId:          msg.BlockID,
	}

	// 使用sender模块发送
	sender.PushQuery(targetAddr, pq, func(chits *db.Chits) {
		// 处理返回的Chits
		t.handleReceivedChits(chits, msg.From)
	})

	return nil
}

// sendPullQuery 发送PullQuery消息
func (t *RealTransport) sendPullQuery(targetAddr string, msg types.Message) error {
	pq := &db.PullQuery{
		Address:         t.address,
		Deadline:        uint64(time.Now().Add(3 * time.Second).UnixNano()),
		BlockId:         msg.BlockID,
		RequestedHeight: msg.Height,
	}

	sender.PullQuery(targetAddr, pq, func(chits *db.Chits) {
		t.handleReceivedChits(chits, msg.From)
	})

	return nil
}

// sendChits 发送Chits响应
func (t *RealTransport) sendChits(targetAddr string, msg types.Message) error {
	// 这个需要在handler中实现，作为对Query的响应
	// 这里暂时留空，因为Chits通常是作为响应自动发送的
	return nil
}

// sendGetData 请求完整交易数据
func (t *RealTransport) sendGetData(targetAddr string, msg types.Message) error {
	sender.PullTx(targetAddr, msg.BlockID, func(anyTx *db.AnyTx) {
		// 处理接收到的交易
		logs.Debug("[RealTransport] Received tx %s from %s", anyTx.GetTxId(), targetAddr)
	})
	return nil
}

// sendPutData 发送完整数据
func (t *RealTransport) sendPutData(targetAddr string, msg types.Message) error {
	if msg.Block == nil {
		return fmt.Errorf("no block data to send")
	}

	// 将Block转换为AnyTx格式（这里需要根据实际情况调整）
	// 或者直接广播区块数据
	blockData, _ := msg.Block.ToMsg()
	sender.BroadcastToRandomMiners(blockData, 1)

	return nil
}

// sendGossip 发送Gossip消息
func (t *RealTransport) sendGossip(targetAddr string, msg types.Message) error {
	if msg.Block == nil {
		return fmt.Errorf("no block to gossip")
	}

	// 序列化并广播
	blockData, _ := msg.Block.ToMsg()
	sender.BroadcastToRandomMiners(blockData, 5)

	return nil
}

// sendSyncRequest 发送同步请求
func (t *RealTransport) sendSyncRequest(targetAddr string, msg types.Message) error {
	// 使用PullBlock来同步区块
	for h := msg.FromHeight; h <= msg.ToHeight; h++ {
		sender.PullBlock(targetAddr, h, func(block *db.Block) {
			// 将接收到的区块转换为Message并放入inbox
			t.inbox <- types.Message{
				Type:   types.MsgSyncResponse,
				From:   msg.From,
				Blocks: t.convertDBBlocksToTypes([]*db.Block{block}),
			}
		})
	}
	return nil
}

// sendHeightQuery 查询节点高度
func (t *RealTransport) sendHeightQuery(targetAddr string, msg types.Message) error {
	// 这个需要通过特定的RPC调用实现
	// 暂时使用PullBlock的方式间接获取
	return nil
}

// sendSnapshotRequest 请求快照
func (t *RealTransport) sendSnapshotRequest(targetAddr string, msg types.Message) error {
	// 使用PullBlock获取特定高度的数据作为快照
	sender.PullBlock(targetAddr, msg.Height, func(block *db.Block) {
		logs.Info("[RealTransport] Received snapshot at height %d", block.Height)
	})
	return nil
}

// handleReceivedChits 处理接收到的Chits
func (t *RealTransport) handleReceivedChits(chits *db.Chits, from types.NodeID) {
	msg := types.Message{
		Type:              types.MsgChits,
		From:              from,
		PreferredID:       chits.PreferredBlock,
		AcceptedID:        chits.AcceptedBlock,
		PreferredIDHeight: chits.PreferredBlockAtHeight,
		AcceptedHeight:    chits.AcceptedHeight,
	}

	select {
	case t.inbox <- msg:
	case <-t.ctx.Done():
	}
}

// getNodeAddress 获取节点的IP地址
func (t *RealTransport) getNodeAddress(nodeID types.NodeID) (string, error) {
	t.mu.RLock()
	if addr, exists := t.nodeAddressCache[nodeID]; exists {
		t.mu.RUnlock()
		return addr, nil
	}
	t.mu.RUnlock()

	// 从数据库通过节点索引获取Account
	account, err := db.GetAccount(t.dbManager, string(nodeID))
	if err != nil {
		return "", fmt.Errorf("failed to get account for nodeID %d: %v", nodeID, err)
	}

	// 检查IP是否存在
	if account.Ip == "" {
		return "", fmt.Errorf("node %d has no IP address configured", nodeID)
	}

	// 缓存IP地址以加速后续查询
	t.mu.Lock()
	t.nodeAddressCache[nodeID] = account.Ip
	t.mu.Unlock()

	return account.Ip, nil
}

// convertDBBlocksToTypes 将数据库区块转换为类型区块
func (t *RealTransport) convertDBBlocksToTypes(dbBlocks []*db.Block) []*types.Block {
	var blocks []*types.Block
	for _, dbBlock := range dbBlocks {
		block := &types.Block{
			ID:       dbBlock.BlockHash,
			Height:   dbBlock.Height,
			ParentID: dbBlock.PrevBlockHash,
			Data:     fmt.Sprintf("TxCount: %d", len(dbBlock.Body)),
			Proposer: "0", // 需要从Miner字段解析
		}
		blocks = append(blocks, block)
	}
	return blocks
}
