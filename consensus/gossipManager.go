package consensus

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// ============================================
// Gossip管理器
// ============================================

type GossipManager struct {
	nodeID     NodeID
	node       *Node
	transport  Transport
	store      BlockStore
	config     *GossipConfig
	events     EventBus
	seenBlocks map[string]bool
	mu         sync.RWMutex
}

func NewGossipManager(nodeID NodeID, transport Transport, store BlockStore, config *GossipConfig, events EventBus) *GossipManager {
	gm := &GossipManager{
		nodeID:     nodeID,
		transport:  transport,
		store:      store,
		config:     config,
		events:     events,
		seenBlocks: make(map[string]bool),
	}

	events.Subscribe(EventNewBlock, func(e Event) {
		if block, ok := e.Data().(*Block); ok {
			gm.gossipBlock(block)
		}
	})

	return gm
}

func (gm *GossipManager) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(gm.config.Interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				gm.gossipNewBlocks()
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (gm *GossipManager) gossipNewBlocks() {
	_, currentHeight := gm.store.GetLastAccepted()

	blocks := make([]*Block, 0)
	blocks = append(blocks, gm.store.GetByHeight(currentHeight+1)...)
	blocks = append(blocks, gm.store.GetByHeight(currentHeight+2)...)

	for _, block := range blocks {
		if block.ID == "genesis" {
			continue
		}

		gm.mu.RLock()
		alreadyGossiped := gm.seenBlocks[block.ID]
		gm.mu.RUnlock()

		if !alreadyGossiped {
			gm.gossipBlock(block)
		}
	}
}

func (gm *GossipManager) gossipBlock(block *Block) {
	gm.mu.Lock()
	gm.seenBlocks[block.ID] = true
	gm.mu.Unlock()

	peers := gm.transport.SamplePeers(gm.nodeID, gm.config.Fanout)
	msg := Message{
		Type:    MsgGossip,
		From:    gm.nodeID,
		Block:   block,
		BlockID: block.ID,
		Height:  block.Height,
	}

	gm.transport.Broadcast(msg, peers)
}

func (gm *GossipManager) HandleGossip(msg Message) {
	if msg.Block == nil {
		return
	}

	if gm.node != nil {
		gm.node.stats.mu.Lock()
		gm.node.stats.gossipsReceived++
		gm.node.stats.mu.Unlock()
	}

	gm.mu.RLock()
	alreadySeen := gm.seenBlocks[msg.Block.ID]
	gm.mu.RUnlock()

	if alreadySeen {
		return
	}

	gm.mu.Lock()
	gm.seenBlocks[msg.Block.ID] = true
	gm.mu.Unlock()

	isNew, err := gm.store.Add(msg.Block)
	if err != nil {
		return
	}

	if isNew {
		Logf("[Node %d] Received new block %s via gossip from Node %d\n",
			gm.nodeID, msg.Block.ID, msg.From)

		gm.events.Publish(BaseEvent{
			eventType: EventBlockReceived,
			data:      msg.Block,
		})

		go func() {
			time.Sleep(time.Duration(50+rand.Intn(100)) * time.Millisecond)
			gm.gossipBlock(msg.Block)
		}()
	}
}
