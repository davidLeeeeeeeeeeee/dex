package consensus

import (
	"dex/interfaces"
	"dex/types"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ============================================
// 区块存储接口（增强版，支持快照）
// ============================================

type MemoryBlockStore struct {
	mu                 sync.RWMutex
	blocks             map[string]*types.Block
	heightIndex        map[uint64][]*types.Block
	lastAccepted       *types.Block
	lastAcceptedHeight uint64
	finalizedBlocks    map[uint64]*types.Block
	maxHeight          uint64

	// 快照相关
	snapshots       map[uint64]*types.Snapshot
	snapshotHeights []uint64 // 有序的快照高度列表
	maxSnapshots    int
}

func NewMemoryBlockStore() interfaces.BlockStore {
	return NewMemoryBlockStoreWithConfig(10)
}

func NewMemoryBlockStoreWithConfig(maxSnapshots int) interfaces.BlockStore {
	store := &MemoryBlockStore{
		blocks:          make(map[string]*types.Block),
		heightIndex:     make(map[uint64][]*types.Block),
		finalizedBlocks: make(map[uint64]*types.Block),
		maxHeight:       0,
		snapshots:       make(map[uint64]*types.Snapshot),
		snapshotHeights: make([]uint64, 0),
		maxSnapshots:    maxSnapshots,
	}

	// 创世区块
	genesis := &types.Block{
		ID:       "genesis",
		Height:   0,
		ParentID: "",
		Proposer: -1,
	}
	store.blocks[genesis.ID] = genesis
	store.heightIndex[0] = []*types.Block{genesis}
	store.lastAccepted = genesis
	store.lastAcceptedHeight = 0
	store.finalizedBlocks[0] = genesis
	store.maxHeight = 0

	return store
}

func (s *MemoryBlockStore) Add(block *types.Block) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.blocks[block.ID]; exists {
		return false, nil
	}

	if err := s.validateBlock(block); err != nil {
		return false, err
	}

	s.blocks[block.ID] = block
	s.heightIndex[block.Height] = append(s.heightIndex[block.Height], block)

	if block.Height > s.maxHeight {
		s.maxHeight = block.Height
	}

	return true, nil
}

func (s *MemoryBlockStore) validateBlock(block *types.Block) error {
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

func (s *MemoryBlockStore) Get(id string) (*types.Block, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	block, exists := s.blocks[id]
	return block, exists
}

func (s *MemoryBlockStore) GetByHeight(height uint64) []*types.Block {
	s.mu.RLock()
	defer s.mu.RUnlock()
	blocks := s.heightIndex[height]
	result := make([]*types.Block, len(blocks))
	copy(result, blocks)
	return result
}

func (s *MemoryBlockStore) GetLastAccepted() (string, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastAccepted.ID, s.lastAcceptedHeight
}

func (s *MemoryBlockStore) GetFinalizedAtHeight(height uint64) (*types.Block, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	block, exists := s.finalizedBlocks[height]
	return block, exists
}

func (s *MemoryBlockStore) GetBlocksFromHeight(from, to uint64) []*types.Block {
	s.mu.RLock()
	defer s.mu.RUnlock()

	blocks := make([]*types.Block, 0)
	for h := from; h <= to && h <= s.maxHeight; h++ {
		if heightBlocks, exists := s.heightIndex[h]; exists {
			blocks = append(blocks, heightBlocks...)
		}
	}
	return blocks
}

func (s *MemoryBlockStore) GetCurrentHeight() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.maxHeight
}

func (s *MemoryBlockStore) SetFinalized(height uint64, blockID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if block, exists := s.blocks[blockID]; exists {
		s.finalizedBlocks[height] = block
		s.lastAccepted = block
		s.lastAcceptedHeight = height

		// 清理同高度其他区块
		newBlocks := make([]*types.Block, 0, 1)
		for _, b := range s.heightIndex[height] {
			if b.ID == blockID {
				newBlocks = append(newBlocks, b)
			} else {
				delete(s.blocks, b.ID)
			}
		}
		s.heightIndex[height] = newBlocks
	}
}

// 创建快照
func (s *MemoryBlockStore) CreateSnapshot(height uint64) (*types.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 只在已最终化的高度创建快照
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

	// 复制所有已最终化的区块（到指定高度）
	for h := uint64(0); h <= height; h++ {
		if block, exists := s.finalizedBlocks[h]; exists {
			snapshot.FinalizedBlocks[h] = block
			snapshot.BlockHashes[block.ID] = true
		}
	}

	// 存储快照
	s.snapshots[height] = snapshot
	s.snapshotHeights = append(s.snapshotHeights, height)
	sort.Slice(s.snapshotHeights, func(i, j int) bool {
		return s.snapshotHeights[i] < s.snapshotHeights[j]
	})

	// 限制快照数量
	if len(s.snapshotHeights) > s.maxSnapshots {
		oldestHeight := s.snapshotHeights[0]
		delete(s.snapshots, oldestHeight)
		s.snapshotHeights = s.snapshotHeights[1:]
	}

	return snapshot, nil
}

// 加载快照（新增）
func (s *MemoryBlockStore) LoadSnapshot(snapshot *types.Snapshot) error {
	if snapshot == nil {
		return fmt.Errorf("nil snapshot")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 清空现有数据
	s.blocks = make(map[string]*types.Block)
	s.heightIndex = make(map[uint64][]*types.Block)
	s.finalizedBlocks = make(map[uint64]*types.Block)

	// 加载快照数据
	for height, block := range snapshot.FinalizedBlocks {
		s.blocks[block.ID] = block
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

	Logf("[Store] Loaded snapshot at height %d with %d blocks\n",
		snapshot.Height, len(snapshot.FinalizedBlocks))

	return nil
}

// 获取最新快照（新增）
func (s *MemoryBlockStore) GetLatestSnapshot() (*types.Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.snapshotHeights) == 0 {
		return nil, false
	}

	latestHeight := s.snapshotHeights[len(s.snapshotHeights)-1]
	snapshot, exists := s.snapshots[latestHeight]
	return snapshot, exists
}

// 获取指定高度或之前的最近快照（新增）
func (s *MemoryBlockStore) GetSnapshotAtHeight(height uint64) (*types.Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 找到小于等于指定高度的最大快照高度
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
