package consensus

import (
	"dex/config"
	"dex/db"
	"dex/interfaces"
	"dex/keys"
	"dex/logs"
	"dex/pb"
	"dex/txpool"
	"dex/types"
	"dex/utils"
	"fmt"
	"sync"
	"time"
)

// RealBlockProposer proposes real blocks from TxPool transactions.
type RealBlockProposer struct {
	maxBlocksPerHeight  int
	maxTxsPerBlock      int
	maxTxsLimitPerBlock int
	pool                *txpool.TxPool
	dbManager           *db.Manager
	vrfProvider         *utils.VRFProvider
	windowConfig        config.WindowConfig
}

func NewRealBlockProposer(dbManager *db.Manager, pool *txpool.TxPool) interfaces.BlockProposer {
	cfg := config.DefaultConfig()
	return &RealBlockProposer{
		maxBlocksPerHeight:  2,
		maxTxsPerBlock:      cfg.TxPool.MaxTxsPerBlock,
		maxTxsLimitPerBlock: cfg.TxPool.MaxTxsLimitPerBlock,
		pool:                pool,
		dbManager:           dbManager,
		vrfProvider:         utils.NewVRFProvider(),
		windowConfig:        cfg.Window,
	}
}

func (p *RealBlockProposer) ProposeBlock(parentID string, height uint64, proposer types.NodeID, window int) (*types.Block, error) {
	pendingTxs := p.pool.GetPendingTxs()
	pendingCount := len(pendingTxs)
	if pendingCount == 0 {
		logs.Debug("[RealBlockProposer] Generating empty block at height %d window %d", height, window)
	}

	effectiveLimit := p.adaptiveTxLimit(pendingCount)
	if pendingCount > effectiveLimit {
		pendingTxs = pendingTxs[:effectiveLimit]
	}

	txsHash := txpool.ComputeTxsHash(pendingTxs)

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

	shortTxs := p.pool.ConcatFirst8Bytes(pendingTxs)

	blockID := fmt.Sprintf("block-%d-%s-w%d-%s", height, proposer, window, txsHash[:8])

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

	dbBlock := &pb.Block{
		BlockHash: blockID,
		Header: &pb.BlockHeader{
			Height:        height,
			TxsHash:       txsHash,
			PrevBlockHash: parentID,
			Miner:         fmt.Sprintf(keys.KeyNode()+"%s", proposer),
			Window:        int32(window),
			VrfProof:      vrfProof,
			VrfOutput:     vrfOutput,
			BlsPublicKey:  blsPublicKeyBytes,
		},
		Body:     pendingTxs,
		ShortTxs: shortTxs,
	}

	p.cacheBlock(blockID, dbBlock)

	logs.Info("[RealBlockProposer] Proposer:%s Proposed block %s with %d txs at height %d window %d",
		proposer, blockID, len(pendingTxs), height, window)

	return block, nil
}

func (p *RealBlockProposer) ShouldPropose(
	nodeID types.NodeID,
	window int,
	currentBlocks int,
	currentHeight int,
	proposeHeight int,
	lastBlockTime time.Time,
	parentID string,
) bool {
	if currentHeight != proposeHeight-1 {
		logs.Debug("[RealBlockProposer] Height check failed: currentHeight=%d, proposeHeight=%d",
			currentHeight, proposeHeight)
		return false
	}

	// Liveness escape: keep strict cap in early windows, relax in late windows.
	// Without this, two early competing blocks can permanently freeze a height.
	allowedBlocks := p.maxBlocksPerHeight
	if allowedBlocks <= 0 {
		allowedBlocks = 2
	}
	switch {
	case window >= 3:
		allowedBlocks += 4 // late windows: allow up to 6 candidates
	case window >= 2:
		allowedBlocks += 2 // mid windows: allow up to 4 candidates
	}
	if currentBlocks >= allowedBlocks {
		return false
	}

	pendingCount := len(p.pool.GetPendingAnyTx())
	maxTxsPerBlock := p.maxTxsPerBlock
	if maxTxsPerBlock <= 0 {
		maxTxsPerBlock = 2500
	}

	// Keep early-window rate limiting, but do not hard-block later windows.
	if currentBlocks > 0 && window == 0 && pendingCount >= maxTxsPerBlock {
		return false
	}

	// Allow empty blocks in the last configured window to preserve liveness.
	// Previous `window < 4` made empty blocks impossible when stages are 0..3.
	lastWindow := 3
	if p.windowConfig.Enabled && len(p.windowConfig.Stages) > 0 {
		lastWindow = len(p.windowConfig.Stages) - 1
	}
	if pendingCount < 1 && window < lastWindow {
		return false
	}

	if !p.windowConfig.Enabled || len(p.windowConfig.Stages) == 0 {
		return int(nodeID.Last2Mod100())%100 < 5
	}

	if window < 0 || window >= len(p.windowConfig.Stages) {
		logs.Debug("[RealBlockProposer] Invalid window %d", window)
		return false
	}

	threshold := p.windowConfig.Stages[window].Probability

	keyMgr := utils.GetKeyManager()
	if keyMgr.PrivateKeyECDSA == nil {
		logs.Error("[RealBlockProposer] Private key not initialized")
		return false
	}

	if parentID == "" {
		if currentHeight == 0 {
			parentID = "genesis"
		} else {
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

	shouldPropose := utils.CalculateBlockProbability(vrfOutput, threshold)
	if shouldPropose {
		logs.Debug("[RealBlockProposer] Node %s should propose at window %d (probability %.2f%%)",
			nodeID, window, threshold*100)
	}

	return shouldPropose
}

func (p *RealBlockProposer) adaptiveTxLimit(pendingCount int) int {
	limit := p.maxTxsLimitPerBlock
	if p.maxTxsPerBlock > 0 && (limit <= 0 || p.maxTxsPerBlock < limit) {
		limit = p.maxTxsPerBlock
	}
	if limit <= 0 {
		limit = 2500
	}

	floor := 256
	switch {
	case pendingCount >= limit*4:
		limit = maxInt(limit/4, floor)
	case pendingCount >= limit*2:
		limit = maxInt(limit/2, floor)
	case pendingCount >= (limit*3)/2:
		limit = maxInt((limit*3)/4, floor)
	}
	return limit
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var (
	blockCache        = make(map[string]*pb.Block)
	blockCacheHeights = make(map[string]uint64)
	blockCacheMu      sync.RWMutex
)

func (p *RealBlockProposer) cacheBlock(blockID string, block *pb.Block) {
	blockCacheMu.Lock()
	defer blockCacheMu.Unlock()
	blockCache[blockID] = block
	blockCacheHeights[blockID] = block.Header.Height
}

func GetCachedBlock(blockID string) (*pb.Block, bool) {
	blockCacheMu.RLock()
	defer blockCacheMu.RUnlock()
	block, exists := blockCache[blockID]
	return block, exists
}

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

func RemoveCachedBlock(blockID string) {
	blockCacheMu.Lock()
	defer blockCacheMu.Unlock()
	delete(blockCache, blockID)
	delete(blockCacheHeights, blockID)
}

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

func GetBlockCacheSize() int {
	blockCacheMu.RLock()
	defer blockCacheMu.RUnlock()
	return len(blockCache)
}
