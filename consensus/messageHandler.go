package consensus

import (
	"dex/interfaces"
	"dex/logs"
	"dex/types"
	"sort"
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
	queryManager    *QueryManager
	gossipManager   *GossipManager
	syncManager     *SyncManager
	snapshotManager *SnapshotManager
	events          interfaces.EventBus
	config          *ConsensusConfig
	// 存储待回复的PullQuery
	pendingQueries   map[string]types.Message
	pendingQueriesMu sync.RWMutex
}

func NewMessageHandler(nodeID types.NodeID, isByzantine bool, transport interfaces.Transport, store interfaces.BlockStore, engine interfaces.ConsensusEngine, events interfaces.EventBus, config *ConsensusConfig) *MessageHandler {
	return &MessageHandler{
		nodeID:         nodeID,
		isByzantine:    isByzantine,
		transport:      transport,
		store:          store,
		engine:         engine,
		events:         events,
		config:         config,
		pendingQueries: make(map[string]types.Message),
	}
}

func (h *MessageHandler) SetManagers(qm *QueryManager, gm *GossipManager, sm *SyncManager, snapMgr *SnapshotManager) {
	h.queryManager = qm
	h.gossipManager = gm
	h.syncManager = sm
	h.snapshotManager = snapMgr
}

func (h *MessageHandler) HandleMsg(msg types.Message) {
	if h.isByzantine && (msg.Type == types.MsgPullQuery || msg.Type == types.MsgPushQuery) {
		if h.node != nil {
			h.node.stats.mu.Lock()
			h.node.stats.QueriesReceived++
			h.node.stats.mu.Unlock()
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
		h.node.stats.mu.Lock()
		h.node.stats.QueriesReceived++
		h.node.stats.mu.Unlock()
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

// 添加存储待回复查询的方法
func (h *MessageHandler) storePendingQuery(msg types.Message) {
	// 可以在MessageHandler中添加一个pendingQueries map
	// 当收到Put消息后，检查是否有待回复的查询
	if h.pendingQueries == nil {
		h.pendingQueries = make(map[string]types.Message)
	}
	h.pendingQueriesMu.Lock()
	h.pendingQueries[msg.BlockID] = msg
	h.pendingQueriesMu.Unlock()
}

func (h *MessageHandler) handlePushQuery(msg types.Message) {
	if h.node != nil {
		h.node.stats.mu.Lock()
		h.node.stats.QueriesReceived++
		h.node.stats.mu.Unlock()
	}

	if msg.Block != nil {
		isNew, err := h.store.Add(msg.Block)
		if err != nil {
			return
		}

		if isNew {
			logs.Debug("[Node %d] Received new block %s via PushQuery\n", h.nodeID, msg.Block.ID)
			h.events.Publish(types.BaseEvent{
				EventType: types.EventNewBlock,
				EventData: msg.Block,
			})
		}

		h.sendChits(types.NodeID(msg.From), msg.RequestID, msg.Block.Height)
	}
}

func (h *MessageHandler) sendChits(to types.NodeID, requestID uint32, queryHeight uint64) {
	preferred := h.engine.GetPreference(queryHeight)

	// NEW: 只有当本地 (h-1) 已最终化，才允许对 h 表态
	if preferred == "" {
		if parent, ok := h.store.GetFinalizedAtHeight(queryHeight - 1); ok {
			// 只在“父=本地最终化父”的孩子里选偏好
			blocks := h.store.GetByHeight(queryHeight)
			cand := make([]string, 0, len(blocks))
			for _, b := range blocks {
				if b.ParentID == parent.ID {
					cand = append(cand, b.ID)
				}
			}
			if len(cand) > 0 {
				sort.Strings(cand)
				preferred = cand[len(cand)-1]
			}
		}
	}

	// 不允许用“全体块里字典序最大”的兜底；父未定就弃权
	// if still "", treat as abstain

	accepted, acceptedHeight := h.store.GetLastAccepted()
	logs.Debug("[sendChits] to=%s req=%d h=%d preferred=%v accepted=%v",
		to, requestID, queryHeight, preferred, accepted)

	h.transport.Send(to, types.Message{
		Type: types.MsgChits, From: h.nodeID, RequestID: requestID,
		PreferredID:       preferred,
		PreferredIDHeight: queryHeight,
		AcceptedID:        accepted, AcceptedHeight: acceptedHeight,
	})
}

func (h *MessageHandler) handleGet(msg types.Message) {
	if block, exists := h.store.Get(msg.BlockID); exists {
		h.transport.Send(types.NodeID(msg.From), types.Message{
			Type:      types.MsgPut,
			From:      h.nodeID,
			RequestID: msg.RequestID,
			Block:     block,
			Height:    block.Height,
		})
	}
}

func (h *MessageHandler) handlePut(msg types.Message) {
	if msg.Block != nil {
		isNew, err := h.store.Add(msg.Block)
		if err != nil {
			return
		}

		if isNew {
			logs.Debug("[Node %d] Received new block %s via Put from Node %d",
				h.nodeID, msg.Block.ID, msg.From)
			h.events.Publish(types.BaseEvent{
				EventType: types.EventBlockReceived,
				EventData: msg.Block,
			})

			// 检查是否有待回复的PullQuery
			h.checkPendingQueries(msg.Block.ID)
		}
	}
}

// 添加检查待回复查询的方法
func (h *MessageHandler) checkPendingQueries(blockID string) {
	h.pendingQueriesMu.Lock()
	if pendingMsg, exists := h.pendingQueries[blockID]; exists {
		delete(h.pendingQueries, blockID)
		h.pendingQueriesMu.Unlock()

		// 现在有了区块，可以回复chits
		block, _ := h.store.Get(blockID)
		h.sendChits(types.NodeID(pendingMsg.From), pendingMsg.RequestID, block.Height)
	} else {
		h.pendingQueriesMu.Unlock()
	}
}
