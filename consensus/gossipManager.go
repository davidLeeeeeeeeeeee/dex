package consensus

import (
	"context"
	"dex/interfaces"
	"dex/logs"
	"dex/types"
	"math/rand"
	"sync"
	"time"
)

// ============================================
// Gossip管理器
// ============================================

type GossipManager struct {
	nodeID     types.NodeID
	node       *Node
	transport  interfaces.Transport
	store      interfaces.BlockStore
	config     *GossipConfig
	events     interfaces.EventBus
	seenBlocks map[string]bool
	mu         sync.RWMutex
}

func NewGossipManager(nodeID types.NodeID, transport interfaces.Transport, store interfaces.BlockStore, config *GossipConfig, events interfaces.EventBus) *GossipManager {
	gm := &GossipManager{
		nodeID:     nodeID,
		transport:  transport,
		store:      store,
		config:     config,
		events:     events,
		seenBlocks: make(map[string]bool),
	}

	events.Subscribe(types.EventNewBlock, func(e interfaces.Event) {
		if block, ok := e.Data().(*types.Block); ok {
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
	curMax := gm.store.GetCurrentHeight()
	// 如果链上还没到 next/next+1，就别去 GetByHeight，避免触发 DB 回退
	if curMax <= currentHeight {
		return
	}

	blocks := make([]*types.Block, 0, 4)
	blocks = append(blocks, gm.store.GetByHeight(currentHeight+1)...)
	if curMax >= currentHeight+2 {
		blocks = append(blocks, gm.store.GetByHeight(currentHeight+2)...)
	}

	for _, block := range blocks {
		if block.ID == "genesis" {
			continue
		}
		gm.mu.RLock()
		seen := gm.seenBlocks[block.ID]
		gm.mu.RUnlock()
		if !seen {
			gm.gossipBlock(block)
		}
	}
}

func (gm *GossipManager) gossipBlock(block *types.Block) {
	gm.mu.Lock()
	if gm.seenBlocks[block.ID] { // 已转发过？那就不再发
		gm.mu.Unlock()
		return
	}
	gm.seenBlocks[block.ID] = true
	gm.mu.Unlock()

	peers := gm.transport.SamplePeers(gm.nodeID, gm.config.Fanout)
	msg := types.Message{
		Type:    types.MsgGossip,
		From:    gm.nodeID,
		Block:   block,
		BlockID: block.ID,
		Height:  block.Height,
	}

	gm.transport.Broadcast(msg, peers)
}

func (gm *GossipManager) HandleGossip(msg types.Message) {
	if msg.Block == nil {
		return
	}

	if gm.node != nil {
		gm.node.stats.Mu.Lock()
		gm.node.stats.GossipsReceived++
		gm.node.stats.Mu.Unlock()
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
		logs.Debug("[Node %d] Received new block %s via gossip from Node %d\n",
			gm.nodeID, msg.Block.ID, msg.From)

		gm.events.Publish(types.BaseEvent{
			EventType: types.EventBlockReceived,
			EventData: msg.Block,
		})

		go func() {
			time.Sleep(time.Duration(50+rand.Intn(100)) * time.Millisecond)
			gm.gossipBlock(msg.Block)
		}()
	}
}
