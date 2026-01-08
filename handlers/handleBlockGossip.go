package handlers

import (
	"dex/consensus"
	"dex/types"
	"encoding/json"
	"io"
	"net/http"
)

// 发送端 gossip 发的是 JSON 的 types.GossipPayload（里头嵌 db.Block）
// 这里只解析这一种；不做其他格式兜底。
func (hm *HandlerManager) HandleBlockGossip(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleBlockGossip0")

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var payload types.GossipPayload
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		http.Error(w, "Failed to parse JSON GossipPayload", http.StatusBadRequest)
		return
	}
	if payload.Block == nil {
		http.Error(w, "GossipPayload missing block", http.StatusBadRequest)
		return
	}

	// 转为共识层 block
	if hm.adapter == nil {
		http.Error(w, "adapter unavailable", http.StatusServiceUnavailable)
		return
	}
	consBlock, err := hm.adapter.DBBlockToConsensus(payload.Block)
	if err != nil {
		http.Error(w, "Failed to convert block", http.StatusBadRequest)
		return
	}

	// 已见过的区块直接返回 OK（可选，按你现有字段）
	if hm.seenBlocksCache != nil && consBlock.ID != "" && hm.seenBlocksCache.Contains(consBlock.ID) {
		hm.Logger.Debug("[HandleBlockGossip] Block %s already seen, OK", consBlock.ID)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
		return
	}
	if hm.seenBlocksCache != nil && consBlock.ID != "" {
		hm.seenBlocksCache.Add(consBlock.ID, true)
	}

	// 统计点位
	hm.Stats.RecordAPICall("HandleBlockGossip1")

	// 交给共识/网络层
	msg := types.Message{
		RequestID: payload.RequestID,
		Type:      types.MsgGossip,
		From:      types.NodeID(consBlock.Proposer),
		Block:     consBlock,
		BlockID:   consBlock.ID,
		Height:    consBlock.Height,
	}

	// 将 dbBlock 加到共识存储（直接用 payload 里的 db.Block 即可）
	if hm.consensusManager != nil {
		if err := hm.consensusManager.AddBlock(payload.Block); err != nil {
			hm.Logger.Debug("[HandleBlockGossip] AddBlock warn: %v", err) // 已存在等非致命错误
		}
		// 如果是RealTransport，使用队列方式处理
		if rt, ok := hm.consensusManager.Transport.(*consensus.RealTransport); ok {
			if err := rt.EnqueueReceivedMessage(msg); err != nil {
				hm.Logger.Warn("[Handler] Failed to enqueue chits message: %v", err)
				// 控制面消息入队失败，返回 503
				http.Error(w, "Service temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
		}
	}

	// 将交易放入交易池
	if len(payload.Block.Body) > 0 {
		for _, tx := range payload.Block.Body {
			if tx != nil {
				if err := hm.txPool.StoreAnyTx(tx); err != nil {
					hm.Logger.Debug("[HandleBlockGossip] Failed to store tx %s: %v", tx.GetTxId(), err)
				}
			}
		}
	}

	hm.Logger.Debug("[HandleBlockGossip] Block %s at height %d proposer=%s",
		consBlock.ID, consBlock.Height, consBlock.Proposer)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}
