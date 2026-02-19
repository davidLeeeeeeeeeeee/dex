package handlers

import (
	"dex/types"
	"encoding/json"
	"net/http"
)

func (hm *HandlerManager) HandleStateSnapshotShards(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleStateSnapshotShards")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req types.StateSnapshotShardsRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	targetHeight := req.TargetHeight
	if hm.consensusManager != nil {
		_, accepted := hm.consensusManager.GetLastAccepted()
		if targetHeight == 0 || targetHeight > accepted {
			targetHeight = accepted
		}
	}

	// Build a stable snapshot checkpoint so subsequent shard pages stay consistent.
	if targetHeight > 0 {
		if err := hm.dbManager.EnsureStateCheckpoint(targetHeight); err != nil {
			writeStateSnapshotShardsError(w, targetHeight, err)
			return
		}
	}

	shards, err := hm.dbManager.ListStateSnapshotShards(targetHeight)
	if err != nil {
		writeStateSnapshotShardsError(w, targetHeight, err)
		return
	}

	resp := types.StateSnapshotShardsResponse{
		SnapshotHeight: targetHeight,
		Shards:         make([]types.StateSnapshotShard, 0, len(shards)),
	}
	for _, sh := range shards {
		resp.Shards = append(resp.Shards, types.StateSnapshotShard{
			Shard: sh.Shard,
			Count: sh.Count,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (hm *HandlerManager) HandleStateSnapshotPage(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleStateSnapshotPage")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req types.StateSnapshotPageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Shard == "" {
		http.Error(w, "missing shard", http.StatusBadRequest)
		return
	}

	// Refresh checkpoint on demand so page reads have deterministic source height.
	if req.SnapshotHeight > 0 {
		if err := hm.dbManager.EnsureStateCheckpoint(req.SnapshotHeight); err != nil {
			writeStateSnapshotPageError(w, req.SnapshotHeight, req.Shard, err)
			return
		}
	}

	page, err := hm.dbManager.PageStateSnapshotShard(req.SnapshotHeight, req.Shard, req.PageSize, req.PageToken)
	if err != nil {
		writeStateSnapshotPageError(w, req.SnapshotHeight, req.Shard, err)
		return
	}

	resp := types.StateSnapshotPageResponse{
		SnapshotHeight: req.SnapshotHeight,
		Shard:          req.Shard,
		Items:          make([]types.StateSnapshotItem, 0, len(page.Items)),
		NextPageToken:  page.NextPageToken,
	}
	for _, item := range page.Items {
		if item.Deleted {
			continue
		}
		valCopy := make([]byte, len(item.Value))
		copy(valCopy, item.Value)
		resp.Items = append(resp.Items, types.StateSnapshotItem{
			Key:   item.Key,
			Value: valCopy,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func writeStateSnapshotShardsError(w http.ResponseWriter, snapshotHeight uint64, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(types.StateSnapshotShardsResponse{
		SnapshotHeight: snapshotHeight,
		Error:          err.Error(),
	})
}

func writeStateSnapshotPageError(w http.ResponseWriter, snapshotHeight uint64, shard string, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(types.StateSnapshotPageResponse{
		SnapshotHeight: snapshotHeight,
		Shard:          shard,
		Error:          err.Error(),
	})
}
