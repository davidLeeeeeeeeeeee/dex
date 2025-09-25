package consensus

import (
	"dex/interfaces"
	"dex/logs"
	"dex/types"
	"sort"
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
	snapshotManager *SnapshotManager // 新增
	events          interfaces.EventBus
	config          *ConsensusConfig
}

func NewMessageHandler(nodeID types.NodeID, isByzantine bool, transport interfaces.Transport, store interfaces.BlockStore, engine interfaces.ConsensusEngine, events interfaces.EventBus, config *ConsensusConfig) *MessageHandler {
	return &MessageHandler{
		nodeID:      nodeID,
		isByzantine: isByzantine,
		transport:   transport,
		store:       store,
		engine:      engine,
		events:      events,
		config:      config,
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

	block, exists := h.store.Get(msg.BlockID)
	if !exists {
		h.transport.Send(types.NodeID(msg.From), types.Message{
			Type:      types.MsgGet,
			From:      h.nodeID,
			RequestID: msg.RequestID,
			BlockID:   msg.BlockID,
		})
		return
	}

	h.sendChits(types.NodeID(msg.From), msg.RequestID, block.Height)
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
			Logf("[Node %d] Received new block %s via PushQuery\n", h.nodeID, msg.Block.ID)
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

	if preferred == "" {
		if block, ok := h.store.GetFinalizedAtHeight(queryHeight); ok {
			preferred = block.ID
		}
	}

	if preferred == "" {
		blocks := h.store.GetByHeight(queryHeight)
		if len(blocks) > 0 {
			ids := make([]string, 0, len(blocks))
			for _, b := range blocks {
				ids = append(ids, b.ID)
			}
			sort.Strings(ids)
			preferred = ids[len(ids)-1]
		}
	}

	if h.node != nil {
		h.node.stats.mu.Lock()
		h.node.stats.ChitsResponded++
		h.node.stats.mu.Unlock()
	}

	accepted, acceptedHeight := h.store.GetLastAccepted()
	logs.Debug("[sendChits] to=%s req=%d preferred=%v accepted=%v", to, requestID, preferred, accepted)

	h.transport.Send(to, types.Message{
		Type: types.MsgChits, From: h.nodeID, RequestID: requestID,
		PreferredID: preferred, PreferredIDHeight: queryHeight,
		AcceptedID: accepted, AcceptedHeight: acceptedHeight,
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
			Logf("[Node %d] Received new block %s via Put from Node %d, gossiping it\n",
				h.nodeID, msg.Block.ID, msg.From)
			h.events.Publish(types.BaseEvent{
				EventType: types.EventBlockReceived,
				EventData: msg.Block,
			})
		}
	}
}
