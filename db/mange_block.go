package db

import (
	"dex/config"
	"dex/pb"
	"encoding/json"
	"fmt"
	"strconv"
)

// SaveBlock saves a block to the database and cache
//
// ⚠️ INTERNAL API - DO NOT CALL DIRECTLY FROM OUTSIDE DB PACKAGE
// This method is used internally by db package for legacy compatibility.
// New code should use VM's unified write path (applyResult) instead.
//
// Deprecated: Use VM's WriteOp mechanism for all state changes.
func (mgr *Manager) SaveBlock(block *pb.Block) error {
	mgr.Logger.Debug("Saving new block_%d with ID %s", block.Header.Height, block.BlockHash)

	// 1. 使用 BlockID 作为主键存储完整区块
	blockKey := KeyBlockData(block.BlockHash)
	data, err := ProtoMarshal(block)
	if err != nil {
		return err
	}
	mgr.EnqueueSet(blockKey, string(data))

	// 2. 维护高度到区块ID列表的映射
	heightKey := KeyHeightBlocks(block.Header.Height)
	existingIDs, _ := mgr.Read(heightKey)

	var blockIDs []string
	if existingIDs != "" {
		// 解析现有的ID列表
		json.Unmarshal([]byte(existingIDs), &blockIDs)
	}

	// 添加新的blockID（去重）
	found := false
	for _, id := range blockIDs {
		if id == block.BlockHash {
			found = true
			break
		}
	}
	if !found {
		blockIDs = append(blockIDs, block.BlockHash)
	}

	idsData, _ := json.Marshal(blockIDs)
	mgr.EnqueueSet(heightKey, string(idsData))

	// 3. ID到高度映射
	idKey := KeyBlockIDToHeight(block.BlockHash)
	mgr.EnqueueSet(idKey, strconv.FormatUint(block.Header.Height, 10))

	// 4. 更新最新区块高度
	mgr.EnqueueSet(KeyLatestHeight(), strconv.FormatUint(block.Header.Height, 10))

	// 5. 更新内存缓存
	// 5. 更新内存缓存
	mgr.cachedBlocksMu.Lock()
	cacheFound := false
	for i, v := range mgr.cachedBlocks {
		if v != nil && v.BlockHash == block.BlockHash {
			mgr.cachedBlocks[i] = block
			cacheFound = true
			break
		}
	}
	if !cacheFound {
		cfg := mgr.cfg
		if cfg == nil {
			cfg = config.DefaultConfig()
		}
		if len(mgr.cachedBlocks) >= cfg.Database.BlockCacheSize {
			mgr.cachedBlocks = mgr.cachedBlocks[1:]
		}
		mgr.cachedBlocks = append(mgr.cachedBlocks, block)
	}
	mgr.cachedBlocksMu.Unlock()

	return nil
}

// GetBlock 根据高度获取对应区块，先看内存缓存，再看 DB
func (mgr *Manager) GetBlock(height uint64) (*pb.Block, error) {
	// 获取该高度的第一个区块（用于兼容旧代码）
	blocks, err := mgr.GetBlocksByHeight(height)
	if err != nil || len(blocks) == 0 {
		return nil, fmt.Errorf("no blocks at height %d", height)
	}
	return blocks[0], nil
}

func (mgr *Manager) GetBlocksByHeight(height uint64) ([]*pb.Block, error) {
	// 1. 从高度映射获取所有区块ID
	heightKey := KeyHeightBlocks(height)
	idsStr, err := mgr.Read(heightKey)
	if err != nil {
		return nil, err
	}

	var blockIDs []string
	if err := json.Unmarshal([]byte(idsStr), &blockIDs); err != nil {
		return nil, err
	}

	// 2. 获取每个区块
	var blocks []*pb.Block
	for _, id := range blockIDs {
		block, err := mgr.GetBlockByID(id)
		if err == nil && block != nil {
			blocks = append(blocks, block)
		}
	}

	return blocks, nil
}

