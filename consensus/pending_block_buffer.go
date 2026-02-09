package consensus

import (
	"dex/logs"
	"dex/pb"
	"dex/sender"
	"dex/txpool"
	"dex/types"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// PendingBlockBuffer 待处理区块缓冲区
// 用于存储因缺失交易而暂时无法还原的区块，支持重试机制
type PendingBlockBuffer struct {
	mu            sync.RWMutex
	pending       map[string]*pendingBlockEntry // blockID -> entry
	txPool        *txpool.TxPool
	adapter       *ConsensusAdapter
	senderManager *sender.SenderManager
	stopChan      chan struct{}
	logger        logs.Logger
}

type pendingBlockEntry struct {
	block         *types.Block // 区块头（types.Block）
	shortTxs      []byte
	height        uint64
	blockID       string
	sources       []string // 改为来源节点地址列表，支持多源重试
	retryCount    int
	nextRetry     time.Time
	maxRetries    int
	onSuccess     func(*pb.Block)    // 原有回调（返回 pb.Block）
	onSuccessType func(*types.Block) // 新增回调（返回 types.Block，用于注入共识）
	missingHashes [][]byte           // 缺失的短哈希
	requestID     uint32             // 原始请求 ID，用于补发 Chits
	fromNode      types.NodeID       // 来源节点，用于补发 Chits
	proposer      string             // 提议者地址，作为最高优先级拉取源
}

const (
	initialRetryDelay = 200 * time.Millisecond
	maxRetryDelay     = 2 * time.Second
	defaultMaxRetries = 5
)

// NewPendingBlockBuffer 创建待处理区块缓冲区
func NewPendingBlockBuffer(
	txPool *txpool.TxPool,
	adapter *ConsensusAdapter,
	senderManager *sender.SenderManager,
	logger logs.Logger,
) *PendingBlockBuffer {
	return &PendingBlockBuffer{
		pending:       make(map[string]*pendingBlockEntry),
		txPool:        txPool,
		adapter:       adapter,
		senderManager: senderManager,
		stopChan:      make(chan struct{}),
		logger:        logger,
	}
}

// GetPendingBlock 获取正在补课中的区块（包含头和 ShortTxs，可能暂无 Body）
func (b *PendingBlockBuffer) GetPendingBlock(blockID string) (*types.Block, []byte) {
	if b == nil {
		return nil, nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	entry, ok := b.pending[blockID]
	if !ok {
		return nil, nil
	}
	return entry.block, entry.shortTxs
}

// Start 启动重试循环
func (b *PendingBlockBuffer) Start() {
	go b.retryLoop()
}

// Stop 停止缓冲区
func (b *PendingBlockBuffer) Stop() {
	close(b.stopChan)
}

// AddPendingBlock 添加待处理区块
func (b *PendingBlockBuffer) AddPendingBlock(
	shortTxs []byte,
	height uint64,
	blockID string,
	peerAddress string,
	missingHashes [][]byte,
	onSuccess func(*pb.Block),
) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// 如果已存在，不重复添加
	if _, exists := b.pending[blockID]; exists {
		return
	}

	entry := &pendingBlockEntry{
		shortTxs:      shortTxs,
		height:        height,
		blockID:       blockID,
		sources:       []string{peerAddress}, // Changed from peerAddress to sources
		retryCount:    0,
		nextRetry:     time.Now().Add(initialRetryDelay),
		maxRetries:    defaultMaxRetries,
		onSuccess:     onSuccess,
		missingHashes: missingHashes,
	}

	b.pending[blockID] = entry

	if b.logger != nil {
		b.logger.Verbose("[PendingBlockBuffer] Added block %s at height %d, missing %d txs",
			blockID, height, len(missingHashes))
	}

	// 立即尝试拉取缺失交易
	go b.fetchMissingTxs(entry)
}

// AddPendingBlockForConsensus 添加待处理区块（用于共识流程）
// 当数据齐全后触发 onSuccess 回调，将区块注入共识
func (b *PendingBlockBuffer) AddPendingBlockForConsensus(
	block *types.Block,
	shortTxs []byte,
	fromNode types.NodeID,
	requestID uint32,
	onSuccess func(*types.Block),
) error {
	if block == nil {
		return fmt.Errorf("block is nil")
	}

	// 解析短哈希，获取缺失列表
	_, missingHashes, err := b.adapter.ResolveShorHashesToTxsWithMissing(shortTxs)
	if err != nil {
		return err
	}

	// 如果数据已齐全，直接构建完整区块并触发回调
	if len(missingHashes) == 0 {
		txs, _, _ := b.adapter.ResolveShorHashesToTxsWithMissing(shortTxs)
		// 构建完整的 pb.Block 并缓存
		fullBlock := b.buildFullPbBlock(block, txs)
		CacheBlock(fullBlock)

		if b.logger != nil {
			b.logger.Info("[PendingBlockBuffer] Block %s already complete, injecting to consensus", block.ID)
		}

		// 触发回调
		if onSuccess != nil {
			onSuccess(block)
		}
		return nil
	}

	// 获取来源节点的 IP 地址
	peerIP := b.getNodeIP(fromNode)
	proposerIP := ""
	if block.Header.Proposer != "" {
		// 尝试将提议者 nodeID 转换为 IP
		proposerIP, _ = b.senderManager.AddressToIP(block.Header.Proposer)
	}

	b.mu.Lock()
	if entry, exists := b.pending[block.ID]; exists {
		// 如果块已存在，追加新节点
		newAddr := peerIP
		found := false
		for _, s := range entry.sources {
			if s == newAddr {
				found = true
				break
			}
		}
		if !found && newAddr != "" {
			entry.sources = append(entry.sources, newAddr)
		}
		if entry.proposer == "" {
			entry.proposer = proposerIP
		}
		b.mu.Unlock()
		return nil
	}

	entry := &pendingBlockEntry{
		block:         block,
		shortTxs:      shortTxs,
		height:        block.Header.Height,
		blockID:       block.ID,
		sources:       []string{peerIP},
		retryCount:    0,
		nextRetry:     time.Now().Add(initialRetryDelay),
		maxRetries:    defaultMaxRetries,
		onSuccessType: onSuccess,
		missingHashes: missingHashes,
		requestID:     requestID,
		fromNode:      fromNode,
		proposer:      proposerIP,
	}

	b.pending[block.ID] = entry
	b.mu.Unlock()

	if b.logger != nil {
		b.logger.Verbose("[PendingBlockBuffer] Added consensus block %s at height %d, missing %d txs",
			block.ID, block.Header.Height, len(missingHashes))
	}

	// 立即尝试拉取缺失交易
	go b.fetchMissingTxs(entry)

	return nil
}

// getNodeIP 获取节点 IP（简化实现，从 peerAddress 或配置获取）
func (b *PendingBlockBuffer) getNodeIP(nodeID types.NodeID) string {
	// 如果 senderManager 有获取 IP 的方法，可以调用
	// 这里暂时返回空串，由 fetchMissingTxs 处理
	return string(nodeID)
}

// buildFullPbBlock 构建完整的 pb.Block
func (b *PendingBlockBuffer) buildFullPbBlock(block *types.Block, txs []*pb.AnyTx) *pb.Block {
	return &pb.Block{
		BlockHash: block.ID,
		Header: &pb.BlockHeader{
			Height:        block.Header.Height,
			PrevBlockHash: block.Header.ParentID,
			Timestamp:     block.Header.Timestamp,
			StateRoot:     []byte(block.Header.StateRoot),
			TxsHash:       block.Header.TxHash,
			Miner:         block.Header.Proposer,
			Window:        int32(block.Header.Window),
			VrfProof:      block.Header.VRFProof,
			VrfOutput:     block.Header.VRFOutput,
			BlsPublicKey:  block.Header.BLSPublicKey,
		},
		Body: txs,
	}
}

// fetchMissingTxs 主动拉取缺失交易
func (b *PendingBlockBuffer) fetchMissingTxs(entry *pendingBlockEntry) {
	if b.senderManager == nil || len(entry.missingHashes) == 0 {
		return
	}

	// 构建缺失哈希的 map
	missingMap := make(map[string]bool)
	for _, hash := range entry.missingHashes {
		missingMap[hex.EncodeToString(hash)] = true
	}

	// 尝试从不同的 source 拉取。优先找 proposer，其次按顺序/随机轮询 sources
	b.mu.RLock()
	targetPeer := ""
	if entry.proposer != "" {
		targetPeer = entry.proposer
	} else if len(entry.sources) > 0 {
		targetPeer = entry.sources[entry.retryCount%len(entry.sources)]
	}
	b.mu.RUnlock()

	if targetPeer == "" {
		return
	}

	if b.logger != nil {
		b.logger.Verbose("[PendingBlockBuffer] Fetching %d missing txs for block %s from %s (retry %d)",
			len(missingMap), entry.blockID, targetPeer, entry.retryCount)
	}

	// 调用 BatchGetTxs 拉取
	b.senderManager.BatchGetTxs(targetPeer, missingMap, func(txs []*pb.AnyTx) {
		if b.logger != nil {
			b.logger.Verbose("[PendingBlockBuffer] Received %d txs for block %s from %s",
				len(txs), entry.blockID, targetPeer)
		}

		// 存入 TxPool
		for _, tx := range txs {
			if err := b.txPool.CacheAnyTx(tx); err != nil {
				if b.logger != nil {
					b.logger.Debug("[PendingBlockBuffer] Failed to store tx: %v", err)
				}
			}
		}

		// 触发重试
		b.triggerRetry(entry.blockID)
	})
}

// triggerRetry 立即触发重试
func (b *PendingBlockBuffer) triggerRetry(blockID string) {
	b.mu.Lock()
	if entry, exists := b.pending[blockID]; exists {
		entry.nextRetry = time.Now() // 立即重试
	}
	b.mu.Unlock()
}

// retryLoop 重试循环
func (b *PendingBlockBuffer) retryLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-b.stopChan:
			return
		case <-ticker.C:
			b.processRetries()
		}
	}
}

