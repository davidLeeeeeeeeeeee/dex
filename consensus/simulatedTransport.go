package consensus

import (
	"context"
	"dex/interfaces"
	"dex/types"
	"math/rand"
	"time"
)

type SimulatedTransport struct {
	nodeID         types.NodeID
	ctrlInbox      chan types.Message
	dataInbox      chan types.Message
	network        *NetworkManager
	ctx            context.Context
	networkLatency time.Duration
	packetLossRate float64 // 丢包率
}

func NewSimulatedTransport(nodeID types.NodeID, network *NetworkManager, ctx context.Context, latency time.Duration) interfaces.Transport {
	return &SimulatedTransport{
		nodeID:         nodeID,
		ctrlInbox:      make(chan types.Message, 20000),
		dataInbox:      make(chan types.Message, 5000),
		network:        network,
		ctx:            ctx,
		networkLatency: latency,
		packetLossRate: network.config.Network.PacketLossRate, // 设置丢包率
	}
}

func (t *SimulatedTransport) Send(to types.NodeID, msg types.Message) error {
	// 模拟网络丢包
	if rand.Float64() < t.packetLossRate {
		// 丢包了，直接返回，不发送消息
		// 可以选择记录日志以便调试
		// Logf("[Network] Packet dropped: %s -> %s, MsgType=%v\n", t.nodeID, to, msg.Type)
		return nil // 返回nil表示"发送成功"（发送方不知道丢包）
	}
	go func() {
		// 快照传输延迟更长
		delay := t.networkLatency
		if msg.Type == types.MsgSnapshotResponse && msg.Snapshot != nil {
			delay = delay * 3 // 快照数据更大，传输时间更长
		}
		delay += time.Duration(rand.Intn(int(delay / 2)))
		time.Sleep(delay)

		receiver, ok := t.network.GetTransport(to).(*SimulatedTransport)
		if !ok || receiver == nil {
			return
		}

		targetInbox := receiver.ctrlInbox
		if msg.Type == types.MsgGet || msg.Type == types.MsgPut {
			targetInbox = receiver.dataInbox
		}

		select {
		case targetInbox <- msg:
		case <-time.After(100 * time.Millisecond):
		case <-t.ctx.Done():
		}
	}()
	return nil
}

func (t *SimulatedTransport) Receive() <-chan types.Message {
	return t.ctrlInbox
}

func (t *SimulatedTransport) ReceiveData() <-chan types.Message {
	return t.dataInbox
}

func (t *SimulatedTransport) Broadcast(msg types.Message, peers []types.NodeID) {
	for _, peer := range peers {
		t.Send(peer, msg)
	}
}

func (t *SimulatedTransport) SamplePeers(exclude types.NodeID, count int) []types.NodeID {
	return t.network.SamplePeers(exclude, count)
}
