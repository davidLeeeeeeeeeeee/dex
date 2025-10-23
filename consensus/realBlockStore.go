package consensus

import (
	"dex/db"
	"dex/interfaces"
	"dex/logs"
	"dex/txpool"
	"dex/types"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// RealBlockStore 使用数据库的真实区块存储实现
type RealBlockStore struct {
	mu        sync.RWMutex
	dbManager *db.Manager
	pool      *txpool.TxPool
	adapter   *ConsensusAdapter
	// 内存缓存
	blockCache         map[string]*types.Block
	heightIndex        map[uint64][]*types.Block
	finalizedBlocks    map[uint64]*types.Block
	lastAccepted       *types.Block
	lastAcceptedHeight uint64
	maxHeight          uint64

	// 快照管理
	snapshots       map[uint64]*types.Snapshot
	snapshotHeights []uint64
	maxSnapshots    int
}

// 创建真实的区块存储
func NewRealBlockStore(dbManager *db.Manager, maxSnapshots int, pool *txpool.TxPool) interfaces.BlockStore {
	store := &RealBlockStore{
		dbManager:       dbManager,
		pool:            pool,
		blockCache:      make(map[string]*types.Block),
		heightIndex:     make(map[uint64][]*types.Block),
		finalizedBlocks: make(map[uint64]*types.Block),
		snapshots:       make(map[uint64]*types.Snapshot),
		snapshotHeights: make([]uint64, 0),
		maxSnapshots:    maxSnapshots,
		maxHeight:       0,
		adapter:         NewConsensusAdapter(dbManager),
	}

	// 初始化创世区块
	genesis := &types.Block{
		ID:       "genesis",
		Height:   0,
		ParentID: "",
		Data:     "Genesis Block",
		Proposer: "-1",
	}

	store.blockCache[genesis.ID] = genesis
	store.heightIndex[0] = []*types.Block{genesis}
	store.lastAccepted = genesis
	store.lastAcceptedHeight = 0
	store.finalizedBlocks[0] = genesis

	// 将创世区块保存到数据库
	store.saveBlockToDB(genesis)

	// 从数据库加载已有区块
	store.loadFromDB()

	return store
}

// Add 添加新区块
func (s *RealBlockStore) Add(block *types.Block) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.blockCache[block.ID]; exists {
		return false, nil
	}

	if err := s.validateBlock(block); err != nil {
		return false, err
	}

	// 添加到内存缓存
	s.blockCache[block.ID] = block
	s.heightIndex[block.Height] = append(s.heightIndex[block.Height], block)

	if block.Height > s.maxHeight {
		s.maxHeight = block.Height
		// 更新数据库中的最新高度
		s.dbManager.EnqueueSet(db.KeyLatestHeight(), strconv.FormatUint(block.Height, 10))
	}

	// 异步保存到数据库（非最终化的区块暂时只在内存中）
	go s.saveBlockToDB(block)

	logs.Debug("[RealBlockStore] Added block %s at height %d", block.ID, block.Height)

	return true, nil
}

// Get 获取区块
func (s *RealBlockStore) Get(id string) (*types.Block, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 先从内存缓存查找
	if block, exists := s.blockCache[id]; exists {
		return block, true
	}

	// 从数据库通过ID查找
	dbBlock, err := s.dbManager.GetBlockByID(id)
	if err != nil || dbBlock == nil {
		logs.Debug("[RealBlockStore] Block %s not found: %v", id, err)
		return nil, false
	}

	// 转换为types.Block
	block := s.convertDBBlockToTypes(dbBlock)
	if block != nil {
		// 加入缓存以加速后续访问
		s.blockCache[id] = block
		return block, true
	}

	return nil, false
}

