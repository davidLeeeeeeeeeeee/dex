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
	"dex/vm"
	"dex/witness"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
)

var ErrAlreadyFinalized = errors.New("already finalized")
var emptyTxsHash = txpool.ComputeTxsHash(nil)

// RealBlockStore 使用数据库的真实区块存储实现
type RealBlockStore struct {
	mu         sync.RWMutex
	finalizeMu sync.Mutex // 串行化最终化流程，避免同节点并发提交导致 DB 事务冲突
	dbManager  *db.Manager
	pool       *txpool.TxPool
	adapter    *ConsensusAdapter
	vmExecutor *vm.Executor        // VM执行器，用于预执行和提交区块
	events     interfaces.EventBus // 事件总线
	// 内存缓存
	blockCache         map[string]*types.Block
	heightIndex        map[uint64][]*types.Block
	finalizedBlocks    map[uint64]*types.Block
	lastAccepted       *types.Block
	lastAcceptedHeight uint64
	maxHeight          uint64

	nodeID types.NodeID

	// 最终化投票记录（用于调试）
	finalizationChits map[uint64]*types.FinalizationChits

	// VRF 签名集合（共识证据）
	signatureSets map[uint64]*pb.ConsensusSignatureSet

	// 最终化强制刷盘策略（用于降低每块同步刷盘带来的尾延迟）
	forceFlushMu            sync.Mutex
	forceFlushEveryN        int
	forceFlushMaxInterval   time.Duration
	finalizedSinceLastFlush uint64
	lastForceFlushAt        time.Time
}

// 创建真实的区块存储
func NewRealBlockStore(nodeID types.NodeID, dbManager *db.Manager, pool *txpool.TxPool, cfg *config.Config) interfaces.BlockStore {
	// 转换见证者配置
	var witnessCfg *witness.Config
	if cfg != nil {
		witnessCfg = &witness.Config{
			ConsensusThreshold:      cfg.Witness.ConsensusThreshold,
			AbstainThreshold:        cfg.Witness.AbstainThreshold,
			VotingPeriodBlocks:      cfg.Witness.VotingPeriodBlocks,
			ChallengePeriodBlocks:   cfg.Witness.ChallengePeriodBlocks,
			ArbitrationPeriodBlocks: cfg.Witness.ArbitrationPeriodBlocks,
			UnstakeLockBlocks:       cfg.Witness.UnstakeLockBlocks,
			RetryIntervalBlocks:     cfg.Witness.RetryIntervalBlocks,
			MinStakeAmount:          cfg.Witness.MinStakeAmount,
			ChallengeStakeAmount:    cfg.Witness.ChallengeStakeAmount,
			InitialWitnessCount:     cfg.Witness.InitialWitnessCount,
			ExpandMultiplier:        cfg.Witness.ExpandMultiplier,
			WitnessRewardRatio:      cfg.Witness.WitnessRewardRatio,
			SlashRatio:              cfg.Witness.SlashRatio,
			ChallengerReward:        cfg.Witness.ChallengerReward,
		}
	}

	// 初始化见证者服务
	witnessSvc := witness.NewService(witnessCfg)
	_ = witnessSvc.Start()

	// 初始化 VM 执行器
	registry := vm.NewHandlerRegistry()
	if err := vm.RegisterDefaultHandlers(registry, cfg, witnessSvc); err != nil {
		logs.Error("Failed to register VM handlers: %v", err)
		// 继续执行，但VM功能可能不完整
	}
	// 内存优化：将 SpecExecLRU 缓存容量从 1024 减小到 32
	// 每个区块可能有 2000 笔交易，1个 SpecResult 包含 2000 个 Receipt
	// 1024 个缓存会消耗巨量内存且在高并发下产生大量小对象碎片
	cache := vm.NewSpecExecLRU(32)

	vmExecutor := vm.NewExecutorWithWitnessService(dbManager, registry, cache, witnessSvc)

	store := &RealBlockStore{
		dbManager:             dbManager,
		pool:                  pool,
		vmExecutor:            vmExecutor,
		blockCache:            make(map[string]*types.Block),
		heightIndex:           make(map[uint64][]*types.Block),
		finalizedBlocks:       make(map[uint64]*types.Block),
		maxHeight:             0,
		adapter:               NewConsensusAdapter(dbManager),
		nodeID:                nodeID,
		finalizationChits:     make(map[uint64]*types.FinalizationChits),
		signatureSets:         make(map[uint64]*pb.ConsensusSignatureSet),
		forceFlushEveryN:      1,
		forceFlushMaxInterval: 0,
		lastForceFlushAt:      time.Now(),
	}

	if cfg != nil {
		store.forceFlushEveryN = cfg.Database.FinalizationForceFlushEveryN
		store.forceFlushMaxInterval = cfg.Database.FinalizationForceFlushInterval
	}

	logs.Info(
		"[RealBlockStore] Finalization flush policy: everyN=%d, interval=%v",
		store.forceFlushEveryN,
		store.forceFlushMaxInterval,
	)

	// 初始化创世区块
	genesis := &types.Block{
		ID: "genesis",
		Header: types.BlockHeader{
			Height:   0,
			ParentID: "",
			Proposer: "-1",
		},
	}

	store.blockCache[genesis.ID] = genesis
	store.heightIndex[0] = []*types.Block{genesis}
	store.lastAccepted = genesis
	store.lastAcceptedHeight = 0
	store.finalizedBlocks[0] = genesis

	// 从数据库加载已有区块
	store.loadFromDB()

	return store
}

