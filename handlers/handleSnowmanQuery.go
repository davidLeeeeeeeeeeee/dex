package handlers

import (
	"compress/gzip"
	"dex/consensus"
	"dex/db"
	"dex/logs"
	"dex/sender"
	"dex/txpool"
	"dex/utils"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"
)

// 全局共识管理器（需要在主程序初始化时设置）
var ConsensusManager *consensus.ConsensusNodeManager

// HandlePushQuery 处理PushQuery请求（发送方有完整容器数据）
func HandlePushQuery(w http.ResponseWriter, r *http.Request) {
	// 1. 读取请求体
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	// 2. 解析PushQuery消息
	var pushQuery db.PushQuery
	if err := proto.Unmarshal(bodyBytes, &pushQuery); err != nil {
		http.Error(w, "Invalid PushQuery proto", http.StatusBadRequest)
		return
	}

	logs.Debug("[HandlePushQuery] Received from %s for block %s at height %d",
		pushQuery.Address, pushQuery.BlockId, pushQuery.RequestedHeight)

	// 3. 处理容器数据
	if err := processContainer(&pushQuery); err != nil {
		logs.Error("[HandlePushQuery] Failed to process container: %v", err)
		http.Error(w, "Failed to process container", http.StatusInternalServerError)
		return
	}

	// 4. 生成Chits响应（包含本节点的投票偏好）
	chits := generateChitsResponse(pushQuery.RequestedHeight)

	// 5. 序列化响应
	respBytes, err := proto.Marshal(chits)
	if err != nil {
		http.Error(w, "Failed to marshal Chits response", http.StatusInternalServerError)
		return
	}

	// 6. 返回Chits
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(respBytes)
}