// GetByHeight 获取指定高度的所有区块
func (s *RealBlockStore) GetByHeight(height uint64) []*types.Block {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 先从内存查找
	if blocks, exists := s.heightIndex[height]; exists {
		result := make([]*types.Block, len(blocks))
		copy(result, blocks)
		return result
	}
	// NEW: 如果请求的高度还没被任何区块覆盖，就直接返回空，避免打 DB
	if height > s.maxHeight {
		return []*types.Block{}
	}
	// 从数据库查找
	dbBlock, err := s.dbManager.GetBlock(height)
	if err != nil || dbBlock == nil {
		return []*types.Block{}
	}

	// 转换为types.Block
	block := s.convertDBBlockToTypes(dbBlock)
	if block != nil {
		s.blockCache[block.ID] = block
		s.heightIndex[height] = []*types.Block{block}
		return []*types.Block{block}
	}

	return []*types.Block{}
}

// GetLastAccepted 获取最后接受的区块
func (s *RealBlockStore) GetLastAccepted() (string, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.lastAccepted != nil {
		return s.lastAccepted.ID, s.lastAcceptedHeight
	}

	// 从数据库获取最新高度
	height, err := s.dbManager.GetLatestBlockHeight()
	if err == nil && height > 0 {
		if block, err := s.dbManager.GetBlock(height); err == nil && block != nil {
			return block.BlockHash, height
		}
	}

	return "genesis", 0
}

// GetFinalizedAtHeight 获取指定高度的最终化区块
func (s *RealBlockStore) GetFinalizedAtHeight(height uint64) (*types.Block, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 从内存查找
	if block, exists := s.finalizedBlocks[height]; exists {
		return block, true
	}

	// 从数据库查找
	dbBlock, err := s.dbManager.GetBlock(height)
	if err != nil || dbBlock == nil {
		return nil, false
	}

	block := s.convertDBBlockToTypes(dbBlock)
	if block != nil {
		s.finalizedBlocks[height] = block
		return block, true
	}

	return nil, false
}

// 获取指定高度范围的区块
func (s *RealBlockStore) GetBlocksFromHeight(from, to uint64) []*types.Block {
	s.mu.RLock()
	defer s.mu.RUnlock()

	blocks := make([]*types.Block, 0)
	for h := from; h <= to && h <= s.maxHeight; h++ {
		if heightBlocks, exists := s.heightIndex[h]; exists {
			blocks = append(blocks, heightBlocks...)
		} else {
			// 从数据库加载
			if dbBlock, err := s.dbManager.GetBlock(h); err == nil && dbBlock != nil {
				block := s.convertDBBlockToTypes(dbBlock)
				if block != nil {
					blocks = append(blocks, block)
				}
			}
		}
	}
	return blocks
}

// GetCurrentHeight 获取当前最大高度
func (s *RealBlockStore) GetCurrentHeight() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 优先返回内存中的最大高度
	if s.maxHeight > 0 {
		return s.maxHeight
	}

	// 从数据库获取
	height, _ := s.dbManager.GetLatestBlockHeight()
	return height
}

// 设置区块为最终化状态（内部使用）
func (s *RealBlockStore) SetFinalized(height uint64, blockID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	block, exists := s.blockCache[blockID]
	if !exists {
		// 从数据库通过ID加载
		if dbBlock, err := s.dbManager.GetBlockByID(blockID); err == nil && dbBlock != nil {
			block = s.convertDBBlockToTypes(dbBlock)
			s.blockCache[blockID] = block
		}
	}

	if block != nil {
		s.finalizedBlocks[height] = block
		s.lastAccepted = block
		s.lastAcceptedHeight = height

		// 清理同高度其他区块
		newBlocks := make([]*types.Block, 0, 1)
		for _, b := range s.heightIndex[height] {
			if b.ID == blockID {
				newBlocks = append(newBlocks, b)
			} else {
				delete(s.blockCache, b.ID)
			}
		}
		s.heightIndex[height] = newBlocks

		// 最终化区块，包含其交易
		s.finalizeBlockWithTxs(block)

		logs.Info("[RealBlockStore] Finalized block %s at height %d", blockID, height)
	}
}