// SetEventBus 设置事件总线（在初始化后调用）
func (s *RealBlockStore) SetEventBus(events interfaces.EventBus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = events
}

// Add 添加新区块
func isCompleteBlockPayload(block *pb.Block) bool {
	if block == nil || block.Header == nil {
		return false
	}
	if len(block.Body) > 0 {
		return true
	}
	if len(block.ShortTxs) > 0 {
		return false
	}
	return block.Header.TxsHash == emptyTxsHash
}

func normalizeReceiptStatus(status string) (pb.Status, bool) {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "SUCCEED", "SUCCESS":
		return pb.Status_SUCCEED, true
	case "FAILED", "FAIL":
		return pb.Status_FAILED, true
	default:
		return pb.Status_PENDING, false
	}
}

// materializeFinalizedBlock builds a block copy whose tx statuses come from VM receipts
// of the same finalized height. Txs not executed at this height remain unchanged (pending).
func (s *RealBlockStore) materializeFinalizedBlock(block *pb.Block) *pb.Block {
	if block == nil {
		return nil
	}
	cloned, ok := proto.Clone(block).(*pb.Block)
	if !ok || cloned == nil || cloned.Header == nil || len(cloned.Body) == 0 {
		return block
	}

	finalizedHeight := cloned.Header.Height
	for _, tx := range cloned.Body {
		if tx == nil {
			continue
		}
		base := tx.GetBase()
		if base == nil {
			continue
		}
		txID := tx.GetTxId()
		if txID == "" {
			continue
		}

		receipt, err := s.dbManager.GetTxReceipt(txID)
		if err != nil || receipt == nil {
			continue
		}
		// Only stamp txs executed by VM in this finalized block.
		if receipt.BlockHeight != finalizedHeight {
			continue
		}
		if status, ok := normalizeReceiptStatus(receipt.Status); ok {
			base.Status = status
			base.ExecutedHeight = receipt.BlockHeight
		}
	}

	return cloned
}

func (s *RealBlockStore) Add(block *types.Block) (bool, error) {
	// 第一步：快速检查是否已存在 + 验证区块（持锁）
	s.mu.Lock()
	if _, exists := s.blockCache[block.ID]; exists {
		s.mu.Unlock()
		return false, nil
	}
	// validateBlock 需要读取 blockCache
	if err := s.validateBlock(block); err != nil {
		s.mu.Unlock()
		return false, err
	}
	s.mu.Unlock()

	// 第二步（新增）：检查是否有完整的区块数据（交易体）
	// 这是共识安全的关键门槛：没有完整数据的块不能进入候选池
	pbBlock, hasFullData := GetCachedBlock(block.ID)
	if (!hasFullData || !isCompleteBlockPayload(pbBlock)) && s.dbManager != nil {
		if dbBlock, err := s.dbManager.GetBlockByID(block.ID); err == nil && isCompleteBlockPayload(dbBlock) {
			pbBlock = dbBlock
			hasFullData = true
			CacheBlock(dbBlock)
		}
	}
	if !hasFullData || !isCompleteBlockPayload(pbBlock) {
		// 没有完整数据，拒绝加入共识候选
		logs.Debug("[RealBlockStore] Block %s rejected: no full data available (awaiting transaction body)", block.ID)
		return false, fmt.Errorf("block data incomplete: awaiting transaction body")
	}

	// 第三步：占坑，防止并发 worker 重复执行 VM
	s.mu.Lock()
	// 再次检查（可能在释放锁期间被其他 goroutine 添加了）
	if _, exists := s.blockCache[block.ID]; exists {
		s.mu.Unlock()
		return false, nil
	}
	s.blockCache[block.ID] = block
	s.heightIndex[block.Header.Height] = append(s.heightIndex[block.Header.Height], block)
	s.mu.Unlock()

	// 第四步：轻量验证（延迟执行架构）
	// 重计算（订单簿重建、撮合、余额计算）延迟到 CommitFinalizedBlock 阶段执行，
	// 避免对不会最终化的候选区块浪费 CPU。
	if err := s.lightweightValidateBlock(pbBlock); err != nil {
		logs.Debug("[RealBlockStore] Block %s failed lightweight validation: %v", block.ID, err)
		s.mu.Lock()
		delete(s.blockCache, block.ID)
		s.mu.Unlock()
		return false, fmt.Errorf("lightweight validation failed: %w", err)
	}

	logs.Debug("[RealBlockStore] Block %s passed lightweight validation", block.ID)

	// 第三步：更新高度元数据
	s.mu.Lock()
	if block.Header.Height > s.maxHeight {
		s.maxHeight = block.Header.Height
	}
	s.mu.Unlock()

	// 注意：不在 Add() 阶段保存区块到数据库
	// 原因：Add() 时交易状态仍为 PENDING，如果异步 SaveBlock 在 SetFinalized()
	// 的 SaveBlock（SUCCEED）之后执行，会用 PENDING 覆盖 SUCCEED，造成状态回退。
	// 区块的持久化统一由 SetFinalized() 处理，确保保存的是最终状态。

	logs.Debug("[RealBlockStore] Added block %s at height %d", block.ID, block.Header.Height)

	return true, nil
}

