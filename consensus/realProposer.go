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
	maxBlocksPerHeight  int
	maxTxsPerBlock      int // ShortTxs 切换阈值
	maxTxsLimitPerBlock int // 区块交易总数限制 (5000)
	pool                *txpool.TxPool
	dbManager           *db.Manager
	vrfProvider         *utils.VRFProvider
	windowConfig        config.WindowConfig
}

// NewRealBlockProposer 创建真实的区块提案者
func NewRealBlockProposer(dbManager *db.Manager, pool *txpool.TxPool) interfaces.BlockProposer {
	cfg := config.DefaultConfig()
	return &RealBlockProposer{
		maxBlocksPerHeight:  3,
		maxTxsPerBlock:      cfg.TxPool.MaxTxsPerBlock,
		maxTxsLimitPerBlock: cfg.TxPool.MaxTxsLimitPerBlock,
		pool:                pool, // 使用注入的实例
		dbManager:           dbManager,
		vrfProvider:         utils.NewVRFProvider(),
		windowConfig:        cfg.Window,
	}
}

// 生成包含实际交易的区块
func (p *RealBlockProposer) ProposeBlock(parentID string, height uint64, proposer types.NodeID, window int) (*types.Block, error) {
	// 1. 从TxPool获取待打包的交易
	pendingTxs := p.pool.GetPendingTxs()
	// 如果没有交易，且当前window较小，不生成区块（除非强制出空块维持活性）
	// 这里我们在ShouldPropose已经做了控制，如果进了这里说明 permitted to propose empty block
	if len(pendingTxs) == 0 {
		logs.Debug("[RealBlockProposer] Generating empty block at height %d window %d", height, window)
	}

	// 限制交易数量，最高 5000 笔
	if len(pendingTxs) > p.maxTxsLimitPerBlock {
		pendingTxs = pendingTxs[:p.maxTxsLimitPerBlock]
	}

	// 2. 计算交易哈希（提案者决定交易顺序，不再排序）
	txsHash := txpool.ComputeTxsHash(pendingTxs)

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
	shortTxs := p.pool.ConcatFirst8Bytes(pendingTxs)

	// 5. 生成区块ID（结合高度、提案者、window和交易哈希）
	blockID := fmt.Sprintf("block-%d-%s-w%d-%s", height, proposer, window, txsHash[:8])

	// 6. 构造实际的区块
	block := &types.Block{
		ID: blockID,
		Header: types.BlockHeader{
			Height:       height,
			ParentID:     parentID,
			Proposer:     string(proposer),
			Window:       window,
			VRFProof:     vrfProof,
			VRFOutput:    vrfOutput,
			BLSPublicKey: blsPublicKeyBytes,
		},
	}

	// 7. 将区块数据保存到数据库（包含交易信息）
	dbBlock := &pb.Block{
		BlockHash: blockID,
		Header: &pb.BlockHeader{
			Height:        height,
			TxsHash:       txsHash,
			PrevBlockHash: parentID,
			Miner:         fmt.Sprintf(db.KeyNode()+"%s", proposer),
			Window:        int32(window),
			VrfProof:      vrfProof,
			VrfOutput:     vrfOutput,
			BlsPublicKey:  blsPublicKeyBytes,
		},
		Body:     pendingTxs,
		ShortTxs: shortTxs,
	}

	// 临时存储到内存，等区块最终化后再持久化
	p.cacheBlock(blockID, dbBlock)

	logs.Info("[RealBlockProposer] Proposer:%s Proposed block %s with %d txs at height %d window %d",
		proposer, blockID, len(pendingTxs), height, window)

	return block, nil
}

// 决定是否应该在当前时间窗口提出区块
func (p *RealBlockProposer) ShouldPropose(nodeID types.NodeID, window int, currentBlocks int, currentHeight int, proposeHeight int, lastBlockTime time.Time, parentID string) bool {
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
	// 策略修改：
	// 1. 如果有交易，任何window都可以出块
	// 2. 如果没交易，只有在 window >= 4 时才允许出空块（维持活性）
	if pendingCount < 1 && window < 4 {
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

	// 默认 parentID 之前处理逻辑已移除，现在直接使用传入的真实 parentID
	if parentID == "" {
		if currentHeight == 0 {
			parentID = "genesis"
		} else {
			// 如果没传且不是高度1，尝试回退（虽然理论上 ProposalManager 会传）
			parentID = fmt.Sprintf("block-%d", currentHeight)
		}
	}

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

// cacheBlock 临时缓存区块，等待最终化
var blockCache = make(map[string]*pb.Block)
var blockCacheHeights = make(map[string]uint64) // 记录每个区块的高度，用于清理
var blockCacheMu sync.RWMutex

func (p *RealBlockProposer) cacheBlock(blockID string, block *pb.Block) {
	blockCacheMu.Lock()
	defer blockCacheMu.Unlock()
	blockCache[blockID] = block
	blockCacheHeights[blockID] = block.Header.Height
}

// GetCachedBlock 获取缓存的区块
func GetCachedBlock(blockID string) (*pb.Block, bool) {
	blockCacheMu.RLock()
	defer blockCacheMu.RUnlock()
	block, exists := blockCache[blockID]
	return block, exists
}

// CacheBlock 缓存区块（公开方法，供其他模块使用）
func CacheBlock(block *pb.Block) {
	if block == nil {
		return
	}
	blockCacheMu.Lock()
	defer blockCacheMu.Unlock()
	blockCache[block.BlockHash] = block
	if block.Header != nil {
		blockCacheHeights[block.BlockHash] = block.Header.Height
	}
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