// processRetries 处理到期的重试
func (b *PendingBlockBuffer) processRetries() {
	b.mu.Lock()
	now := time.Now()
	var toProcess []*pendingBlockEntry
	var toRemove []string

	for blockID, entry := range b.pending {
		if now.After(entry.nextRetry) {
			if entry.retryCount >= entry.maxRetries {
				toRemove = append(toRemove, blockID)
				if b.logger != nil {
					b.logger.Warn("[PendingBlockBuffer] Block %s exceeded max retries, giving up", blockID)
				}
			} else {
				toProcess = append(toProcess, entry)
			}
		}
	}

	// 移除超限的
	for _, blockID := range toRemove {
		delete(b.pending, blockID)
	}
	b.mu.Unlock()

	// 处理待重试的
	for _, entry := range toProcess {
		b.retryResolve(entry)
	}
}

// retryResolve 重试还原区块
func (b *PendingBlockBuffer) retryResolve(entry *pendingBlockEntry) {
	// 尝试还原
	txs, missingHashes, err := b.adapter.ResolveShorHashesToTxsWithMissing(entry.shortTxs)
	if err != nil {
		if b.logger != nil {
			b.logger.Debug("[PendingBlockBuffer] Resolve error for block %s: %v", entry.blockID, err)
		}
		b.updateRetry(entry, missingHashes)
		return
	}

	if len(missingHashes) > 0 {
		// 仍有缺失，更新并继续拉取
		if b.logger != nil {
			b.logger.Verbose("[PendingBlockBuffer] Block %s still missing %d txs (retry %d/%d)",
				entry.blockID, len(missingHashes), entry.retryCount+1, entry.maxRetries)
		}
		b.updateRetry(entry, missingHashes)

		// 再次尝试拉取
		entry.missingHashes = missingHashes
		go b.fetchMissingTxs(entry)
		return
	}

	// 成功还原
	if b.logger != nil {
		b.logger.Info("[PendingBlockBuffer] Successfully resolved block %s with %d txs",
			entry.blockID, len(txs))
	}

	// 构建完整区块
	var fullPbBlock *pb.Block
	if entry.block != nil {
		// 使用已有的区块头信息构建完整区块
		fullPbBlock = b.buildFullPbBlock(entry.block, txs)
	} else {
		// 兼容旧逻辑
		fullPbBlock = &pb.Block{
			BlockHash: entry.blockID,
			Header: &pb.BlockHeader{
				Height: entry.height,
			},
			Body:     txs,
			ShortTxs: entry.shortTxs,
		}
	}

	// 缓存完整区块（关键：这样 RealBlockStore.Add 才能通过数据完整性检查）
	CacheBlock(fullPbBlock)

	if b.logger != nil {
		b.logger.Info("[PendingBlockBuffer] ✅ Successfully resolved block %s (height %d) after fetching missing txs", entry.blockID, entry.height)
	}

	// 从待处理中移除
	b.mu.Lock()
	delete(b.pending, entry.blockID)
	b.mu.Unlock()

	// 回调通知（新的 types.Block 回调优先）
	if entry.onSuccessType != nil && entry.block != nil {
		entry.onSuccessType(entry.block)
	} else if entry.onSuccess != nil {
		entry.onSuccess(fullPbBlock)
	}
}

// updateRetry 更新重试信息
func (b *PendingBlockBuffer) updateRetry(entry *pendingBlockEntry, missingHashes [][]byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	entry.retryCount++
	entry.missingHashes = missingHashes

	// 指数退避
	delay := initialRetryDelay * time.Duration(1<<uint(entry.retryCount))
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}
	entry.nextRetry = time.Now().Add(delay)
}

// HasPending 检查是否有待处理区块
func (b *PendingBlockBuffer) HasPending(blockID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, exists := b.pending[blockID]
	return exists
}

// GetPendingCount 获取待处理区块数量
func (b *PendingBlockBuffer) GetPendingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}
