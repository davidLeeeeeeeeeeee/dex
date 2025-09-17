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

// SaveBlock 将区块存入DB，同时将区块存入内存切片（缓存）
func SaveBlock(mgr *Manager, block *Block) error {
	logs.Info("Saving new block_%d", block.Height)
	// 1. 保存区块到 DB
	key := fmt.Sprintf("block_%d", block.Height)
	data, err := ProtoMarshal(block)
	if err != nil {
		return err
	}
	mgr.EnqueueSet(key, string(data))

	// 2. 更新内存缓存切片
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