// CreateSnapshot 创建快照
func (s *RealBlockStore) CreateSnapshot(height uint64) (*types.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if height > s.lastAcceptedHeight {
		return nil, fmt.Errorf("cannot create snapshot beyond last accepted height")
	}

	snapshot := &types.Snapshot{
		Height:             height,
		Timestamp:          time.Now(),
		FinalizedBlocks:    make(map[uint64]*types.Block),
		LastAcceptedID:     s.lastAccepted.ID,
		LastAcceptedHeight: s.lastAcceptedHeight,
		BlockHashes:        make(map[string]bool),
	}

	// 复制所有已最终化的区块
	for h := uint64(0); h <= height; h++ {
		if block, exists := s.finalizedBlocks[h]; exists {
			snapshot.FinalizedBlocks[h] = block
			snapshot.BlockHashes[block.ID] = true
		}
	}

	// 存储快照
	s.snapshots[height] = snapshot
	s.snapshotHeights = append(s.snapshotHeights, height)

	// 限制快照数量
	if len(s.snapshotHeights) > s.maxSnapshots {
		oldestHeight := s.snapshotHeights[0]
		delete(s.snapshots, oldestHeight)
		s.snapshotHeights = s.snapshotHeights[1:]
	}

	// 持久化快照到数据库
	go s.saveSnapshotToDB(snapshot)

	logs.Info("[RealBlockStore] Created snapshot at height %d", height)

	return snapshot, nil
}

// LoadSnapshot 加载快照
func (s *RealBlockStore) LoadSnapshot(snapshot *types.Snapshot) error {
	if snapshot == nil {
		return fmt.Errorf("nil snapshot")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 清空现有数据
	s.blockCache = make(map[string]*types.Block)
	s.heightIndex = make(map[uint64][]*types.Block)
	s.finalizedBlocks = make(map[uint64]*types.Block)

	// 加载快照数据
	for height, block := range snapshot.FinalizedBlocks {
		s.blockCache[block.ID] = block
		s.heightIndex[height] = []*types.Block{block}
		s.finalizedBlocks[height] = block

		if height > s.maxHeight {
			s.maxHeight = height
		}
	}

	// 恢复最后接受的区块
	if lastBlock, exists := snapshot.FinalizedBlocks[snapshot.LastAcceptedHeight]; exists {
		s.lastAccepted = lastBlock
		s.lastAcceptedHeight = snapshot.LastAcceptedHeight
	}

	logs.Info("[RealBlockStore] Loaded snapshot at height %d with %d blocks",
		snapshot.Height, len(snapshot.FinalizedBlocks))

	return nil
}

// GetLatestSnapshot 获取最新快照
func (s *RealBlockStore) GetLatestSnapshot() (*types.Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.snapshotHeights) == 0 {
		return nil, false
	}

	latestHeight := s.snapshotHeights[len(s.snapshotHeights)-1]
	snapshot, exists := s.snapshots[latestHeight]
	return snapshot, exists
}

// GetSnapshotAtHeight 获取指定高度或之前的最近快照
func (s *RealBlockStore) GetSnapshotAtHeight(height uint64) (*types.Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var bestHeight uint64
	found := false

	for i := len(s.snapshotHeights) - 1; i >= 0; i-- {
		if s.snapshotHeights[i] <= height {
			bestHeight = s.snapshotHeights[i]
			found = true
			break
		}
	}

	if !found {
		return nil, false
	}

	snapshot, exists := s.snapshots[bestHeight]
	return snapshot, exists
}

// 内部辅助方法

func (s *RealBlockStore) validateBlock(block *types.Block) error {
	if block == nil || block.ID == "" {
		return fmt.Errorf("invalid block")
	}
	if block.Height == 0 && block.ID != "genesis" {
		return fmt.Errorf("invalid genesis block")
	}
	if block.Height > 0 && block.ParentID == "" {
		return fmt.Errorf("non-genesis block must have parent")
	}
	return nil
}

