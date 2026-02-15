package handlers

import (
	"dex/consensus"
	"dex/types"
	"io"
	"net/http"
)

// HandleBlockGossip handles JSON GossipPayload posted to /gossipAnyMsg.
func (hm *HandlerManager) HandleBlockGossip(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleBlockGossip0")

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	payload, err := types.DecodeGossipPayload(bodyBytes)
	if err != nil {
		http.Error(w, "Failed to parse GossipPayload", http.StatusBadRequest)
		hm.Logger.Debug("[HandleBlockGossip] decode payload failed: %v", err)
		return
	}

	// Cache payload first so consensus Add() can use body/shortTxs immediately.
	consensus.CacheBlock(payload.Block)

	if hm.adapter == nil {
		http.Error(w, "adapter unavailable", http.StatusServiceUnavailable)
		return
	}
	consBlock, err := hm.adapter.DBBlockToConsensus(payload.Block)
	if err != nil {
		http.Error(w, "Failed to convert block", http.StatusBadRequest)
		return
	}

	if hm.seenBlocksCache != nil && consBlock.ID != "" && hm.seenBlocksCache.Contains(consBlock.ID) {
		hm.Logger.Debug("[HandleBlockGossip] Block %s already seen, OK", consBlock.ID)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
		return
	}
	if hm.seenBlocksCache != nil && consBlock.ID != "" {
		hm.seenBlocksCache.Add(consBlock.ID, true)
	}

	hm.Stats.RecordAPICall("HandleBlockGossip1")

	msg := types.Message{
		RequestID: payload.RequestID,
		Type:      types.MsgGossip,
		From:      types.NodeID(consBlock.Header.Proposer),
		Block:     consBlock,
		BlockID:   consBlock.ID,
		Height:    consBlock.Header.Height,
		ShortTxs:  payload.Block.ShortTxs,
	}

	if hm.consensusManager != nil {
		if err := hm.consensusManager.AddBlock(payload.Block); err != nil {
			hm.Logger.Debug("[HandleBlockGossip] AddBlock warn: %v", err)
		}
		if rt, ok := hm.consensusManager.Transport.(*consensus.RealTransport); ok {
			if err := rt.EnqueueReceivedMessage(msg); err != nil {
				hm.Logger.Warn("[Handler] Failed to enqueue chits message: %v", err)
				http.Error(w, "Service temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
		}
	}

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
		consBlock.ID, consBlock.Header.Height, consBlock.Header.Proposer)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}
