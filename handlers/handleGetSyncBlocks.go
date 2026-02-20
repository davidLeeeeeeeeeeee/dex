package handlers

import (
	"dex/consensus"
	"dex/types"
	"encoding/json"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// HandleGetSyncBlocks handles range sync requests and returns finalized blocks
// together with their VRF signature set proofs.
func (hm *HandlerManager) HandleGetSyncBlocks(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleGetSyncBlocks")

	var req types.SyncBlocksRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeSyncBlocksError(w, http.StatusBadRequest, "invalid json request")
		return
	}
	if req.ToHeight < req.FromHeight {
		writeSyncBlocksError(w, http.StatusBadRequest, "invalid height range")
		return
	}
	if hm.consensusManager == nil {
		writeSyncBlocksError(w, http.StatusServiceUnavailable, "consensus manager unavailable")
		return
	}

	store := hm.consensusManager.GetBlockStore()
	realStore, ok := store.(*consensus.RealBlockStore)
	if !ok || realStore == nil {
		writeSyncBlocksError(w, http.StatusServiceUnavailable, "signature set store unavailable")
		return
	}

	resp := types.SyncBlocksResponse{
		Blocks: make([]types.SyncBlockBundle, 0),
	}

	for h := req.FromHeight; h <= req.ToHeight; h++ {
		blockID, ok := hm.consensusManager.GetFinalizedBlockID(h)
		if !ok || blockID == "" {
			hm.Logger.Warn("[HandleGetSyncBlocks] No finalized block at height %d", h)
			if h == req.ToHeight {
				break
			}
			continue
		}

		dbBlock, err := hm.dbManager.GetBlockByID(blockID)
		if err != nil || dbBlock == nil {
			hm.Logger.Warn("[HandleGetSyncBlocks] Finalized block %s at height %d not found in DB: %v", blockID, h, err)
			if h == req.ToHeight {
				break
			}
			continue
		}

		sigSet, exists := realStore.GetSignatureSet(h)
		if !exists || sigSet == nil {
			hm.Logger.Warn("[HandleGetSyncBlocks] Missing signature set at height %d (block=%s)", h, blockID)
			if h == req.ToHeight {
				break
			}
			continue
		}

		blockBytes, err := proto.Marshal(dbBlock)
		if err != nil {
			hm.Logger.Warn("[HandleGetSyncBlocks] Failed to marshal block %s at height %d: %v", blockID, h, err)
			if h == req.ToHeight {
				break
			}
			continue
		}
		sigSetBytes, err := proto.Marshal(sigSet)
		if err != nil {
			hm.Logger.Warn("[HandleGetSyncBlocks] Failed to marshal signature set at height %d: %v", h, err)
			if h == req.ToHeight {
				break
			}
			continue
		}

		resp.Blocks = append(resp.Blocks, types.SyncBlockBundle{
			Height:            h,
			BlockID:           blockID,
			BlockBytes:        blockBytes,
			SignatureSetBytes: sigSetBytes,
		})

		if h == req.ToHeight {
			break
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func writeSyncBlocksError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(types.SyncBlocksResponse{Error: msg})
}
