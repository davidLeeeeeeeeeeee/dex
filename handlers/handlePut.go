package handlers

import (
	"dex/consensus"
	"dex/pb"
	"dex/types"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// HandlePut handles /put where sender posts a protobuf db.Block.
func (hm *HandlerManager) HandlePut(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandlePut")

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var block pb.Block
	if err := proto.Unmarshal(bodyBytes, &block); err != nil {
		http.Error(w, "Failed to parse protobuf db.Block", http.StatusBadRequest)
		return
	}

	if block.BlockHash == "" || block.BlockHash == "genesis" {
		http.Error(w, "Invalid block hash", http.StatusBadRequest)
		return
	}

	if err := hm.dbManager.SaveBlock(&block); err != nil {
		hm.Logger.Error("[HandlePut] Failed to save block %s: %v", block.BlockHash, err)
		http.Error(w, "Failed to save block", http.StatusInternalServerError)
		return
	}

	// Keep block payload available for consensus Add()/finalize path.
	consensus.CacheBlock(&block)

	if len(block.Body) > 0 {
		for _, tx := range block.Body {
			if tx != nil {
				if err := hm.txPool.StoreAnyTx(tx); err != nil {
					hm.Logger.Debug("[HandlePut] Failed to store tx %s: %v", tx.GetTxId(), err)
				}
			}
		}
		hm.Logger.Debug("[HandlePut] Added %d transactions from block %s to pool", len(block.Body), block.BlockHash)
	}

	if hm.consensusManager != nil && hm.adapter != nil {
		if consensusBlock, err := hm.adapter.DBBlockToConsensus(&block); err == nil {
			from := types.NodeID(consensusBlock.Header.Proposer)
			if from == "" && block.Header != nil {
				from = types.NodeID(block.Header.Miner)
			}

			msg := types.Message{
				RequestID: 0,
				Type:      types.MsgPut,
				From:      from,
				Block:     consensusBlock,
				BlockID:   block.BlockHash,
				Height:    block.Header.Height,
				ShortTxs:  block.ShortTxs,
			}
			if rt, ok := hm.consensusManager.Transport.(*consensus.RealTransport); ok {
				if err := rt.EnqueueReceivedMessage(msg); err != nil {
					hm.Logger.Warn("[HandlePut] Failed to enqueue block message: %v", err)
				}
			}
		}
	}

	hm.Logger.Info("[HandlePut] Stored block %s at height %d (txs=%d)", block.BlockHash, block.Header.Height, len(block.Body))
	w.WriteHeader(http.StatusOK)
}
