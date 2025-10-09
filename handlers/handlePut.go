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

// 这个接口被 doSendBlock 调用，用于节点间传输完整区块
func (hm *HandlerManager) HandlePut(w http.ResponseWriter, r *http.Request) {
	// 记录API调用
	hm.Stats.RecordAPICall("HandlePut")

	// 1. 读取请求体
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// 2. 解析 protobuf 格式的区块数据
	var block db.Block
	if err := proto.Unmarshal(bodyBytes, &block); err != nil {
		http.Error(w, "Invalid Block proto", http.StatusBadRequest)
		return
	}

	// 3. 验证区块基本信息
	if block.BlockHash == "" || block.BlockHash == "genesis" {
		http.Error(w, "Invalid block hash", http.StatusBadRequest)
		return
	}

	// 4. 将区块保存到数据库
	if err := hm.dbManager.SaveBlock(&block); err != nil {
		logs.Error("[HandlePut] Failed to save block %s: %v", block.BlockHash, err)
		http.Error(w, "Failed to save block", http.StatusInternalServerError)
		return
	}

	// 5. 如果有交易，添加到交易池
	if len(block.Body) > 0 {
		for _, tx := range block.Body {
			if tx != nil {
				// 将交易添加到交易池（标记为已在区块中）
				if err := hm.txPool.StoreAnyTx(tx); err != nil {
					logs.Debug("[HandlePut] Failed to store tx %s: %v",
						tx.GetTxId(), err)
				}
			}
		}
		logs.Debug("[HandlePut] Added %d transactions from block %s to pool",
			len(block.Body), block.BlockHash)
	}

	// 6. 通知共识管理器有新区块
	if hm.consensusManager != nil {

		// 如果需要，触发共识处理
		if hm.adapter != nil {
			// 转换为共识层格式
			consensusBlock, err := hm.adapter.DBBlockToConsensus(&block)
			if err == nil {
				// 构造消息通知共识层
				msg := types.Message{
					Type:    types.MsgPut,
					From:    types.NodeID(block.Miner),
					Block:   consensusBlock,
					BlockID: block.BlockHash,
					Height:  block.Height,
				}

				// 使用RealTransport的消息队列
				if rt, ok := hm.consensusManager.Transport.(*consensus.RealTransport); ok {
					if err := rt.EnqueueReceivedMessage(msg); err != nil {
						logs.Warn("[HandlePut] Failed to enqueue block message: %v", err)
					}
				}
			}
		}
	}
	// 7. 记录成功日志
	logs.Info("[HandlePut] Successfully stored block %s at height %d with %d txs",
		block.BlockHash, block.Height, len(block.Body))

	// 8. 返回成功响应
	w.WriteHeader(http.StatusOK)
}
