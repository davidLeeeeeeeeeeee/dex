package consensus

import (
	"dex/config"
	"dex/db"
	"dex/interfaces"
	"dex/logs"
	"dex/pb"
	"dex/txpool"
	"dex/types"
	"dex/utils"
	"fmt"
	"sync"
	"time"
)

// RealBlockProposer 真实的区块提案者实现，从TxPool获取交易并生成区块
type RealBlockProposer struct {
	maxBlocksPerHeight int
	maxTxsPerBlock     int
	pool               *txpool.TxPool
	dbManager          *db.Manager
	vrfProvider        *utils.VRFProvider
	windowConfig       config.WindowConfig
}

// NewRealBlockProposer 创建真实的区块提案者
func NewRealBlockProposer(dbManager *db.Manager, pool *txpool.TxPool) interfaces.BlockProposer {
	cfg := config.DefaultConfig()
	return &RealBlockProposer{
		maxBlocksPerHeight: 3,
		maxTxsPerBlock:     cfg.TxPool.MaxTxsPerBlock,
		pool:               pool, // 使用注入的实例
		dbManager:          dbManager,
		vrfProvider:        utils.NewVRFProvider(),
		windowConfig:       cfg.Window,
	}
}

// 生成包含实际交易的区块
func (p *RealBlockProposer) ProposeBlock(parentID string, height uint64, proposer types.NodeID, window int) (*types.Block, error) {
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

	// 3. 生成VRF证明和BLS公钥
	keyMgr := utils.GetKeyManager()
	vrfOutput, vrfProof, err := p.vrfProvider.GenerateVRF(
		keyMgr.PrivateKeyECDSA,
		height,
		window,
		parentID,
		proposer,
	)
	if err != nil {
		logs.Error("[RealBlockProposer] Failed to generate VRF: %v", err)
		return nil, err
	}

	// 获取BLS公钥用于验证
	blsPublicKey, err := utils.GetBLSPublicKey(keyMgr.PrivateKeyECDSA)
	if err != nil {
		logs.Error("[RealBlockProposer] Failed to get BLS public key: %v", err)
		return nil, err
	}
	blsPublicKeyBytes, err := utils.SerializeBLSPublicKey(blsPublicKey)
	if err != nil {
		logs.Error("[RealBlockProposer] Failed to serialize BLS public key: %v", err)
		return nil, err
	}

	// 4. 生成短交易哈希列表（用于Snowman共识传输）
	shortTxs := p.pool.ConcatFirst8Bytes(sortedTxs)

	// 5. 生成区块ID（结合高度、提案者、window和交易哈希）
	blockID := fmt.Sprintf("block-%d-%s-w%d-%s", height, proposer, window, txsHash[:8])

	// 6. 构造实际的区块
	block := &types.Block{
		ID:           blockID,
		Height:       height,
		ParentID:     parentID,
		Data:         fmt.Sprintf("Height %d, Proposer %s, Window %d, TxCount %d", height, proposer, window, len(sortedTxs)),
		Proposer:     string(proposer),
		Window:       window,
		VRFProof:     vrfProof,
		VRFOutput:    vrfOutput,
		BLSPublicKey: blsPublicKeyBytes,
	}

	// 7. 将区块数据保存到数据库（包含交易信息）
	dbBlock := &pb.Block{
		Height:        height,
		TxsHash:       txsHash,
		BlockHash:     blockID,
		PrevBlockHash: parentID,
		Miner:         fmt.Sprintf(db.KeyNode()+"%s", proposer),
		Body:          sortedTxs,
		ShortTxs:      shortTxs,
		Window:        int32(window),
		VrfProof:      vrfProof,
		VrfOutput:     vrfOutput,
		BlsPublicKey:  blsPublicKeyBytes,
	}

	// 临时存储到内存，等区块最终化后再持久化
	p.cacheBlock(blockID, dbBlock)

	logs.Info("[RealBlockProposer] Proposer:%s Proposed block %s with %d txs at height %d window %d",
		proposer, blockID, len(sortedTxs), height, window)

	return block, nil
}

