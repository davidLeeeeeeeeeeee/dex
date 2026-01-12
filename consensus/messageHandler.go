package consensus

import (
	"dex/interfaces"
	"dex/logs"
	"dex/stats"
	"dex/types"
	"sync"
)

// ============================================
// 消息处理器
// ============================================

type MessageHandler struct {
	nodeID          types.NodeID
	node            *Node
	isByzantine     bool
	transport       interfaces.Transport
	store           interfaces.BlockStore
	engine          interfaces.ConsensusEngine
	gossipManager   *GossipManager
	syncManager     *SyncManager
	snapshotManager *SnapshotManager
	events          interfaces.EventBus
	config          *ConsensusConfig
	Logger          logs.Logger
	queryManager    *QueryManager
	proposalManager *ProposalManager // 用于访问window计算和缓存
	// 存储待回复的PullQuery
	pendingQueries   map[uint32]types.Message
	pendingQueriesMu sync.RWMutex
	stats            *stats.Stats
}

func NewMessageHandler(id types.NodeID, byzantine bool, transport interfaces.Transport, store interfaces.BlockStore, engine interfaces.ConsensusEngine, events interfaces.EventBus, config *ConsensusConfig, logger logs.Logger) *MessageHandler {
	return &MessageHandler{
		nodeID:         id,
		isByzantine:    byzantine,
		transport:      transport,
		store:          store,
		engine:         engine,
		events:         events,
		config:         config,
		Logger:         logger,
		pendingQueries: make(map[uint32]types.Message),
		stats:          stats.NewStats(),
	}
}

func (h *MessageHandler) SetManagers(qm *QueryManager, gm *GossipManager, sm *SyncManager, snapMgr *SnapshotManager) {
	h.queryManager = qm
	h.gossipManager = gm
	h.syncManager = sm
	h.snapshotManager = snapMgr
}

func (h *MessageHandler) SetProposalManager(pm *ProposalManager) {
	h.proposalManager = pm
}

func (h *MessageHandler) HandleMsg(msg types.Message) {
	h.stats.RecordAPICall(string(msg.Type))
	if h.isByzantine && (msg.Type == types.MsgPullQuery || msg.Type == types.MsgPushQuery) {
		if h.node != nil {
			h.node.Stats.Mu.Lock()
			h.node.Stats.QueriesReceived++
			h.node.Stats.Mu.Unlock()
		}
		return
	}

	switch msg.Type {
	case types.MsgPullQuery:
		h.handlePullQuery(msg)
	case types.MsgPushQuery:
		h.handlePushQuery(msg)
	case types.MsgChits:
		h.queryManager.HandleChit(msg)
	case types.MsgGet:
		h.handleGet(msg)
	case types.MsgPut:
		h.handlePut(msg)
	case types.MsgGossip:
		h.gossipManager.HandleGossip(msg)
	case types.MsgSyncRequest:
		h.syncManager.HandleSyncRequest(msg)
	case types.MsgSyncResponse:
		h.syncManager.HandleSyncResponse(msg)
	case types.MsgHeightQuery:
		h.syncManager.HandleHeightQuery(msg)
	case types.MsgHeightResponse:
		h.syncManager.HandleHeightResponse(msg)
	case types.MsgSnapshotRequest: // 新增
		h.syncManager.HandleSnapshotRequest(msg)
	case types.MsgSnapshotResponse: // 新增
		h.syncManager.HandleSnapshotResponse(msg)
	}
}

