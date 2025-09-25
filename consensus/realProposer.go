package consensus

import (
	"dex/db"
	"dex/interfaces"
	"dex/logs"
	"dex/txpool"
	"dex/types"
	"dex/utils"
	"fmt"
	"sync"
)

// RealBlockProposer 真实的区块提案者实现，从TxPool获取交易并生成区块
type RealBlockProposer struct {
	maxBlocksPerHeight int
	maxTxsPerBlock     int
	pool               *txpool.TxPool
	dbManager          *db.Manager
}

// NewRealBlockProposer 创建真实的区块提案者
func NewRealBlockProposer(dbManager *db.Manager, pool *txpool.TxPool) interfaces.BlockProposer {
	return &RealBlockProposer{
		maxBlocksPerHeight: 3,
		maxTxsPerBlock:     2500,
		pool:               pool, // 使用注入的实例
		dbManager:          dbManager,
	}
}

// 生成包含实际交易的区块
func (p *RealBlockProposer) ProposeBlock(parentID string, height uint64, proposer types.NodeID, round int) (*types.Block, error) {
	// 1. 从TxPool获取待打包的交易
	pendingTxs := p.pool.GetPendingTxs()
	if len(pendingTxs) == 0 {
		logs.Debug("[RealBlockProposer] no txs for height %d, skip", height)
		return nil, nil
	}
	// 限制交易数量
	if len(pendingTxs) > p.maxTxsPerBlock {
		pendingTxs = pendingTxs[:p.maxTxsPerBlock]
	}

	// 2. 对交易进行排序（按FB余额排序）
	sortedTxs, txsHash, err := txpool.SortTxsByFBBalanceAndComputeHash(p.dbManager, pendingTxs)
	if err != nil {
		logs.Error("[RealBlockProposer] Failed to sort txs: %v", err)
		// 如果排序失败，使用简单的哈希
		txsHash = p.computeSimpleTxsHash(pendingTxs)
		sortedTxs = pendingTxs
	}

	// 3. 生成短交易哈希列表（用于Snowman共识传输）
	shortTxs := p.pool.ConcatFirst8Bytes(sortedTxs)

	// 4. 生成区块ID（结合高度、提案者、轮次和交易哈希）
	blockID := fmt.Sprintf("block-%d-%s-r%d-%s", height, proposer, round, txsHash[:8])

	// 5. 构造实际的区块
	block := &types.Block{
		ID:       blockID,
		Height:   height,
		ParentID: parentID,
		Data: fmt.Sprintf("Height %d, Proposer %s, Round %d, TxCount %d",
			height, proposer, round, len(sortedTxs)),
		Proposer: string(proposer),
		Round:    round,
	}

	// 6. 将区块数据保存到数据库（包含交易信息）
	dbBlock := &db.Block{
		Height:        height,
		TxsHash:       txsHash,
		BlockHash:     blockID,
		PrevBlockHash: parentID,
		Miner:         fmt.Sprintf("node_%s", proposer),
		Body:          sortedTxs,
		ShortTxs:      shortTxs,
	}

	// 临时存储到内存，等区块最终化后再持久化
	p.cacheBlock(blockID, dbBlock)

	logs.Info("[RealBlockProposer] Proposer:%s Proposed block %s with %d txs at height %d", proposer,
		blockID, len(sortedTxs), height)

	return block, nil
}

// ShouldPropose 决定是否应该在当前轮次提出区块
func (p *RealBlockProposer) ShouldPropose(nodeID types.NodeID, round int, currentBlocks int) bool {
	// 如果当前高度已有足够多的区块，不再提案
	if currentBlocks >= p.maxBlocksPerHeight {
		return false
	}

	// 检查TxPool中是否有足够的待处理交易
	pendingCount := len(p.pool.GetPendingAnyTx())
	if pendingCount < 1 { // 至少要有10笔交易才提案
		return false
	}

	// 使用轮次和节点ID的组合来决定是否提案
	// 这里可以加入更复杂的逻辑，比如基于stake的概率
	proposalProbability := 33 // 33%的概率

	return int(nodeID.Last2Mod100()+round)%100 < proposalProbability
}

// computeSimpleTxsHash 计算简单的交易哈希（备用方案）
func (p *RealBlockProposer) computeSimpleTxsHash(txs []*db.AnyTx) string {
	var allBytes []byte
	for _, tx := range txs {
		txID := tx.GetTxId()
		allBytes = append(allBytes, []byte(txID)...)
	}
	hash := utils.Sha256Hash(allBytes)
	return fmt.Sprintf("%x", hash)
}

// cacheBlock 临时缓存区块，等待最终化
var blockCache = make(map[string]*db.Block)
var blockCacheMu sync.RWMutex

func (p *RealBlockProposer) cacheBlock(blockID string, block *db.Block) {
	blockCacheMu.Lock()
	defer blockCacheMu.Unlock()
	blockCache[blockID] = block
}

// GetCachedBlock 获取缓存的区块
func GetCachedBlock(blockID string) (*db.Block, bool) {
	blockCacheMu.RLock()
	defer blockCacheMu.RUnlock()
	block, exists := blockCache[blockID]
	return block, exists
}