// Get 获取区块
func (s *RealBlockStore) Get(id string) (*types.Block, bool) {
	s.mu.RLock()
	// 先从内存缓存查找
	if block, exists := s.blockCache[id]; exists {
		defer s.mu.RUnlock()
		return block, true
	}
	s.mu.RUnlock()

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
		s.mu.Lock()
		s.blockCache[id] = block
		s.mu.Unlock()
		return block, true
	}

	return nil, false
}

// GetByHeight 获取指定高度的所有区块
func (s *RealBlockStore) GetByHeight(height uint64) []*types.Block {
	s.mu.RLock()
	// 先从内存查找
	if blocks, exists := s.heightIndex[height]; exists {
		result := make([]*types.Block, len(blocks))
		copy(result, blocks)
		s.mu.RUnlock()
		return result
	}

	// NEW: 如果请求的高度还没被任何区块覆盖，就直接返回空，避免打 DB
	if height > s.maxHeight {
		s.mu.RUnlock()
		return []*types.Block{}
	}
	s.mu.RUnlock()

	// 从数据库查找
	dbBlock, err := s.dbManager.GetBlock(height)
	if err != nil || dbBlock == nil {
		return []*types.Block{}
	}

	// 转换为types.Block
	block := s.convertDBBlockToTypes(dbBlock)
	if block != nil {
		s.mu.Lock()
		s.blockCache[block.ID] = block
		s.heightIndex[height] = []*types.Block{block}
		s.mu.Unlock()
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
	// 从内存查找
	if block, exists := s.finalizedBlocks[height]; exists {
		s.mu.RUnlock()
		return block, true
	}
	s.mu.RUnlock()

	// 从数据库查找
	dbBlock, err := s.dbManager.GetBlock(height)
	if err != nil || dbBlock == nil {
		return nil, false
	}

	block := s.convertDBBlockToTypes(dbBlock)
	if block != nil {
		s.mu.Lock()
		s.finalizedBlocks[height] = block
		s.mu.Unlock()
		return block, true
	}

	return nil, false
}

// 获取指定高度范围的区块（只返回已最终化的区块，用于同步）
func (s *RealBlockStore) GetBlocksFromHeight(from, to uint64) []*types.Block {
	s.mu.RLock()
	defer s.mu.RUnlock()

	blocks := make([]*types.Block, 0)
	for h := from; h <= to && h <= s.maxHeight; h++ {
		// 优先返回已最终化的区块
		if finalizedBlock, exists := s.finalizedBlocks[h]; exists {
			blocks = append(blocks, finalizedBlock)
		} else if heightBlocks, exists := s.heightIndex[h]; exists && len(heightBlocks) > 0 {
			// 如果该高度尚未最终化，返回第一个候选区块（降级处理）
			blocks = append(blocks, heightBlocks[0])
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
// 原子语义：只有 VM 提交 + 区块持久化成功后，才推进内存 finalized/lastAccepted。
func (s *RealBlockStore) SetFinalized(height uint64, blockID string) error {
	// 同一节点内串行化最终化，避免并发 SetFinalized 触发 DB 事务冲突。
	s.finalizeMu.Lock()
	defer s.finalizeMu.Unlock()

	// 第一步：获取区块并验证（短暂持锁）
	s.mu.Lock()

	// 幂等处理：若该高度已经最终化为同一 block，直接 no-op。
	if finalized, ok := s.finalizedBlocks[height]; ok && finalized != nil {
		if finalized.ID == blockID {
			s.mu.Unlock()
			logs.Debug("[RealBlockStore] Skip duplicate finalization: height=%d block=%s", height, blockID)
			return ErrAlreadyFinalized
		}
		s.mu.Unlock()
		err := fmt.Errorf("height %d already finalized by %s", height, finalized.ID)
		logs.Error("[RealBlockStore] Cannot finalize block %s at height %d: %v", blockID, height, err)
		return err
	}

	block, exists := s.blockCache[blockID]
	if !exists {
		// 从数据库通过ID加载
		if dbBlock, err := s.dbManager.GetBlockByID(blockID); err == nil && dbBlock != nil {
			block = s.convertDBBlockToTypes(dbBlock)
			s.blockCache[blockID] = block
		}
	}

	if block == nil {
		s.mu.Unlock()
		err := fmt.Errorf("block not found")
		logs.Error("[RealBlockStore] Cannot finalize block %s: %v", blockID, err)
		return err
	}

	// 关键安全检查：验证父区块链接
	// 只有当区块的 ParentID 指向前一高度已最终化的区块时，才允许最终化
	if height > 0 {
		parentBlock, parentExists := s.finalizedBlocks[height-1]
		if !parentExists {
			s.mu.Unlock()
			err := fmt.Errorf("parent at height %d not finalized", height-1)
			logs.Error("[RealBlockStore] Cannot finalize block %s at height %d: %v", blockID, height, err)
			return err
		}
		if block.Header.ParentID != parentBlock.ID {
			s.mu.Unlock()
			err := fmt.Errorf("parent mismatch (expected %s, got %s)", parentBlock.ID, block.Header.ParentID)
			logs.Error("[RealBlockStore] Cannot finalize block %s at height %d: %v", blockID, height, err)
			return err
		}
	}

	// 获取事件总线引用（提交后异步发布）
	events := s.events
	s.mu.Unlock()

	// 第二步：先做持久化提交（不持锁，这是耗时操作）
	// 注意：提交成功前不推进 finalized，保证「共识 finalized」与「状态已落盘」一致。
	commitStart := time.Now()
	var txCount int
	// 优先使用完整的 pb.Block（包含交易）
	pbBlock, exists := GetCachedBlock(block.ID)
	if (!exists || !isCompleteBlockPayload(pbBlock)) && s.dbManager != nil {
		if dbBlock, err := s.dbManager.GetBlockByID(blockID); err == nil && isCompleteBlockPayload(dbBlock) {
			pbBlock = dbBlock
			exists = true
			CacheBlock(dbBlock)
		}
	}
	if exists && isCompleteBlockPayload(pbBlock) {
		if err := s.vmExecutor.CommitFinalizedBlock(pbBlock); err != nil {
			logs.Error("[RealBlockStore] VM CommitFinalizedBlock failed for block %s: %v", block.ID, err)
			return fmt.Errorf("vm commit failed: %w", err)
		}
		txCount = len(pbBlock.Body)
		logs.Info("[RealBlockStore] VM committed finalized block %s with %d txs at height %d",
			block.ID, txCount, height)

		// 从交易池移除已执行的交易
		// 注意：交易原文的保存已由 VM 的 applyResult 统一处理（使用 SaveTxRaw）
		// 区块存储层不再保存交易，避免重复写入和索引混乱
		for _, tx := range pbBlock.Body {
			if base := tx.GetBase(); base != nil {
				s.pool.RemoveAnyTx(base.TxId)
			}
		}

		// 保存区块元数据与正文（用于 /getblock、sync 等按 blockID 查询）。
		// 这里使用 VM 回执回填后的副本，确保最终化区块中的 tx.status 与 VM 结果一致。
		finalizedPBBlock := s.materializeFinalizedBlock(pbBlock)
		if err := s.dbManager.SaveBlock(finalizedPBBlock); err != nil {
			logs.Error("[RealBlockStore] Failed to save finalized block %s: %v", block.ID, err)
			return fmt.Errorf("save finalized block failed: %w", err)
		}
		CacheBlock(finalizedPBBlock)
		// 避免每块都强制刷盘导致共识主路径阻塞，改为按策略批量/间隔刷盘。
		s.maybeForceFlushAfterFinalize()
	} else {
		// 兜底：若缓存缺失，至少确认区块已在 DB 中可见；否则不能推进 finalized。
		if _, err := s.dbManager.GetBlockByID(blockID); err != nil {
			logs.Error("[RealBlockStore] Cannot finalize block %s at height %d: missing cached pb.Block and not persisted: %v",
				blockID, height, err)
			return fmt.Errorf("no cached pb.Block and block not persisted: %w", err)
		}
		if block.Header.Height > 0 {
			logs.Debug("[RealBlockStore] No cached pb.Block for %s; finalized using already-persisted block", block.ID)
		}
	}

	if duration := time.Since(commitStart); duration > 200*time.Millisecond {
		logs.Info("[RealBlockStore] SLOW Finalization: block=%s, txs=%d, duration=%v", block.ID, txCount, duration)
	}

	// 第三步：持久化成功后再推进内存最终化状态
	s.mu.Lock()
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
	s.mu.Unlock()

	logs.Info("[RealBlockStore] Finalized block %s at height %d", blockID, height)

	// 第四步：清理旧的缓存数据（避免内存泄漏）
	// 保留最近 20 个高度的数据
	const keepRecentHeights = 20
	if height > keepRecentHeights {
		cleanupHeight := height - keepRecentHeights

		// 清理全局 pb.Block 缓存
		cleanedCount := CleanupBlockCacheBelowHeight(cleanupHeight)
		if cleanedCount > 0 {
			logs.Debug("[RealBlockStore] Cleaned %d blocks from global cache below height %d", cleanedCount, cleanupHeight)
		}

		// 清理本地缓存（需要加锁）
		s.mu.Lock()
		s.cleanupOldData(cleanupHeight)
		s.mu.Unlock()

		// 清理 VM 缓存
		if s.vmExecutor != nil {
			s.vmExecutor.CleanupCache(cleanupHeight)
		}
	}

	// 第五步：发布事件（不持锁）
	if events != nil {
		events.PublishAsync(types.BaseEvent{
			EventType: types.EventBlockFinalized,
			EventData: block,
		})
	}
	return nil
}

// 内部辅助方法

func (s *RealBlockStore) validateBlock(block *types.Block) error {
	if block == nil || block.ID == "" {
		return fmt.Errorf("invalid block")
	}
	if block.Header.Height == 0 && block.ID != "genesis" {
		return fmt.Errorf("invalid genesis block")
	}
	if block.Header.Height > 0 && block.Header.ParentID == "" {
		return fmt.Errorf("non-genesis block must have parent")
	}

	// 父区块链接验证（关键共识安全检查）
	// 确保新区块的 ParentID 指向本地已知的父区块，且高度正确
	if block.Header.Height > 0 {
		parent, exists := s.blockCache[block.Header.ParentID]
		if !exists {
			// 尝试从数据库加载
			if dbBlock, err := s.dbManager.GetBlockByID(block.Header.ParentID); err == nil && dbBlock != nil {
				parent = s.convertDBBlockToTypes(dbBlock)
				if parent != nil {
					s.blockCache[block.Header.ParentID] = parent
					exists = true
				}
			}
		}

		if !exists {
			logs.Warn("[RealBlockStore] Block %s rejected: parent %s not found locally", block.ID, block.Header.ParentID)
			return fmt.Errorf("parent block %s not found", block.Header.ParentID)
		}

		// 验证父区块高度正确
		if parent.Header.Height != block.Header.Height-1 {
			logs.Warn("[RealBlockStore] Block %s rejected: parent height mismatch (expected %d, got %d)",
				block.ID, block.Header.Height-1, parent.Header.Height)
			return fmt.Errorf("parent height mismatch: expected %d, got %d", block.Header.Height-1, parent.Header.Height)
		}
	}

	// VRF验证（跳过创世区块）
	if block.Header.Height > 0 {
		if err := s.validateVRF(block); err != nil {
			return fmt.Errorf("VRF validation failed: %w", err)
		}
	}

	return nil
}

// 验证区块的VRF证明
func (s *RealBlockStore) validateVRF(block *types.Block) error {
	// 检查区块是否包含VRF证明
	if len(block.Header.VRFProof) == 0 || len(block.Header.VRFOutput) == 0 {
		logs.Debug("[RealBlockStore] Block %s has no VRF proof, skipping validation", block.ID)
		return nil // 旧区块可能没有VRF，跳过验证
	}

	// 检查区块是否包含BLS公钥
	if len(block.Header.BLSPublicKey) == 0 {
		logs.Warn("[RealBlockStore] Block %s has VRF proof but no BLS public key", block.ID)
		return fmt.Errorf("missing BLS public key for VRF verification")
	}

	// 反序列化BLS公钥
	blsPublicKey, err := utils.DeserializeBLSPublicKey(block.Header.BLSPublicKey)
	if err != nil {
		logs.Warn("[RealBlockStore] Failed to deserialize BLS public key for block %s: %v", block.ID, err)
		return fmt.Errorf("invalid BLS public key: %w", err)
	}

	// 使用BLS公钥验证VRF证明
	vrfProvider := utils.NewVRFProvider()
	err = vrfProvider.VerifyVRFWithBLSPublicKey(
		blsPublicKey,
		block.Header.Height,
		block.Header.Window,
		block.Header.ParentID,
		types.NodeID(block.Header.Proposer),
		block.Header.VRFProof,
		block.Header.VRFOutput,
	)

	if err != nil {
		logs.Warn("[RealBlockStore] VRF verification failed for block %s: %v", block.ID, err)
		return err
	}

	logs.Debug("[RealBlockStore] VRF verification passed for block %s", block.ID)
	return nil
}

// lightweightValidateBlock 轻量验证区块（延迟执行架构）
// 只做格式检查和去重，跳过订单簿重建、撮合、余额计算等重计算。
// 全量 VM 执行延迟到 CommitFinalizedBlock 阶段。
func (s *RealBlockStore) lightweightValidateBlock(block *pb.Block) error {
	if block == nil || block.Header == nil {
		return fmt.Errorf("nil block or header")
	}

	seenTxIDs := make(map[string]struct{}, len(block.Body))
	for i, tx := range block.Body {
		if tx == nil {
			continue
		}

		// 检查交易格式是否可解析
		kind, err := vm.DefaultKindFn(tx)
		if err != nil {
			return fmt.Errorf("tx %d has invalid structure: %v", i, err)
		}
		_ = kind

		// 检查区块内重复 TxId
		if base := tx.GetBase(); base != nil && base.TxId != "" {
			if _, exists := seenTxIDs[base.TxId]; exists {
				return fmt.Errorf("duplicate tx_id in block: %s", base.TxId)
			}
			seenTxIDs[base.TxId] = struct{}{}
		}
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
		}
	} else {
		// 创建简单的数据库区块
		dbBlock := &pb.Block{
			BlockHash: block.ID,
			Header: &pb.BlockHeader{
				Height:        block.Header.Height,
				PrevBlockHash: block.Header.ParentID,
				Miner:         fmt.Sprintf(keys.KeyNode()+"%s", block.Header.Proposer),
				Window:        int32(block.Header.Window),
				VrfProof:      block.Header.VRFProof,
				VrfOutput:     block.Header.VRFOutput,
				BlsPublicKey:  block.Header.BLSPublicKey,
			},
		}
		if err := s.dbManager.SaveBlock(dbBlock); err != nil {
			logs.Error("[RealBlockStore] Failed to save simple block to DB: %v", err)
		} else {
			logs.Debug("[RealBlockStore] Saved simple block %s to DB", block.ID)
		}
	}
}

func (s *RealBlockStore) loadFromDB() {
	// 尝试读取 latest key（可能因为历史竞态出现偏大或偏小）
	heightFromLatest := uint64(0)
	hasLatestKey := false
	if h, err := s.dbManager.GetLatestBlockHeight(); err == nil {
		heightFromLatest = h
		hasLatestKey = true
	}

	// 扫描真实存在的 height_<h>_blocks 键，作为恢复兜底
	heightFromScan, hasScannedHeight := s.findMaxPersistedHeightKey()

	// 计算恢复时的有效 tip：优先使用真实存在的最高高度
	effectiveTip := uint64(0)
	hasPersistedTip := false
	if hasLatestKey {
		effectiveTip = heightFromLatest
		hasPersistedTip = true
	}
	if hasScannedHeight && (!hasPersistedTip || heightFromScan > effectiveTip) {
		if hasLatestKey && heightFromScan != heightFromLatest {
			logs.Warn("[RealBlockStore] latest height key=%d, but scanned max persisted height=%d, using scanned tip",
				heightFromLatest, heightFromScan)
		}
		effectiveTip = heightFromScan
		hasPersistedTip = true
	}

	if !hasPersistedTip {
		// 全新数据库：首次启动时将创世块持久化
		if s.lastAccepted != nil {
			s.saveBlockToDB(s.lastAccepted)
		}
		logs.Info("[RealBlockStore] No persisted tip found, initialized with genesis")
		return
	}

	// 加载最近的一些区块到缓存
	startHeight := uint64(0)
	if effectiveTip > 100 {
		startHeight = effectiveTip - 100
	}

	var loadedTip *types.Block
	var loadedTipHeight uint64

	for h := startHeight; h <= effectiveTip; h++ {
		if dbBlock, err := s.dbManager.GetBlock(h); err == nil && dbBlock != nil {
			block := s.convertDBBlockToTypes(dbBlock)
			if block == nil {
				continue
			}
			s.blockCache[block.ID] = block
			s.heightIndex[h] = []*types.Block{block}
			s.finalizedBlocks[h] = block // 恢复内存中的最终化映射

			if loadedTip == nil || h > loadedTipHeight {
				loadedTip = block
				loadedTipHeight = h
			}
		}
	}

	if loadedTip != nil {
		s.lastAccepted = loadedTip
		s.lastAcceptedHeight = loadedTipHeight
		s.maxHeight = loadedTipHeight

		if hasLatestKey && loadedTipHeight != heightFromLatest {
			logs.Warn("[RealBlockStore] latest height key=%d but highest loadable block=%d, using loadable tip",
				heightFromLatest, loadedTipHeight)
		}

		logs.Info("[RealBlockStore] Loaded blocks from DB, tip height: %d (latest key: %d, scanned max: %d)",
			loadedTipHeight, heightFromLatest, heightFromScan)
		return
	}

	// latest key 存在但按高度找不到区块，回退到创世并修复基础键
	s.maxHeight = 0
	s.lastAcceptedHeight = 0
	if genesis, exists := s.blockCache["genesis"]; exists {
		s.lastAccepted = genesis
		s.finalizedBlocks[0] = genesis
	}
	if _, err := s.dbManager.GetBlock(0); err != nil && s.lastAccepted != nil {
		s.saveBlockToDB(s.lastAccepted)
	}
	logs.Warn("[RealBlockStore] persisted tip metadata exists but no blocks loadable (latest=%d, scanned=%d), fallback to genesis",
		heightFromLatest, heightFromScan)
}

func (s *RealBlockStore) findMaxPersistedHeightKey() (uint64, bool) {
	if s.dbManager == nil {
		return 0, false
	}

	const (
		prefix = "v1_height_"
		suffix = "_blocks"
	)

	records, err := s.dbManager.Scan(prefix)
	if err != nil {
		return 0, false
	}

	var (
		maxHeight uint64
		found     bool
	)

	for key := range records {
		if !strings.HasSuffix(key, suffix) {
			continue
		}

		raw := strings.TrimSuffix(strings.TrimPrefix(key, prefix), suffix)
		h, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			continue
		}

		if !found || h > maxHeight {
			maxHeight = h
			found = true
		}
	}

	return maxHeight, found
}

func (s *RealBlockStore) convertDBBlockToTypes(dbBlock *pb.Block) *types.Block {
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
		// 从交易池移除已执行的交易
		// 注意：交易原文的保存已由 VM 的 applyResult 统一处理（使用 SaveTxRaw）
		// 区块存储层不再保存交易，避免重复写入和索引混乱
		for _, tx := range cachedBlock.Body {
			base := tx.GetBase()
			if base != nil {
				s.pool.RemoveAnyTx(base.TxId)
			}
		}

		// 保存最终化的区块
		if err := s.dbManager.SaveBlock(cachedBlock); err != nil {
			logs.Error("[RealBlockStore] Failed to save finalized block: %v", err)
		} else {
			s.maybeForceFlushAfterFinalize()
		}

		logs.Info("[RealBlockStore] Finalized block %s with %d txs", block.ID, len(cachedBlock.Body))
	}
}

func (s *RealBlockStore) maybeForceFlushAfterFinalize() {
	if s.dbManager == nil {
		return
	}

	s.forceFlushMu.Lock()
	defer s.forceFlushMu.Unlock()

	s.finalizedSinceLastFlush++

	// 安全兜底：若两个触发器都关闭，回退到每块强刷。
	if s.forceFlushEveryN <= 0 && s.forceFlushMaxInterval <= 0 {
		if err := s.dbManager.ForceFlush(); err != nil {
			logs.Error("[RealBlockStore] ForceFlush failed (fallback): %v", err)
			return
		}
		s.finalizedSinceLastFlush = 0
		s.lastForceFlushAt = time.Now()
		return
	}

	countTrigger := s.forceFlushEveryN > 0 && s.finalizedSinceLastFlush >= uint64(s.forceFlushEveryN)
	intervalTrigger := s.forceFlushMaxInterval > 0 && time.Since(s.lastForceFlushAt) >= s.forceFlushMaxInterval
	if !countTrigger && !intervalTrigger {
		return
	}

	pending := s.finalizedSinceLastFlush
	if err := s.dbManager.ForceFlush(); err != nil {
		logs.Error("[RealBlockStore] ForceFlush failed: pending=%d err=%v", pending, err)
		return
	}

	s.finalizedSinceLastFlush = 0
	s.lastForceFlushAt = time.Now()
}

// cleanupOldData 清理低于指定高度的旧缓存数据（必须持有写锁调用）
func (s *RealBlockStore) cleanupOldData(belowHeight uint64) {
	cleanedBlocks := 0
	cleanedHeights := 0
	cleanedFinalized := 0

	// 清理 blockCache 中的旧区块
	for blockID, block := range s.blockCache {
		if block.Header.Height < belowHeight {
			delete(s.blockCache, blockID)
			cleanedBlocks++
		}
	}

	// 清理 heightIndex 中的旧高度
	for height := range s.heightIndex {
		if height < belowHeight {
			delete(s.heightIndex, height)
			cleanedHeights++
		}
	}

	// 清理 finalizedBlocks 中的旧高度
	for height := range s.finalizedBlocks {
		if height < belowHeight {
			delete(s.finalizedBlocks, height)
			cleanedFinalized++
		}
	}

	// 清理 finalizationChits 中的旧高度
	for height := range s.finalizationChits {
		if height < belowHeight {
			delete(s.finalizationChits, height)
		}
	}

	// 清理 signatureSets 中的旧高度
	for height := range s.signatureSets {
		if height < belowHeight {
			delete(s.signatureSets, height)
		}
	}

	if cleanedBlocks > 0 || cleanedHeights > 0 || cleanedFinalized > 0 {
		logs.Debug("[RealBlockStore] Cleaned old data below height %d: blocks=%d, heights=%d, finalized=%d",
			belowHeight, cleanedBlocks, cleanedHeights, cleanedFinalized)
	}
}

func (s *RealBlockStore) GetWitnessService() *witness.Service {
	if s.vmExecutor == nil {
		return nil
	}
	return s.vmExecutor.GetWitnessService()
}

// GetPendingBlocksCount 获取候选区块数量（未最终化，去重显示）
func (s *RealBlockStore) GetPendingBlocksCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[string]bool)
	for height := s.lastAcceptedHeight + 1; height <= s.maxHeight; height++ {
		if blocks, exists := s.heightIndex[height]; exists {
			for _, b := range blocks {
				seen[b.ID] = true
			}
		}
	}
	return len(seen)
}