func (h *MessageHandler) handlePullQuery(msg types.Message) {
	if h.node != nil {
		h.node.Stats.Mu.Lock()
		h.node.Stats.QueriesReceived++
		h.node.Stats.Mu.Unlock()
	}

	// 检查本地是否有该区块
	block, exists := h.store.Get(msg.BlockID)
	if !exists {
		// 本地没有区块，向发送者请求
		logs.Debug("[Node %s] Don't have block %s, requesting from %s",
			h.nodeID, msg.BlockID, msg.From)

		// 发送Get消息请求区块数据
		h.transport.Send(types.NodeID(msg.From), types.Message{
			Type:      types.MsgGet,
			From:      h.nodeID,
			RequestID: msg.RequestID,
			BlockID:   msg.BlockID,
			Height:    msg.Height,
		})

		// 可以选择存储待回复的查询，等收到区块后再回复chits
		h.storePendingQuery(msg)
		return
	}

	// 有区块，直接发送chits投票
	h.sendChits(types.NodeID(msg.From), msg.RequestID, block.Height)
}

type PendingQueryKey struct {
	BlockID   string `json:"block_id"`
	RequestID uint32 `json:"request_id"`
}

// 添加存储待回复查询的方法
func (h *MessageHandler) storePendingQuery(msg types.Message) {
	// 可以在MessageHandler中添加一个pendingQueries map
	// 当收到Put消息后，检查是否有待回复的查询
	if h.pendingQueries == nil {
		h.pendingQueries = make(map[uint32]types.Message)
	}
	h.pendingQueriesMu.Lock()
	h.pendingQueries[msg.RequestID] = msg
	h.pendingQueriesMu.Unlock()
}

func (h *MessageHandler) handlePushQuery(msg types.Message) {
	if h.node != nil {
		h.node.Stats.Mu.Lock()
		h.node.Stats.QueriesReceived++
		h.node.Stats.Mu.Unlock()
	}

	if msg.Block == nil {
		return
	}

	// 如果该高度本地已经最终化/接受过，直接回复偏好即可（避免把旧高度的候选块重新塞回 store）
	_, acceptedHeight := h.store.GetLastAccepted()
	if msg.Block.Height <= acceptedHeight {
		h.sendChits(types.NodeID(msg.From), msg.RequestID, msg.Block.Height)
		return
	}

	// 仅对“当前要决策的下一高度”做 window 约束；否则会导致落后节点的 PushQuery 长期收不到回应而卡住
	if h.proposalManager != nil && msg.Block.Height == acceptedHeight+1 {
		h.proposalManager.mu.Lock()
		currentWindow := h.proposalManager.calculateCurrentWindow()
		h.proposalManager.mu.Unlock()

		if msg.Block.Window > currentWindow {
			logs.Debug("[Node %s] Received block %s for future window %d (current: %d), caching",
				h.nodeID, msg.Block.ID, msg.Block.Window, currentWindow)
			h.proposalManager.CacheProposal(msg.Block)
			// 关键：即使缓存，也要回复 chits，避免对方 query 超时导致共识停滞
			h.sendChits(types.NodeID(msg.From), msg.RequestID, msg.Block.Height)
			return
		}
	}

	isNew, err := h.store.Add(msg.Block)
	if err != nil {
		// 区块被拒绝也应回复，避免对方长期等待
		h.sendChits(types.NodeID(msg.From), msg.RequestID, msg.Block.Height)
		return
	}

	if isNew {
		logs.Debug("[Node %s] Received new block %s via PushQuery (window %d)",
			h.nodeID, msg.Block.ID, msg.Block.Window)
		h.events.PublishAsync(types.BaseEvent{
			EventType: types.EventNewBlock,
			EventData: msg.Block,
		})
	}

	h.sendChits(types.NodeID(msg.From), msg.RequestID, msg.Block.Height)
}

