package consensus

import (
	"dex/config"
	"dex/db"
	"dex/keys"
	"dex/pb"
	"dex/txpool"
	"dex/types"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
)

// 集中处理共识层与外部的所有数据转换
type ConsensusAdapter struct {
	dbManager       *db.Manager
	txPool          *txpool.TxPool  // TxPool 用于 ShortTxs 模式的交易还原
	contactedNodes  map[string]bool // 追踪联系过的节点
	contactedHeight uint64          // 当前追踪的区块高度
}

func NewConsensusAdapter(dbMgr *db.Manager) *ConsensusAdapter {
	return &ConsensusAdapter{
		dbManager:      dbMgr,
		contactedNodes: make(map[string]bool),
	}
}

// NewConsensusAdapterWithTxPool 创建带 TxPool 的适配器（用于 ShortTxs 模式）
func NewConsensusAdapterWithTxPool(dbMgr *db.Manager, pool *txpool.TxPool) *ConsensusAdapter {
	return &ConsensusAdapter{
		dbManager:      dbMgr,
		txPool:         pool,
		contactedNodes: make(map[string]bool),
	}
}

// SetTxPool 设置 TxPool（用于后期注入）
func (a *ConsensusAdapter) SetTxPool(pool *txpool.TxPool) {
	a.txPool = pool
}

// RecordContact 记录与某节点的通信
func (a *ConsensusAdapter) RecordContact(nodeID string, height uint64) {
	// 如果高度变化，重置追踪
	if height != a.contactedHeight {
		a.contactedNodes = make(map[string]bool)
		a.contactedHeight = height
	}
	a.contactedNodes[nodeID] = true
}

// GetContactedNodes 获取联系过的节点列表
func (a *ConsensusAdapter) GetContactedNodes() []string {
	nodes := make([]string, 0, len(a.contactedNodes))
	for node := range a.contactedNodes {
		nodes = append(nodes, node)
	}
	return nodes
}

// ============================================
// DBBlock -> Consensus Types 转换
// ============================================

// DBBlockToConsensus 将区块转换为共识区块类型
func (a *ConsensusAdapter) DBBlockToConsensus(dbBlock *pb.Block) (*types.Block, error) {
	if dbBlock == nil {
		return nil, fmt.Errorf("nil db block")
	}

	// 获取 header（兼容新旧格式）
	header := dbBlock.Header
	if header == nil {
		// 旧格式兼容：从 Block 根字段构造 Header
		return nil, fmt.Errorf("block header is nil, old format blocks are no longer supported")
	}

	// 兼容：DB 中 Miner 字段有时会被写成带前缀的形式（例如 "v1_node_3"），
	// 但 VRF 生成/验证的 nodeID 必须一致，否则会导致同步拉到的新块无法通过 VRF 校验。
	proposer := header.Miner
	if prefix := keys.KeyNode(); strings.HasPrefix(proposer, prefix) {
		proposer = strings.TrimPrefix(proposer, prefix)
	}

	return &types.Block{
		ID: dbBlock.BlockHash,
		Header: types.BlockHeader{
			Height:       header.Height,
			ParentID:     header.PrevBlockHash,
			Timestamp:    header.Timestamp,
			StateRoot:    string(header.StateRoot),
			TxHash:       header.TxsHash,
			Proposer:     proposer,
			Window:       int(header.Window),
			VRFProof:     header.VrfProof,
			VRFOutput:    header.VrfOutput,
			BLSPublicKey: header.BlsPublicKey,
		},
		// Transactions 字段暂不填充，需要时从 dbBlock.Body 提取
	}, nil
}

// ConsensusBlockToDB 将共识区块转换为数据库格式
func (a *ConsensusAdapter) ConsensusBlockToDB(block *types.Block, txs []*pb.AnyTx) *pb.Block {
	if block == nil {
		return nil
	}

	useProvidedTxs := txs != nil
	body := txs
	var shortTxs []byte

	// Prefer the canonical cached pb.Block when available so propagation keeps Body/ShortTxs.
	if cachedBlock, exists := GetCachedBlock(block.ID); exists && cachedBlock != nil {
		if !useProvidedTxs && len(cachedBlock.Body) > 0 {
			body = cachedBlock.Body
		}
		if len(cachedBlock.ShortTxs) > 0 {
			shortTxs = cachedBlock.ShortTxs
		}
	}

	if len(shortTxs) == 0 && len(body) > 0 && a.txPool != nil {
		shortTxs = a.txPool.ConcatFirst8Bytes(body)
	}

	return &pb.Block{
		BlockHash: block.ID,
		Header: &pb.BlockHeader{
			Height:        block.Header.Height,
			PrevBlockHash: block.Header.ParentID,
			Timestamp:     block.Header.Timestamp,
			StateRoot:     []byte(block.Header.StateRoot),
			TxsHash:       block.Header.TxHash,
			Miner:         block.Header.Proposer,
			Window:        int32(block.Header.Window),
			VrfProof:      block.Header.VRFProof,
			VrfOutput:     block.Header.VRFOutput,
			BlsPublicKey:  block.Header.BLSPublicKey,
		},
		Body:     body,
		ShortTxs: shortTxs,
	}
}