func (s *RealBlockStore) saveBlockToDB(block *types.Block) {
	// 获取缓存的完整区块数据
	if cachedBlock, exists := GetCachedBlock(block.ID); exists {
		// 保存完整的区块数据
		if err := s.dbManager.SaveBlock(cachedBlock); err != nil {
			logs.Error("[RealBlockStore] Failed to save block to DB: %v", err)
		} else {
			logs.Debug("[RealBlockStore] Saved block %s to DB", block.ID)
			s.dbManager.ForceFlush() // 立刻刷盘，保证最终化的块可被 DB 即时读取
		}
	} else {
		// 创建简单的数据库区块
		dbBlock := &db.Block{
			Height:        block.Height,
			BlockHash:     block.ID,
			PrevBlockHash: block.ParentID,
			Miner:         fmt.Sprintf(db.KeyNode()+"%d", block.Proposer),
		}
		if err := s.dbManager.SaveBlock(dbBlock); err != nil {
			logs.Error("[RealBlockStore] Failed to save simple block to DB: %v", err)
		}
	}
}

func (s *RealBlockStore) loadFromDB() {
	// 加载最新高度
	height, err := s.dbManager.GetLatestBlockHeight()
	if err == nil {
		s.maxHeight = height

		// 加载最近的一些区块到缓存
		startHeight := uint64(0)
		if height > 100 {
			startHeight = height - 100
		}

		for h := startHeight; h <= height; h++ {
			if dbBlock, err := s.dbManager.GetBlock(h); err == nil && dbBlock != nil {
				block := s.convertDBBlockToTypes(dbBlock)
				if block != nil {
					s.blockCache[block.ID] = block
					s.heightIndex[h] = []*types.Block{block}
					if h == height {
						s.lastAccepted = block
						s.lastAcceptedHeight = h
					}
				}
			}
		}

		logs.Info("[RealBlockStore] Loaded blocks from DB, latest height: %d", height)
	}
}

func (s *RealBlockStore) convertDBBlockToTypes(dbBlock *db.Block) *types.Block {
	if s.adapter == nil {
		s.adapter = NewConsensusAdapter(s.dbManager)
	}
	block, err := s.adapter.DBBlockToConsensus(dbBlock)
	if err != nil {
		logs.Error("[RealBlockStore] Failed to convert block: %v", err)
		return nil
	}
	return block
}

func (s *RealBlockStore) finalizeBlockWithTxs(block *types.Block) {
	// 获取该区块包含的交易
	if cachedBlock, exists := GetCachedBlock(block.ID); exists {
		// 更新交易状态
		for _, tx := range cachedBlock.Body {
			base := tx.GetBase()
			if base != nil {
				base.Status = db.Status_SUCCEED
				base.ExecutedHeight = block.Height

				// 更新交易池
				s.pool.RemoveAnyTx(base.TxId)

				// 保存到数据库
				if err := s.dbManager.SaveAnyTx(tx); err != nil {
					logs.Error("[RealBlockStore] Failed to save finalized tx %s: %v", base.TxId, err)
				}
			}
		}

		// 保存最终化的区块
		if err := s.dbManager.SaveBlock(cachedBlock); err != nil {
			logs.Error("[RealBlockStore] Failed to save finalized block: %v", err)
		}

		logs.Info("[RealBlockStore] Finalized block %s with %d txs", block.ID, len(cachedBlock.Body))
	}
}

func (s *RealBlockStore) saveSnapshotToDB(snapshot *types.Snapshot) {
	// 这里可以实现快照的持久化逻辑
	// 例如序列化后保存到数据库的特定键下
	key := fmt.Sprintf("snapshot_%d", snapshot.Height)
	data, _ := json.Marshal(snapshot)
	s.dbManager.EnqueueSet(key, string(data))
	logs.Debug("[RealBlockStore] Snapshot saved at height %d", snapshot.Height)
}