// GetPendingBlocks 获取候选区块列表（去重返回）
func (s *RealBlockStore) GetPendingBlocks() []*types.Block {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*types.Block, 0)
	seen := make(map[string]bool)
	for height := s.lastAcceptedHeight + 1; height <= s.maxHeight; height++ {
		if blocks, exists := s.heightIndex[height]; exists {
			for _, b := range blocks {
				if !seen[b.ID] {
					result = append(result, b)
					seen[b.ID] = true
				}
			}
		}
	}
	return result
}

// SetFinalizationChits 存储指定高度的最终化投票信息
func (s *RealBlockStore) SetFinalizationChits(height uint64, chits *types.FinalizationChits) {
	s.mu.Lock()
	s.finalizationChits[height] = chits
	s.mu.Unlock()

	// 异步持久化到数据库
	go func() {
		key := fmt.Sprintf("finalization_chits_%d", height)
		data, err := json.Marshal(chits)
		if err != nil {
			logs.Warn("[RealBlockStore] Failed to marshal finalization chits for height %d: %v", height, err)
			return
		}
		s.dbManager.EnqueueSet(key, string(data))
	}()
}

// GetFinalizationChits 获取指定高度的最终化投票信息
func (s *RealBlockStore) GetFinalizationChits(height uint64) (*types.FinalizationChits, bool) {
	s.mu.RLock()
	if chits, exists := s.finalizationChits[height]; exists {
		s.mu.RUnlock()
		return chits, true
	}
	s.mu.RUnlock()

	// 尝试从数据库加载
	key := fmt.Sprintf("finalization_chits_%d", height)
	data, err := s.dbManager.Read(key)
	if err != nil || data == "" {
		return nil, false
	}

	var chits types.FinalizationChits
	if err := json.Unmarshal([]byte(data), &chits); err != nil {
		logs.Warn("[RealBlockStore] Failed to unmarshal finalization chits for height %d: %v", height, err)
		return nil, false
	}

	// 缓存到内存
	s.mu.Lock()
	s.finalizationChits[height] = &chits
	s.mu.Unlock()

	return &chits, true
}

