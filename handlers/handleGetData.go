package handlers

import (
	"dex/pb"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// HandleGetData 处理来自对等节点的 /getdata 请求。
// 客户端会发送 GetData 消息（包含 tx_id），服务器查找该 tx（例如 AnyTx 或 Transaction），
// 并将完整交易数据返回给请求者。

// 处理获取交易数据请求
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

	// 先从TxPool查找
	txFromPool := hm.txPool.GetTransactionById(getDataMsg.TxId)
	if txFromPool != nil {
		respData, _ := proto.Marshal(txFromPool)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
		w.Write(respData)
		return
	}

	// 从数据库查找
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

// HandleGetTxReceipt 处理获取交易回执的请求
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

	// 从数据库获取对应的 Receipt
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
	blk, err := hm.dbManager.GetBlockByID(req.BlockId)
	if (err != nil || blk == nil) && hm.consensusManager != nil {
		// 1. 尝试从共识 Store 获取（可能在内存尚未落盘）
		if hm.consensusManager.Node != nil && hm.adapter != nil {
			if b, ok := hm.consensusManager.Node.GetBlock(req.BlockId); ok && b != nil {
				blk = hm.adapter.ConsensusBlockToDB(b, nil)
			}
		}

		// 2. 尝试从补课缓冲区获取（支持“短 Hash 传染”机制）
		if blk == nil {
			if pbb := hm.consensusManager.GetPendingBlockBuffer(); pbb != nil {
				if tBlk, shortTxs := pbb.GetPendingBlock(req.BlockId); tBlk != nil {
					// 虽然没有 Body，但返回 Header + ShortTxs，允许请求者也开始补课
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
	resp := &pb.GetBlockResponse{Block: blk}
	bytes, _ := proto.Marshal(resp)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(bytes)
}