func (h *MessageHandler) sendChits(to types.NodeID, requestID uint32, queryHeight uint64) {
	var preferred string

	// 对于 height=0（创世区块），直接返回 genesis
	if queryHeight == 0 {
		preferred = "genesis"
	} else {
		// 关键：必须验证父区块已最终化，才能对当前高度投票
		parent, ok := h.store.GetFinalizedAtHeight(queryHeight - 1)
		if !ok {
			// 父区块尚未最终化，弃权（不投票）
			logs.Debug("[sendChits] Parent at height %d not finalized, abstaining for height %d",
				queryHeight-1, queryHeight)
			preferred = "" // 弃权
		} else {
			// 获取引擎的当前偏好
			preferred = h.engine.GetPreference(queryHeight)

			// 验证偏好的区块是否链接到已最终化的父区块
			if preferred != "" {
				block, exists := h.store.Get(preferred)
				if !exists || block.ParentID != parent.ID {
					// 偏好的区块父链接不正确，重新选择
					logs.Debug("[sendChits] Preferred block %s has wrong parent, reselecting", preferred)
					preferred = ""
				}
			}

			// 如果没有有效偏好，从符合条件的候选中选择
			if preferred == "" {
				blocks := h.store.GetByHeight(queryHeight)
				cand := make([]string, 0, len(blocks))
				for _, b := range blocks {
					if b.ParentID == parent.ID {
						cand = append(cand, b.ID)
					}
				}
				if len(cand) > 0 {
					// 使用 selectByMinHash 与 Snowball 保持一致的确定性选择规则
					preferred = selectByMinHash(cand)
				}
			}
		}
	}

	accepted, acceptedHeight := h.store.GetLastAccepted()
	logs.Debug("[sendChits] to=%s req=%d h=%d preferred=%v accepted=%v",
		to, requestID, queryHeight, preferred, accepted)

	h.transport.Send(to, types.Message{
		Type: types.MsgChits, From: h.nodeID, RequestID: requestID,
		PreferredID:       preferred,
		PreferredIDHeight: queryHeight,
		AcceptedID:        accepted, AcceptedHeight: acceptedHeight,
	})

	// 增加 ChitsResponded 计数器
	if h.node != nil {
		h.node.Stats.Mu.Lock()
		h.node.Stats.ChitsResponded++
		h.node.Stats.Mu.Unlock()
	}
}

func (h *MessageHandler) handleGet(msg types.Message) {
	// 请求方需要区块数据，就查本地，发给他
	if block, exists := h.store.Get(msg.BlockID); exists {
		err := h.transport.Send(types.NodeID(msg.From), types.Message{
			Type:      types.MsgPut,
			From:      h.nodeID,
			RequestID: msg.RequestID,
			Block:     block,
			Height:    block.Height,
		})
		if err != nil {
			return
		}
	}
}

func (h *MessageHandler) handlePut(msg types.Message) {
	if msg.Block != nil {
		isNew, err := h.store.Add(msg.Block)
		if err != nil {
			// 仍尝试清理 pendingQueries，避免泄漏
			h.checkPendingQueries(msg.RequestID)
			return
		}

		if isNew {
			logs.Debug("[Node %s] Received new block %s via Put from Node %s",
				h.nodeID, msg.Block.ID, msg.From)
			h.events.PublishAsync(types.BaseEvent{
				EventType: types.EventBlockReceived,
				EventData: msg.Block,
			})
		}

		// 无论是否 isNew，都要检查 pendingQueries（可能已通过 gossip 等路径先收到该块）
		h.checkPendingQueries(msg.RequestID)
	}
}

// 添加检查待回复查询的方法
func (h *MessageHandler) checkPendingQueries(requestId uint32) {
	h.pendingQueriesMu.Lock()
	if pendingMsg, exists := h.pendingQueries[requestId]; exists {
		blockID := pendingMsg.BlockID
		delete(h.pendingQueries, requestId)
		h.pendingQueriesMu.Unlock()

		// 现在有了区块，可以回复chits
		block, found := h.store.Get(blockID)
		if !found || block == nil {
			// 区块可能已被清理或从未存储（例如：收到 PushQuery 后区块被拒绝）
			// 这种情况下跳过回复，让请求方超时重试
			logs.Debug("[checkPendingQueries] block %s not found for pending query %d",
				blockID, requestId)
			return
		}
		h.sendChits(types.NodeID(pendingMsg.From), pendingMsg.RequestID, block.Height)
	} else {
		h.pendingQueriesMu.Unlock()
	}
}
