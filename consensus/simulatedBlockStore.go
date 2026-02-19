package consensus

import (
	"dex/interfaces"
	"dex/types"
	"fmt"
	"sync"
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
}

func NewMemoryBlockStore() interfaces.BlockStore {
	return NewMemoryBlockStoreWithConfig()
}

func NewMemoryBlockStoreWithConfig() interfaces.BlockStore {
	store := &MemoryBlockStore{
		blocks:          make(map[string]*types.Block),
		heightIndex:     make(map[uint64][]*types.Block),
		finalizedBlocks: make(map[uint64]*types.Block),
		maxHeight:       0,
	}

	// 创世区块
	genesis := &types.Block{
		ID: "genesis",
		Header: types.BlockHeader{
			Height:   0,
			ParentID: "",
			Proposer: "-1",
		},
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
	s.heightIndex[block.Header.Height] = append(s.heightIndex[block.Header.Height], block)

	if block.Header.Height > s.maxHeight {
		s.maxHeight = block.Header.Height
	}

	return true, nil
}

func (s *MemoryBlockStore) validateBlock(block *types.Block) error {
	if block == nil || block.ID == "" {
		return fmt.Errorf("invalid block")
	}
	if block.Header.Height == 0 && block.ID != "genesis" {
		return fmt.Errorf("invalid genesis block")
	}
	if block.Header.Height > 0 && block.Header.ParentID == "" {
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

func (s *MemoryBlockStore) SetFinalized(height uint64, blockID string) error {
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
		return nil
	}
	return fmt.Errorf("block %s not found", blockID)
}

// GetPendingBlocksCount 获取候选区块数量（未最终化）
func (s *MemoryBlockStore) GetPendingBlocksCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	for height := s.lastAcceptedHeight + 1; height <= s.maxHeight; height++ {
		if blocks, exists := s.heightIndex[height]; exists {
			count += len(blocks)
		}
	}
	return count
}

// GetPendingBlocks 获取候选区块列表
func (s *MemoryBlockStore) GetPendingBlocks() []*types.Block {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*types.Block, 0)
	for height := s.lastAcceptedHeight + 1; height <= s.maxHeight; height++ {
		if blocks, exists := s.heightIndex[height]; exists {
			result = append(result, blocks...)
		}
	}
	return result
}