// SetSignatureSet 存储指定高度的 VRF 签名集合
func (s *RealBlockStore) SetSignatureSet(height uint64, sigSet *pb.ConsensusSignatureSet) {
	s.mu.Lock()
	s.signatureSets[height] = sigSet
	s.mu.Unlock()

	// 异步持久化（protobuf 序列化）
	go func() {
		key := fmt.Sprintf("consensus_sig_set_%d", height)
		data, err := json.Marshal(sigSet)
		if err != nil {
			logs.Warn("[RealBlockStore] Failed to marshal signature set for height %d: %v", height, err)
			return
		}
		s.dbManager.EnqueueSet(key, string(data))
	}()
}

// GetSignatureSet 获取指定高度的 VRF 签名集合
func (s *RealBlockStore) GetSignatureSet(height uint64) (*pb.ConsensusSignatureSet, bool) {
	s.mu.RLock()
	if sigSet, exists := s.signatureSets[height]; exists {
		s.mu.RUnlock()
		return sigSet, true
	}
	s.mu.RUnlock()

	// 尝试从数据库加载
	key := fmt.Sprintf("consensus_sig_set_%d", height)
	data, err := s.dbManager.Read(key)
	if err != nil || data == "" {
		return nil, false
	}

	var sigSet pb.ConsensusSignatureSet
	if err := json.Unmarshal([]byte(data), &sigSet); err != nil {
		logs.Warn("[RealBlockStore] Failed to unmarshal signature set for height %d: %v", height, err)
		return nil, false
	}

	// 缓存到内存
	s.mu.Lock()
	s.signatureSets[height] = &sigSet
	s.mu.Unlock()

	return &sigSet, true
}
