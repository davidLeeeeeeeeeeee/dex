package handlers

import (
	"dex/consensus"
	"dex/pb"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// HandleGetData handles /getdata requests for tx payload by tx_id.
func (hm *HandlerManager) HandleGetData(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleGetData")
	if !hm.checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read getdata request body", http.StatusBadRequest)
		return
	}

	var getDataMsg pb.GetData
	if err := proto.Unmarshal(bodyBytes, &getDataMsg); err != nil {
		http.Error(w, "Invalid GetData proto", http.StatusBadRequest)
		return
	}

	// Try TxPool first.
	txFromPool := hm.txPool.GetTransactionById(getDataMsg.TxId)
	if txFromPool != nil {
		respData, _ := proto.Marshal(txFromPool)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
		w.Write(respData)
		return
	}

	// Fallback to DB.
	anyTx, err := hm.dbManager.GetAnyTxById(getDataMsg.TxId)
	if err != nil {
		http.Error(w, fmt.Sprintf("[TxPool: not found] [DB error: %v]", err), http.StatusNotFound)
		return
	}
	if anyTx == nil {
		http.Error(w, fmt.Sprintf("[TxPool: not found] [DB: returned nil for %s]", getDataMsg.TxId), http.StatusNotFound)
		return
	}

	respData, _ := proto.Marshal(anyTx)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(respData)
}

// HandleGetTxReceipt handles /gettxreceipt request.
func (hm *HandlerManager) HandleGetTxReceipt(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleGetTxReceipt")
	if !hm.checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read getdata request body", http.StatusBadRequest)
		return
	}

	var getDataMsg pb.GetData
	if err := proto.Unmarshal(bodyBytes, &getDataMsg); err != nil {
		http.Error(w, "Invalid GetData proto", http.StatusBadRequest)
		return
	}

	receipt, err := hm.dbManager.GetTxReceipt(getDataMsg.TxId)
	if err != nil {
		http.Error(w, fmt.Sprintf("Receipt for %s not found: %v", getDataMsg.TxId, err), http.StatusNotFound)
		return
	}

	respData, _ := proto.Marshal(receipt)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(respData)
}

// HandleGet handles /getblockbyid request.
func (hm *HandlerManager) HandleGet(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleGet")
	if !hm.checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	body, _ := io.ReadAll(r.Body)
	var req pb.GetBlockByIDRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid GetBlockByID proto", http.StatusBadRequest)
		return
	}

	// 1) Prefer global cache so unfinalized blocks can return complete payload.
	var blk *pb.Block
	if cached, exists := consensus.GetCachedBlock(req.BlockId); exists && cached != nil {
		blk = cached
	}

	// 2) Fallback to DB.
	if blk == nil {
		dbBlock, err := hm.dbManager.GetBlockByID(req.BlockId)
		if err == nil && dbBlock != nil {
			blk = dbBlock
		}
	}

	// 3) Fallback to consensus memory/pending buffer.
	if blk == nil && hm.consensusManager != nil {
		if hm.consensusManager.Node != nil && hm.adapter != nil {
			if b, ok := hm.consensusManager.Node.GetBlock(req.BlockId); ok && b != nil {
				blk = hm.adapter.ConsensusBlockToDB(b, nil)
			}
		}

		if blk == nil {
			if pbb := hm.consensusManager.GetPendingBlockBuffer(); pbb != nil {
				if tBlk, shortTxs := pbb.GetPendingBlock(req.BlockId); tBlk != nil {
					blk = hm.adapter.ConsensusBlockToDB(tBlk, nil)
					blk.ShortTxs = shortTxs
				}
			}
		}
	}

	if blk == nil {
		http.Error(w, fmt.Sprintf("Block %s not found (pending or missing)", req.BlockId), http.StatusNotFound)
		return
	}

	// Enrich fallback responses with cached data if possible.
	if len(blk.ShortTxs) == 0 || len(blk.Body) == 0 {
		if cached, exists := consensus.GetCachedBlock(req.BlockId); exists && cached != nil {
			if len(blk.ShortTxs) == 0 && len(cached.ShortTxs) > 0 {
				blk.ShortTxs = cached.ShortTxs
			}
			if len(blk.Body) == 0 && len(cached.Body) > 0 {
				blk.Body = cached.Body
			}
		}
	}

	resp := &pb.GetBlockResponse{Block: blk}
	bytes, _ := proto.Marshal(resp)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(bytes)
}