// GetBlockByID 根据区块ID（BlockHash）获取区块
func (mgr *Manager) GetBlockByID(blockID string) (*pb.Block, error) {
	// 1. 先检查缓存
	mgr.cachedBlocksMu.RLock()
	for _, b := range mgr.cachedBlocks {
		if b != nil && b.BlockHash == blockID {
			mgr.cachedBlocksMu.RUnlock()
			return b, nil
		}
	}
	mgr.cachedBlocksMu.RUnlock()

	// 2. 直接从 blockdata_<blockID> 读取
	blockKey := KeyBlockData(blockID)
	val, err := mgr.Read(blockKey)
	if err != nil {
		return nil, fmt.Errorf("block %s not found", blockID)
	}

	block := &pb.Block{}

	if err := ProtoUnmarshal([]byte(val), block); err != nil {
		return nil, err
	}

	return block, nil
}

// 当没有ID映射时的降级方案（遍历查找）
func (mgr *Manager) getBlockByIDFallback(blockID string) (*pb.Block, error) {
	// 获取最新高度
	latestHeight, err := mgr.GetLatestBlockHeight()
	if err != nil {
		return nil, fmt.Errorf("failed to get latest height: %v", err)
	}

	// 从最新高度向前遍历查找（通常最近的区块被查询的概率更高）
	for h := latestHeight; h > 0; h-- {
		block, err := mgr.GetBlock(h)
		if err != nil {
			continue // 跳过不存在的高度
		}
		if block.BlockHash == blockID {
			// 找到了，顺便建立映射关系以加速后续查询
			idKey := KeyBlockIDToHeight(blockID)
			mgr.EnqueueSet(idKey, strconv.FormatUint(h, 10))
			return block, nil
		}
	}

	// 检查创世区块
	genesis, err := mgr.GetBlock(0)
	if err == nil && genesis.BlockHash == blockID {
		return genesis, nil
	}

	return nil, fmt.Errorf("block with ID %s not found", blockID)
}

// GetLatestBlockHeight 直接从 "latest_block_height" 键中读取最新的区块高度
func (mgr *Manager) GetLatestBlockHeight() (uint64, error) {
	latestKey := KeyLatestHeight()
	val, err := mgr.Read(latestKey)
	if err != nil {
		return 0, err
	}
	height, err := strconv.ParseUint(val, 10, 64)
	if err != nil {
		return 0, err
	}
	return height, nil
}

// GetCurrentHeight 返回当前最大高度；如果还没有任何区块，则返回 0
func (mgr *Manager) GetCurrentHeight() uint64 {
	height, err := mgr.GetLatestBlockHeight()
	if err != nil {
		return 0
	}
	return height
}

// 获取指定高度范围内的所有区块
func (mgr *Manager) GetBlocksByRange(fromHeight, toHeight uint64) ([]*pb.Block, error) {
	if fromHeight > toHeight {
		return nil, fmt.Errorf("invalid range: from %d to %d", fromHeight, toHeight)
	}

	blocks := make([]*pb.Block, 0, toHeight-fromHeight+1)
	for h := fromHeight; h <= toHeight; h++ {
		block, err := mgr.GetBlock(h)
		if err != nil {
			// 跳过不存在的高度
			mgr.Logger.Debug("[GetBlocksByRange] Skip height %d: %v", h, err)
			continue
		}
		blocks = append(blocks, block)
	}

	return blocks, nil
}

// BlockExists 检查指定ID的区块是否存在
func (mgr *Manager) BlockExists(blockID string) bool {
	// 1. 先检查缓存
	mgr.cachedBlocksMu.RLock()
	for _, b := range mgr.cachedBlocks {
		if b != nil && b.BlockHash == blockID {
			mgr.cachedBlocksMu.RUnlock()
			return true
		}
	}
	mgr.cachedBlocksMu.RUnlock()

	// 2. 检查数据库中的ID映射
	idKey := KeyBlockIDToHeight(blockID)
	_, err := mgr.Read(idKey)
	return err == nil
}