// ============================================
// Message 转换
// ============================================

// 将共识消息转换为PushQuery
func (a *ConsensusAdapter) ConsensusMessageToPushQuery(msg types.Message, address string) (*pb.PushQuery, error) {
	container, isBlock, err := a.prepareContainer(msg)
	if err != nil {
		return nil, err
	}

	return &pb.PushQuery{
		Address:          address,
		Deadline:         a.calculateDeadline(3),
		ContainerIsBlock: isBlock,
		Container:        container,
		RequestedHeight:  msg.Height,
		BlockId:          msg.BlockID,
		RequestId:        msg.RequestID,
	}, nil
}

// PushQueryToConsensusMessage 将PushQuery转换为共识消息
func (a *ConsensusAdapter) PushQueryToConsensusMessage(pq *pb.PushQuery, from types.NodeID) (types.Message, error) {
	msg := types.Message{
		Type:      types.MsgPushQuery,
		From:      from,
		BlockID:   pq.BlockId,
		Height:    pq.RequestedHeight,
		RequestID: pq.RequestId,
	}

	if pq.ContainerIsBlock {
		var block pb.Block
		if err := proto.Unmarshal(pq.Container, &block); err != nil {
			return msg, err
		}
		consensusBlock, err := a.DBBlockToConsensus(&block)
		if err != nil {
			return msg, err
		}
		msg.Block = consensusBlock
		// 传递 ShortTxs 用于不完整区块的还原
		msg.ShortTxs = block.ShortTxs
	}

	return msg, nil
}

// DBChits -> ConsensusChits
func (a *ConsensusAdapter) ChitsToConsensusMessage(chits *pb.Chits, from types.NodeID) types.Message {
	return types.Message{
		Type:              types.MsgChits,
		From:              from,
		RequestID:         chits.RequestId,
		PreferredID:       chits.PreferredBlock,
		PreferredIDHeight: chits.PreferredBlockAtHeight,
		AcceptedID:        chits.AcceptedBlock,
		AcceptedHeight:    chits.AcceptedHeight,
	}
}

// 将共识消息转换为DBChits
func (a *ConsensusAdapter) ConsensusMessageToChits(msg types.Message) *pb.Chits {
	return &pb.Chits{
		RequestId:              msg.RequestID,
		PreferredBlock:         msg.PreferredID,
		AcceptedBlock:          msg.AcceptedID,
		PreferredBlockAtHeight: msg.PreferredIDHeight,
		AcceptedHeight:         msg.AcceptedHeight,
		Bitmap:                 a.generateBitmap(),
		Address:                string(msg.From), // 保留发送方地址
	}
}

// ============================================
// Transaction 转换
// ============================================

// PrepareBlockContainer 准备区块容器数据
func (a *ConsensusAdapter) PrepareBlockContainer(blockID string, height uint64) ([]byte, bool, error) {
	// 首先检查缓存
	cfg := config.DefaultConfig()
	if cachedBlock, exists := GetCachedBlock(blockID); exists {
		if len(cachedBlock.Body) < cfg.TxPool.MaxTxsPerBlock {
			data, err := proto.Marshal(cachedBlock)
			return data, true, err
		}
		return cachedBlock.ShortTxs, false, nil
	}

	// 从数据库获取
	block, err := a.dbManager.GetBlock(height)
	if err != nil {
		return nil, false, err
	}

	if block.BlockHash != blockID {
		return nil, false, fmt.Errorf("block ID mismatch")
	}

	if len(block.Body) < cfg.TxPool.MaxTxsPerBlock {
		data, err := proto.Marshal(block)
		return data, true, err
	}

	return block.ShortTxs, false, nil
}

// ProcessReceivedContainer 处理接收到的容器数据
func (a *ConsensusAdapter) ProcessReceivedContainer(container []byte, isBlock bool, height uint64, blockID string) (*pb.Block, error) {
	if isBlock {
		var block pb.Block
		if err := proto.Unmarshal(container, &block); err != nil {
			return nil, err
		}
		return &block, nil
	}

	// 处理短哈希列表
	txs, err := a.resolveShorHashesToTxs(container)
	if err != nil {
		return nil, err
	}

	return &pb.Block{
		BlockHash: blockID,
		Header: &pb.BlockHeader{
			Height: height,
		},
		Body:     txs,
		ShortTxs: container,
	}, nil
}

// ============================================
// 辅助方法
// ============================================

