package consensus

import (
	"context"
	"math/rand"
	"time"
)

type SimulatedTransport struct {
	nodeID         NodeID
	inbox          chan Message
	network        *NetworkManager
	ctx            context.Context
	networkLatency time.Duration
}

func NewSimulatedTransport(nodeID NodeID, network *NetworkManager, ctx context.Context, latency time.Duration) Transport {
	return &SimulatedTransport{
		nodeID:         nodeID,
		inbox:          make(chan Message, 1000000),
		network:        network,
		ctx:            ctx,
		networkLatency: latency,
	}
}

func (t *SimulatedTransport) Send(to NodeID, msg Message) error {
	go func() {
		// 快照传输延迟更长
		delay := t.networkLatency
		if msg.Type == MsgSnapshotResponse && msg.Snapshot != nil {
			delay = delay * 3 // 快照数据更大，传输时间更长
		}
		delay += time.Duration(rand.Intn(int(delay / 2)))
		time.Sleep(delay)

		select {
		case t.network.GetTransport(to).(*SimulatedTransport).inbox <- msg:
		case <-time.After(100 * time.Millisecond):
		case <-t.ctx.Done():
		}
	}()
	return nil
}

func (t *SimulatedTransport) Receive() <-chan Message {
	return t.inbox
}

func (t *SimulatedTransport) Broadcast(msg Message, peers []NodeID) {
	for _, peer := range peers {
		t.Send(peer, msg)
	}
}

func (t *SimulatedTransport) SamplePeers(exclude NodeID, count int) []NodeID {
	return t.network.SamplePeers(exclude, count)
}