// 决定是否应该在当前时间窗口提出区块
func (p *RealBlockProposer) ShouldPropose(nodeID types.NodeID, window int, currentBlocks int, currentHeight int, proposeHeight int, lastBlockTime time.Time) bool {
	// 新增的高度检查逻辑：当前高度必须是要提议高度减1
	if currentHeight != proposeHeight-1 {
		logs.Debug("[RealBlockProposer] Height check failed: currentHeight=%d, proposeHeight=%d",
			currentHeight, proposeHeight)
		return false
	}

	// 如果当前高度已有足够多的区块，不再提案
	if currentBlocks >= p.maxBlocksPerHeight {
		return false
	}

	// 检查TxPool中是否有足够的待处理交易
	pendingCount := len(p.pool.GetPendingAnyTx())
	if pendingCount < 1 { // 至少要有1笔交易才提案
		return false
	}

	// 检查window配置是否启用
	if !p.windowConfig.Enabled || len(p.windowConfig.Stages) == 0 {
		// 如果未启用window机制，使用简单的随机概率
		return int(nodeID.Last2Mod100())%100 < 5
	}

	// 确保window索引有效
	if window < 0 || window >= len(p.windowConfig.Stages) {
		logs.Debug("[RealBlockProposer] Invalid window %d", window)
		return false
	}

	// 获取当前window的出块概率阈值
	threshold := p.windowConfig.Stages[window].Probability

	// 生成VRF并计算是否应该出块
	keyMgr := utils.GetKeyManager()
	if keyMgr.PrivateKeyECDSA == nil {
		logs.Error("[RealBlockProposer] Private key not initialized")
		return false
	}

	// 使用上一个区块的ID作为父区块哈希（这里简化处理）
	parentID := fmt.Sprintf("parent-%d", currentHeight)

	vrfOutput, _, err := p.vrfProvider.GenerateVRF(
		keyMgr.PrivateKeyECDSA,
		uint64(proposeHeight),
		window,
		parentID,
		nodeID,
	)
	if err != nil {
		logs.Error("[RealBlockProposer] Failed to generate VRF for proposal check: %v", err)
		return false
	}

	// 根据VRF输出和阈值判断是否应该出块
	shouldPropose := utils.CalculateBlockProbability(vrfOutput, threshold)

	if shouldPropose {
		logs.Debug("[RealBlockProposer] Node %s should propose at window %d (probability %.2f%%)",
			nodeID, window, threshold*100)
	}

	return shouldPropose
}

// computeSimpleTxsHash 计算简单的交易哈希（备用方案）
func (p *RealBlockProposer) computeSimpleTxsHash(txs []*pb.AnyTx) string {
	var allBytes []byte
	for _, tx := range txs {
		txID := tx.GetTxId()
		allBytes = append(allBytes, []byte(txID)...)
	}
	hash := utils.Sha256Hash(allBytes)
	return fmt.Sprintf("%x", hash)
}

// cacheBlock 临时缓存区块，等待最终化
var blockCache = make(map[string]*pb.Block)
var blockCacheHeights = make(map[string]uint64) // 记录每个区块的高度，用于清理
var blockCacheMu sync.RWMutex

func (p *RealBlockProposer) cacheBlock(blockID string, block *pb.Block) {
	blockCacheMu.Lock()
	defer blockCacheMu.Unlock()
	blockCache[blockID] = block
	blockCacheHeights[blockID] = block.Height
}

// GetCachedBlock 获取缓存的区块
func GetCachedBlock(blockID string) (*pb.Block, bool) {
	blockCacheMu.RLock()
	defer blockCacheMu.RUnlock()
	block, exists := blockCache[blockID]
	return block, exists
}

// RemoveCachedBlock 从缓存中移除区块
func RemoveCachedBlock(blockID string) {
	blockCacheMu.Lock()
	defer blockCacheMu.Unlock()
	delete(blockCache, blockID)
	delete(blockCacheHeights, blockID)
}

// CleanupBlockCacheBelowHeight 清理低于指定高度的所有缓存区块
func CleanupBlockCacheBelowHeight(height uint64) int {
	blockCacheMu.Lock()
	defer blockCacheMu.Unlock()

	toDelete := make([]string, 0)
	for blockID, h := range blockCacheHeights {
		if h < height {
			toDelete = append(toDelete, blockID)
		}
	}

	for _, blockID := range toDelete {
		delete(blockCache, blockID)
		delete(blockCacheHeights, blockID)
	}

	return len(toDelete)
}

// GetBlockCacheSize 返回缓存大小（用于监控）
func GetBlockCacheSize() int {
	blockCacheMu.RLock()
	defer blockCacheMu.RUnlock()
	return len(blockCache)
}