// HandlePullQuery 处理PullQuery请求（发送方没有容器，只有ID）
func HandlePullQuery(w http.ResponseWriter, r *http.Request) {
	// 1. 读取请求体
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	// 2. 解析PullQuery消息
	var pullQuery db.PullQuery
	if err := proto.Unmarshal(bodyBytes, &pullQuery); err != nil {
		http.Error(w, "Invalid PullQuery proto", http.StatusBadRequest)
		return
	}

	logs.Debug("[HandlePullQuery] Received from %s for block %s",
		pullQuery.Address, pullQuery.BlockId)

	// 3. 检查本地是否有该区块
	if !hasBlock(pullQuery.BlockId) {
		// 如果本地没有，返回空响应或错误
		http.Error(w, "Block not found", http.StatusNotFound)
		return
	}

	// 4. 生成Chits响应
	chits := generateChitsResponse(pullQuery.RequestedHeight)

	// 5. 序列化并返回
	respBytes, err := proto.Marshal(chits)
	if err != nil {
		http.Error(w, "Failed to marshal Chits response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	w.Write(respBytes)
}

// HandlePullContainer 处理容器拉取请求（对方请求完整数据）
func HandlePullContainer(w http.ResponseWriter, r *http.Request) {
	// 1. 读取请求
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	// 2. 解析请求（复用PullQuery结构）
	var pullReq db.PullQuery
	if err := proto.Unmarshal(bodyBytes, &pullReq); err != nil {
		http.Error(w, "Invalid PullContainer request", http.StatusBadRequest)
		return
	}

	logs.Debug("[HandlePullContainer] Request for block %s at height %d",
		pullReq.BlockId, pullReq.RequestedHeight)

	// 3. 获取完整容器数据
	container, isBlock, err := getFullContainer(pullReq.BlockId, pullReq.RequestedHeight)
	if err != nil {
		logs.Error("[HandlePullContainer] Failed to get container: %v", err)
		http.Error(w, "Container not found", http.StatusNotFound)
		return
	}

	// 4. 构造PushQuery响应（包含完整数据）
	pushResponse := &db.PushQuery{
		Address:          getLocalAddress(),
		BlockId:          pullReq.BlockId,
		RequestedHeight:  pullReq.RequestedHeight,
		Container:        container,
		ContainerIsBlock: isBlock,
		Deadline:         0, // 不需要超时
	}

	// 5. 序列化响应
	respBytes, err := proto.Marshal(pushResponse)
	if err != nil {
		http.Error(w, "Failed to marshal response", http.StatusInternalServerError)
		return
	}

	// 6. 支持压缩传输
	if len(respBytes) > 10240 { // 超过10KB时压缩
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
		gz.Write(respBytes)
	} else {
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
		w.Write(respBytes)
	}
}

// processContainer 处理接收到的容器数据
func processContainer(pushQuery *db.PushQuery) error {
	if pushQuery.ContainerIsBlock {
		// 完整区块数据
		var block db.Block
		if err := proto.Unmarshal(pushQuery.Container, &block); err != nil {
			return err
		}

		// 将区块添加到共识引擎
		if ConsensusManager != nil {
			return ConsensusManager.AddBlock(&block)
		}
	} else {
		// 短哈希列表，需要从TxPool获取完整交易
		pool := txpool.GetInstance()

		// 分析短哈希，找出缺失的交易
		missing, _ := pool.AnalyzeProposalTxs(pushQuery.Container)

		if len(missing) > 0 {
			// 异步拉取缺失的交易
			go fetchMissingTxs(pushQuery.Address, missing)

			// 暂时返回，等交易齐全后再处理
			return nil
		}

		// 所有交易都有，可以处理区块
		return processBlockWithShortHashes(pushQuery)
	}

	return nil
}

// generateChitsResponse 生成Chits投票响应
func generateChitsResponse(requestedHeight uint64) *db.Chits {
	// 获取本节点的偏好和接受状态
	preferredBlock := ""
	acceptedBlock := ""
	acceptedHeight := uint64(0)

	if ConsensusManager != nil {
		preferredBlock = ConsensusManager.GetPreference(requestedHeight)
		acceptedBlock, acceptedHeight = ConsensusManager.GetLastAccepted()
	} else {
		// 使用默认值或从数据库获取
		mgr, _ := db.NewManager("")
		if mgr != nil {
			if height, err := db.GetLatestBlockHeight(mgr); err == nil {
				acceptedHeight = height
				if block, err := db.GetBlock(mgr, height); err == nil {
					acceptedBlock = block.BlockHash
					preferredBlock = block.BlockHash
				}
			}
		}
	}

	// 获取位图（表示连接的节点）
	bitmap := generateBitmap()

	return &db.Chits{
		PreferredBlock:         preferredBlock,
		AcceptedBlock:          acceptedBlock,
		PreferredBlockAtHeight: preferredBlock,
		AcceptedHeight:         acceptedHeight,
		Bitmap:                 bitmap,
	}
}

// hasBlock 检查本地是否有指定区块
func hasBlock(blockId string) bool {
	if ConsensusManager != nil {
		return ConsensusManager.HasBlock(blockId)
	}

	// 从数据库检查
	mgr, _ := db.NewManager("")
	if mgr != nil {
		// 这里需要实现根据blockId查询的逻辑
		// 简化处理：假设blockId包含高度信息
		return false
	}

	return false
}

// getFullContainer 获取完整的容器数据
func getFullContainer(blockId string, height uint64) ([]byte, bool, error) {
	// 1. 尝试从缓存获取
	if cachedBlock, exists := consensus.GetCachedBlock(blockId); exists {
		if len(cachedBlock.Body) < 2500 {
			// 交易数少，返回完整数据
			data, err := proto.Marshal(cachedBlock)
			return data, true, err
		} else {
			// 交易数多，返回短哈希
			return cachedBlock.ShortTxs, false, nil
		}
	}

	// 2. 从数据库获取
	mgr, err := db.NewManager("")
	if err != nil {
		return nil, false, err
	}

	block, err := db.GetBlock(mgr, height)
	if err != nil {
		return nil, false, err
	}

	if block.BlockHash != blockId {
		return nil, false, fmt.Errorf("block ID mismatch")
	}

	// 根据交易数量决定返回格式
	if len(block.Body) < 2500 {
		data, err := proto.Marshal(block)
		return data, true, err
	} else {
		return block.ShortTxs, false, nil
	}
}

// processBlockWithShortHashes 处理包含短哈希的区块
func processBlockWithShortHashes(pushQuery *db.PushQuery) error {
	pool := txpool.GetInstance()

	// 从短哈希恢复交易列表
	const shortHashSize = 8
	var txs []*db.AnyTx

	for i := 0; i+shortHashSize <= len(pushQuery.Container); i += shortHashSize {
		shortHash := pushQuery.Container[i : i+shortHashSize]
		matchedTxs := pool.GetTxsByShortHashes([][]byte{shortHash}, true)
		if len(matchedTxs) > 0 {
			txs = append(txs, matchedTxs[0])
		}
	}

	// 构造区块
	block := &db.Block{
		Height:    pushQuery.RequestedHeight,
		BlockHash: pushQuery.BlockId,
		Body:      txs,
		ShortTxs:  pushQuery.Container,
	}

	// 添加到共识引擎
	if ConsensusManager != nil {
		return ConsensusManager.AddBlock(block)
	}

	return nil
}

// fetchMissingTxs 异步拉取缺失的交易
func fetchMissingTxs(peerAddress string, missingHashes map[string]bool) {
	// 使用sender模块的批量获取交易功能
	sender.BatchGetTxs(peerAddress, missingHashes, func(txs []*db.AnyTx) {
		// 将获取到的交易存入TxPool
		pool := txpool.GetInstance()
		for _, tx := range txs {
			if err := pool.StoreAnyTx(tx); err != nil {
				logs.Error("[fetchMissingTxs] Failed to store tx: %v", err)
			}
		}
		logs.Info("[fetchMissingTxs] Fetched %d missing txs from %s",
			len(txs), peerAddress)
	})
}

// generateBitmap 生成节点连接位图
func generateBitmap() []byte {
	// 这里简化处理，实际应该维护节点连接状态
	// 位图表示哪些节点与本节点有连接
	bitmap := make([]byte, 128) // 支持1024个节点

	// 设置一些示例位
	// bitmap[0] = 0xFF 表示节点0-7都连接

	return bitmap
}

// getLocalAddress 获取本地节点地址
func getLocalAddress() string {
	// 从KeyManager获取
	keyMgr := utils.GetKeyManager()
	if keyMgr != nil {
		return keyMgr.GetAddress()
	}
	return ""
}
