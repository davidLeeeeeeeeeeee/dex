package consensus

import "sort"

// ============================================
// 消息处理器
// ============================================

type MessageHandler struct {
	nodeID          NodeID
	node            *Node
	isByzantine     bool
	transport       Transport
	store           BlockStore
	engine          ConsensusEngine
	queryManager    *QueryManager
	gossipManager   *GossipManager
	syncManager     *SyncManager
	snapshotManager *SnapshotManager // 新增
	events          EventBus
	config          *ConsensusConfig
}

func NewMessageHandler(nodeID NodeID, isByzantine bool, transport Transport, store BlockStore, engine ConsensusEngine, events EventBus, config *ConsensusConfig) *MessageHandler {
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

func (h *MessageHandler) Handle(msg Message) {
	if h.isByzantine && (msg.Type == MsgPullQuery || msg.Type == MsgPushQuery) {
		if h.node != nil {
			h.node.stats.mu.Lock()
			h.node.stats.queriesReceived++
			h.node.stats.mu.Unlock()
		}
		return
	}

	switch msg.Type {
	case MsgPullQuery:
		h.handlePullQuery(msg)
	case MsgPushQuery:
		h.handlePushQuery(msg)
	case MsgChits:
		h.queryManager.HandleChit(msg)
	case MsgGet:
		h.handleGet(msg)
	case MsgPut:
		h.handlePut(msg)
	case MsgGossip:
		h.gossipManager.HandleGossip(msg)
	case MsgSyncRequest:
		h.syncManager.HandleSyncRequest(msg)
	case MsgSyncResponse:
		h.syncManager.HandleSyncResponse(msg)
	case MsgHeightQuery:
		h.syncManager.HandleHeightQuery(msg)
	case MsgHeightResponse:
		h.syncManager.HandleHeightResponse(msg)
	case MsgSnapshotRequest: // 新增
		h.syncManager.HandleSnapshotRequest(msg)
	case MsgSnapshotResponse: // 新增
		h.syncManager.HandleSnapshotResponse(msg)
	}
}

func (h *MessageHandler) handlePullQuery(msg Message) {
	if h.node != nil {
		h.node.stats.mu.Lock()
		h.node.stats.queriesReceived++
		h.node.stats.mu.Unlock()
	}

	block, exists := h.store.Get(msg.BlockID)
	if !exists {
		h.transport.Send(NodeID(msg.From), Message{
			Type:      MsgGet,
			From:      h.nodeID,
			RequestID: msg.RequestID,
			BlockID:   msg.BlockID,
		})
		return
	}

	h.sendChits(NodeID(msg.From), msg.RequestID, block.Height)
}

func (h *MessageHandler) handlePushQuery(msg Message) {
	if h.node != nil {
		h.node.stats.mu.Lock()
		h.node.stats.queriesReceived++
		h.node.stats.mu.Unlock()
	}

	if msg.Block != nil {
		isNew, err := h.store.Add(msg.Block)
		if err != nil {
			return
		}

		if isNew {
			Logf("[Node %d] Received new block %s via PushQuery\n", h.nodeID, msg.Block.ID)
			h.events.Publish(BaseEvent{
				eventType: EventNewBlock,
				data:      msg.Block,
			})
		}

		h.sendChits(NodeID(msg.From), msg.RequestID, msg.Block.Height)
	}
}

func (h *MessageHandler) sendChits(to NodeID, requestID uint32, queryHeight uint64) {
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
		h.node.stats.chitsResponded++
		h.node.stats.mu.Unlock()
	}

	accepted, acceptedHeight := h.store.GetLastAccepted()
	h.transport.Send(to, Message{
		Type: MsgChits, From: h.nodeID, RequestID: requestID,
		PreferredID: preferred, PreferredIDHeight: queryHeight,
		AcceptedID: accepted, AcceptedHeight: acceptedHeight,
	})
}

func (h *MessageHandler) handleGet(msg Message) {
	if block, exists := h.store.Get(msg.BlockID); exists {
		h.transport.Send(NodeID(msg.From), Message{
			Type:      MsgPut,
			From:      h.nodeID,
			RequestID: msg.RequestID,
			Block:     block,
			Height:    block.Height,
		})
	}
}

func (h *MessageHandler) handlePut(msg Message) {
	if msg.Block != nil {
		isNew, err := h.store.Add(msg.Block)
		if err != nil {
			return
		}

		if isNew {
			Logf("[Node %d] Received new block %s via Put from Node %d, gossiping it\n",
				h.nodeID, msg.Block.ID, msg.From)
			h.events.Publish(BaseEvent{
				eventType: EventBlockReceived,
				data:      msg.Block,
			})
		}
	}
}
