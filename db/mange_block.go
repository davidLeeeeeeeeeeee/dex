package db

import (
	"dex/logs"
	"fmt"
	"runtime/debug"
	"strconv"
)

const queueSize = 10

// 缓存的区块切片，最多存 10 个
var cachedBlocks []*Block

// 将区块存入DB，同时将区块存入内存切片（缓存）
func SaveBlock(mgr *Manager, block *Block) error {
	logs.Debug("Saving new block_%d", block.Height)
	// 1. 保存区块到 DB - 使用高度作为主键
	key := fmt.Sprintf("block_%d", block.Height)
	data, err := ProtoMarshal(block)
	if err != nil {
		return err
	}
	mgr.EnqueueSet(key, string(data))

	// 2. 额外保存一个ID到高度的映射，方便通过ID查询
	idKey := fmt.Sprintf("blockid_%s", block.BlockHash)
	mgr.EnqueueSet(idKey, strconv.FormatUint(block.Height, 10))

	// 3. 更新最新区块高度
	mgr.EnqueueSet("latest_block_height", strconv.FormatUint(block.Height, 10))

	// 4. 更新内存缓存切片
	//    如果长度 >= 10，就移除最早的一个
	if len(cachedBlocks) >= queueSize {
		cachedBlocks = cachedBlocks[1:]
	}
	//    将最新的 block 加到末尾
	cachedBlocks = append(cachedBlocks, block)

	return nil
}

// GetBlock 根据高度获取对应区块，先看内存缓存，再看 DB
func GetBlock(mgr *Manager, height uint64) (*Block, error) {
	// 1. 先从内存缓存中查
	for _, b := range cachedBlocks {
		if b.Height == height {
			return b, nil
		}
	}

	// 2. 内存里没找到，则再去数据库查
	key := fmt.Sprintf("block_%d", height)
	val, err := mgr.Read(key)
	if err != nil {
		logs.Warn("[GetBlock err]key: %s, err: %v stack: %s\n", key, err, debug.Stack())
		return nil, err
	}

	block := &Block{}
	if err := ProtoUnmarshal([]byte(val), block); err != nil {
		return nil, err
	}

	// 3. 将从 DB 中读到的区块也放到缓存里
	//    同样保持最多 10 个
	if len(cachedBlocks) >= queueSize {
		cachedBlocks = cachedBlocks[1:]
	}
	cachedBlocks = append(cachedBlocks, block)

	return block, nil
}

// GetBlockByID 根据区块ID（BlockHash）获取区块
func GetBlockByID(mgr *Manager, blockID string) (*Block, error) {
	// 1. 先从内存缓存中查找
	for _, b := range cachedBlocks {
		if b.BlockHash == blockID {
			return b, nil
		}
	}

	// 2. 从数据库查找ID到高度的映射
	idKey := fmt.Sprintf("blockid_%s", blockID)
	heightStr, err := mgr.Read(idKey)
	if err != nil {
		// 没有找到映射，可能是旧数据，尝试遍历查找
		return getBlockByIDFallback(mgr, blockID)
	}

	// 3. 解析高度
	height, err := strconv.ParseUint(heightStr, 10, 64)
	if err != nil {
		logs.Error("[GetBlockByID] Failed to parse height for block %s: %v", blockID, err)
		return nil, err
	}

	// 4. 通过高度获取区块
	return GetBlock(mgr, height)
}

// getBlockByIDFallback 当没有ID映射时的降级方案（遍历查找）
func getBlockByIDFallback(mgr *Manager, blockID string) (*Block, error) {
	// 获取最新高度
	latestHeight, err := GetLatestBlockHeight(mgr)
	if err != nil {
		return nil, fmt.Errorf("failed to get latest height: %v", err)
	}

	// 从最新高度向前遍历查找（通常最近的区块被查询的概率更高）
	for h := latestHeight; h > 0; h-- {
		block, err := GetBlock(mgr, h)
		if err != nil {
			continue // 跳过不存在的高度
		}
		if block.BlockHash == blockID {
			// 找到了，顺便建立映射关系以加速后续查询
			idKey := fmt.Sprintf("blockid_%s", blockID)
			mgr.EnqueueSet(idKey, strconv.FormatUint(h, 10))
			return block, nil
		}
	}

	// 检查创世区块
	genesis, err := GetBlock(mgr, 0)
	if err == nil && genesis.BlockHash == blockID {
		return genesis, nil
	}

	return nil, fmt.Errorf("block with ID %s not found", blockID)
}

// GetLatestBlockHeight 直接从 "latest_block_height" 键中读取最新的区块高度
func GetLatestBlockHeight(mgr *Manager) (uint64, error) {
	latestKey := "latest_block_height"
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

// GetBlocksByRange 获取指定高度范围内的所有区块
func GetBlocksByRange(mgr *Manager, fromHeight, toHeight uint64) ([]*Block, error) {
	if fromHeight > toHeight {
		return nil, fmt.Errorf("invalid range: from %d to %d", fromHeight, toHeight)
	}

	blocks := make([]*Block, 0, toHeight-fromHeight+1)
	for h := fromHeight; h <= toHeight; h++ {
		block, err := GetBlock(mgr, h)
		if err != nil {
			// 跳过不存在的高度
			logs.Debug("[GetBlocksByRange] Skip height %d: %v", h, err)
			continue
		}
		blocks = append(blocks, block)
	}

	return blocks, nil
}

// BlockExists 检查指定ID的区块是否存在
func BlockExists(mgr *Manager, blockID string) bool {
	// 1. 先检查缓存
	for _, b := range cachedBlocks {
		if b.BlockHash == blockID {
			return true
		}
	}

	// 2. 检查数据库中的ID映射
	idKey := fmt.Sprintf("blockid_%s", blockID)
	_, err := mgr.Read(idKey)
	return err == nil
}

// UpdateBlockCache 手动更新缓存（用于优化热点数据）
func UpdateBlockCache(block *Block) {
	// 检查是否已在缓存中
	for i, b := range cachedBlocks {
		if b.Height == block.Height {
			cachedBlocks[i] = block
			return
		}
	}

	// 不在缓存中，添加到缓存
	if len(cachedBlocks) >= queueSize {
		cachedBlocks = cachedBlocks[1:]
	}
	cachedBlocks = append(cachedBlocks, block)
}
