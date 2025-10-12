package handlers

import (
	"dex/consensus"
	"dex/db"
	"dex/logs"
	"dex/types"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// 发送端 /put 发的是 protobuf 的 db.Block，这里只解析这一种
func (hm *HandlerManager) HandlePut(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandlePut")

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var block db.Block
	if err := proto.Unmarshal(bodyBytes, &block); err != nil {
		http.Error(w, "Failed to parse protobuf db.Block", http.StatusBadRequest)
		return
	}

	// 基本校验（按你项目习惯来，可保留）
	if block.BlockHash == "" || block.BlockHash == "genesis" {
		http.Error(w, "Invalid block hash", http.StatusBadRequest)
		return
	}

	// 保存区块
	if err := hm.dbManager.SaveBlock(&block); err != nil {
		logs.Error("[HandlePut] Failed to save block %s: %v", block.BlockHash, err)
		http.Error(w, "Failed to save block", http.StatusInternalServerError)
		return
	}

	// 将交易放入交易池（标记为已在区块中）
	if len(block.Body) > 0 {
		for _, tx := range block.Body {
			if tx != nil {
				if err := hm.txPool.StoreAnyTx(tx); err != nil {
					logs.Debug("[HandlePut] Failed to store tx %s: %v", tx.GetTxId(), err)
				}
			}
		}
		logs.Debug("[HandlePut] Added %d transactions from block %s to pool", len(block.Body), block.BlockHash)
	}

	// 通知共识（保持你现有逻辑）
	if hm.consensusManager != nil && hm.adapter != nil {
		if consensusBlock, err := hm.adapter.DBBlockToConsensus(&block); err == nil {
			msg := types.Message{
				RequestID: 0, // /put 不走请求-响应，这里固定 0
				Type:      types.MsgPut,
				From:      types.NodeID(block.Miner),
				Block:     consensusBlock,
				BlockID:   block.BlockHash,
				Height:    block.Height,
			}
			if rt, ok := hm.consensusManager.Transport.(*consensus.RealTransport); ok {
				if err := rt.EnqueueReceivedMessage(msg); err != nil {
					logs.Warn("[HandlePut] Failed to enqueue block message: %v", err)
				}
			}
		}
	}

	logs.Info("[HandlePut] Stored block %s at height %d (txs=%d)", block.BlockHash, block.Height, len(block.Body))
	w.WriteHeader(http.StatusOK)
}
