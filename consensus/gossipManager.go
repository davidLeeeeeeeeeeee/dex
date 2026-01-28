package consensus

import (
	"context"
	"dex/interfaces"
	"dex/logs"
	"dex/types"
	"math/rand"
	"strings"
	"sync"
	"time"
)

// ============================================
// Gossip管理器
// ============================================

type GossipManager struct {
	nodeID       types.NodeID
	node         *Node
	transport    interfaces.Transport
	store        interfaces.BlockStore
	config       *GossipConfig
	events       interfaces.EventBus
	Logger       logs.Logger
	queryManager *QueryManager
	seenBlocks   map[string]bool
	mu           sync.RWMutex
}

func NewGossipManager(id types.NodeID, transport interfaces.Transport, store interfaces.BlockStore, config *GossipConfig, events interfaces.EventBus, logger logs.Logger) *GossipManager {
	gm := &GossipManager{
		nodeID:     id,
		transport:  transport,
		store:      store,
		config:     config,
		events:     events,
		Logger:     logger,
		seenBlocks: make(map[string]bool),
	}

	events.Subscribe(types.EventNewBlock, func(e interfaces.Event) {
		if block, ok := e.Data().(*types.Block); ok {
			gm.gossipBlock(block)
		}
	})

	return gm
}

func (gm *GossipManager) SetQueryManager(qm *QueryManager) {
	gm.queryManager = qm
}

func (gm *GossipManager) Start(ctx context.Context) {
	go func() {
		logs.SetThreadNodeContext(string(gm.nodeID))
		ticker := time.NewTicker(gm.config.Interval)
		cleanupTicker := time.NewTicker(60 * time.Second) // 每分钟清理一次旧的 seenBlocks
		defer ticker.Stop()
		defer cleanupTicker.Stop()

		for {
			select {
			case <-ticker.C:
				gm.gossipNewBlocks()
			case <-cleanupTicker.C:
				gm.cleanupSeenBlocks()
			case <-ctx.Done():
				return
			}
		}
	}()
}

// cleanupSeenBlocks 清理旧的 seenBlocks，避免内存泄漏
func (gm *GossipManager) cleanupSeenBlocks() {
	_, currentHeight := gm.store.GetLastAccepted()
	if currentHeight < 100 {
		return
	}

	gm.mu.Lock()
	defer gm.mu.Unlock()

	// 如果 seenBlocks 太大，清理掉所有的（因为我们没有存储高度信息）
	// 简单策略：如果超过 1000 个条目，全部清空
	if len(gm.seenBlocks) > 1000 {
		gm.seenBlocks = make(map[string]bool)
		logs.Debug("[GossipManager] Cleaned up seenBlocks cache (was > 1000 entries)")
	}
}

func (gm *GossipManager) gossipNewBlocks() {
	_, currentHeight := gm.store.GetLastAccepted()
	curMax := gm.store.GetCurrentHeight()
	// 如果链上还没到 next/next+1，就别去 GetByHeight，避免触发 DB 回退
	if curMax <= currentHeight {
		return
	}

	blocks := make([]*types.Block, 0, 4) //初始0，最多容纳4个元素
	blocks = append(blocks, gm.store.GetByHeight(currentHeight+1)...)
	if curMax >= currentHeight+2 { //控制广播的范围，
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
		gm.node.Stats.Mu.Lock()
		gm.node.Stats.GossipsReceived++
		gm.node.Stats.Mu.Unlock()
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
		// 如果是因为缺父块导致的拒绝，自动请求父块
		if strings.Contains(err.Error(), "parent block") && strings.Contains(err.Error(), "not found") {
			if gm.queryManager != nil {
				gm.queryManager.RequestBlock(msg.Block.ParentID, types.NodeID(msg.From))
			}
		}
		return
	}

	if isNew {
		logs.Debug("[Node %s] Received new block %s via gossip from Node %s",
			gm.nodeID, msg.Block.ID, msg.From)

		gm.events.PublishAsync(types.BaseEvent{
			EventType: types.EventBlockReceived,
			EventData: msg.Block,
		})

		go func() {
			logs.SetThreadNodeContext(string(gm.nodeID))
			time.Sleep(time.Duration(50+rand.Intn(100)) * time.Millisecond)
			gm.gossipBlock(msg.Block)
		}()
	}
}