func (a *ConsensusAdapter) parseMinerToNodeID(miner string) types.NodeID {
	var nodeID int
	fmt.Sscanf(miner, keys.KeyNode()+"%d", &nodeID)
	return types.NodeID(strconv.Itoa(nodeID))
}

func (a *ConsensusAdapter) parseWindowFromBlockHash(blockHash string) int {
	// 从 block-<height>-<node>-w<window>-<hash> 格式解析
	var height, node, window int
	fmt.Sscanf(blockHash, "block-%d-%d-w%d", &height, &node, &window)
	return window
}

func (a *ConsensusAdapter) extractTxsHashFromData(data string) string {
	// 从 Data 字段解析 TxsHash
	var txCount int
	var txsHash string
	fmt.Sscanf(data, "TxCount: %d, TxsHash: %s", &txCount, &txsHash)
	return txsHash
}

func (a *ConsensusAdapter) calculateDeadline(seconds int) uint64 {
	return uint64(time.Now().Add(time.Duration(seconds) * time.Second).UnixNano())
}

func (a *ConsensusAdapter) generateBitmap() []byte {
	// 位图用于记录与哪些节点通信过
	// 每个节点用一个 bit 表示，最多支持 1024 个节点 (128 bytes * 8 bits)
	bitmap := make([]byte, 128)

	// 将联系过的节点编码到位图中
	// 使用节点地址的哈希值来确定位置
	for nodeID := range a.contactedNodes {
		// 计算节点在位图中的位置
		hash := 0
		for _, c := range nodeID {
			hash = (hash*31 + int(c)) & 0x3FF // 取低 10 位 (0-1023)
		}
		byteIdx := hash / 8
		bitIdx := uint(hash % 8)
		if byteIdx < len(bitmap) {
			bitmap[byteIdx] |= 1 << bitIdx
		}
	}

	return bitmap
}

func (a *ConsensusAdapter) prepareContainer(msg types.Message) ([]byte, bool, error) {
	// 准备容器数据的逻辑
	if msg.Block != nil {
		dbBlock := a.ConsensusBlockToDB(msg.Block, nil)
		if dbBlock == nil {
			return nil, false, fmt.Errorf("prepare pushquery container failed: nil db block for %s", msg.Block.ID)
		}
		if dbBlock.Header == nil {
			return nil, false, fmt.Errorf("prepare pushquery container failed: nil header for %s", msg.Block.ID)
		}
		data, err := proto.Marshal(dbBlock)
		if err != nil {
			return nil, false, fmt.Errorf("prepare pushquery container marshal failed for %s: %w", msg.Block.ID, err)
		}
		return data, true, nil
	}
	return nil, false, nil
}

func (a *ConsensusAdapter) resolveShorHashesToTxs(shortHashes []byte) ([]*pb.AnyTx, error) {
	txs, missing, err := a.ResolveShorHashesToTxsWithMissing(shortHashes)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		return txs, fmt.Errorf("missing transactions: expected %d, got %d (missing %d)",
			len(shortHashes)/8, len(txs), len(missing))
	}
	return txs, nil
}

// ResolveShorHashesToTxsWithMissing 解析短哈希并返回缺失列表（用于主动补课机制）
func (a *ConsensusAdapter) ResolveShorHashesToTxsWithMissing(shortHashes []byte) (
	txs []*pb.AnyTx,
	missingHashes [][]byte,
	err error,
) {
	// 检查 TxPool 是否已设置
	if a.txPool == nil {
		return nil, nil, fmt.Errorf("TxPool not set, cannot resolve short hashes")
	}

	// 将连续的 8 字节块切分为独立的短哈希
	const shortHashSize = 8
	if len(shortHashes)%shortHashSize != 0 {
		return nil, nil, fmt.Errorf("invalid shortHashes length: %d (must be multiple of %d)", len(shortHashes), shortHashSize)
	}

	hashCount := len(shortHashes) / shortHashSize
	allHashes := make([][]byte, 0, hashCount)
	for i := 0; i+shortHashSize <= len(shortHashes); i += shortHashSize {
		hash := make([]byte, shortHashSize)
		copy(hash, shortHashes[i:i+shortHashSize])
		allHashes = append(allHashes, hash)
	}

	// 调用 TxPool 解析
	txs = a.txPool.GetTxsByShortHashes(allHashes, true)

	// 如果数量不匹配，找出缺失的哈希
	if len(txs) != len(allHashes) {
		// 构建已找到交易的短哈希集合
		foundSet := make(map[string]bool)
		for _, tx := range txs {
			txID := tx.GetTxId()
			if len(txID) >= 18 {
				foundSet[txID[2:18]] = true // 短哈希是 txID[2:18]
			}
		}

		// 找出缺失的短哈希
		for _, hash := range allHashes {
			shortHex := fmt.Sprintf("%x", hash)
			if !foundSet[shortHex] {
				missingHashes = append(missingHashes, hash)
			}
		}
	}

	return txs, missingHashes, nil
}
