package handlers

import (
	"dex/consensus"
	"dex/db"
	"dex/logs"
	"dex/types"
	"encoding/json"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

func (hm *HandlerManager) HandleBlockGossip(w http.ResponseWriter, r *http.Request) {
	hm.Stats.RecordAPICall("HandleBlockGossip0")
	// 1. 读取请求体
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	// 2. 尝试解析为 types.Block（JSON格式）
	var block types.Block
	if err := json.Unmarshal(bodyBytes, &block); err != nil {
		// 如果不是JSON格式，可能是protobuf格式的db.Block
		var dbBlock db.Block
		if protoErr := proto.Unmarshal(bodyBytes, &dbBlock); protoErr == nil {
			// 转换db.Block为types.Block
			consensusBlock, convertErr := hm.adapter.DBBlockToConsensus(&dbBlock)
			if convertErr != nil {
				http.Error(w, "Failed to convert block", http.StatusBadRequest)
				return
			}
			block = *consensusBlock
		} else {
			http.Error(w, "Invalid block format", http.StatusBadRequest)
			return
		}
	}

	// 3. 验证区块基本信息
	if block.ID == "" || block.ID == "genesis" {
		http.Error(w, "Invalid block ID", http.StatusBadRequest)
		return
	}
	// ========== 已知块短路逻辑 ==========
	// 3.1 检查 LRU 缓存中是否已处理过该区块
	if hm.seenBlocksCache.Contains(block.ID) {
		logs.Debug("[HandleBlockGossip] Block %s already seen in cache, returning 200 OK", block.ID)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
		return
	} else {
		// 添加到缓存以加速后续查询
		hm.seenBlocksCache.Add(block.ID, true)
	}
	hm.Stats.RecordAPICall("HandleBlockGossip1")
	// 4. 构造共识层的Gossip消息
	gossipMsg := types.Message{
		Type:    types.MsgGossip,
		From:    types.NodeID(block.Proposer),
		Block:   &block,
		BlockID: block.ID,
		Height:  block.Height,
	}

	// 5. 如果有共识管理器，将消息传递给它处理
	if hm.consensusManager != nil {
		// 添加区块到共识存储
		dbBlock := hm.adapter.ConsensusBlockToDB(&block, nil)
		if err := hm.consensusManager.AddBlock(dbBlock); err != nil {
			logs.Debug("[HandleBlockGossip] Failed to add block to consensus: %v", err)
			// 不返回错误，因为区块可能已存在
		}

		// 如果使用RealTransport，通过消息队列处理
		if rt, ok := hm.consensusManager.Transport.(*consensus.RealTransport); ok {
			if err := rt.EnqueueReceivedMessage(gossipMsg); err != nil {
				logs.Warn("[HandleBlockGossip] Failed to enqueue gossip message: %v", err)
			}
		} else {
			// 直接处理消息
			hm.consensusManager.ProcessMessage(gossipMsg)
		}

		logs.Debug("[HandleBlockGossip] Received block %s at height %d Proposer %s",
			block.ID, block.Height, block.Proposer)
	}

	// 7. 如果区块包含交易，处理交易
	if dbBlock, ok := consensus.GetCachedBlock(block.ID); ok && len(dbBlock.Body) > 0 {
		// 将交易添加到交易池
		for _, tx := range dbBlock.Body {
			if err := hm.txPool.StoreAnyTx(tx); err != nil {
				logs.Debug("[HandleBlockGossip] Failed to store tx %s: %v",
					tx.GetTxId(), err)
			}
		}
	}

	// 8. 返回成功响应
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}
