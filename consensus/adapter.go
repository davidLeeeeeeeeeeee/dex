package consensus

import (
	"dex/config"
	"dex/db"
	"dex/pb"
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
	contactedNodes  map[string]bool // 追踪联系过的节点
	contactedHeight uint64          // 当前追踪的区块高度
}

func NewConsensusAdapter(dbMgr *db.Manager) *ConsensusAdapter {
	return &ConsensusAdapter{
		dbManager:      dbMgr,
		contactedNodes: make(map[string]bool),
	}
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

	// 兼容：DB 中 Miner 字段有时会被写成带前缀的形式（例如 "v1_node_3"），
	// 但 VRF 生成/验证的 nodeID 必须一致，否则会导致同步拉到的新块无法通过 VRF 校验。
	proposer := dbBlock.Miner
	if prefix := db.KeyNode(); strings.HasPrefix(proposer, prefix) {
		proposer = strings.TrimPrefix(proposer, prefix)
	}
	return &types.Block{
		ID:           dbBlock.BlockHash,
		Height:       dbBlock.Height,
		ParentID:     dbBlock.PrevBlockHash,
		Data:         fmt.Sprintf("TxCount: %d, TxsHash: %s", len(dbBlock.Body), dbBlock.TxsHash),
		Proposer:     proposer,
		Window:       int(dbBlock.Window),
		VRFProof:     dbBlock.VrfProof,
		VRFOutput:    dbBlock.VrfOutput,
		BLSPublicKey: dbBlock.BlsPublicKey,
	}, nil
}

// ConsensusBlockToDB 将共识区块转换为数据库格式
func (a *ConsensusAdapter) ConsensusBlockToDB(block *types.Block, txs []*pb.AnyTx) *pb.Block {
	return &pb.Block{
		Height:        block.Height,
		BlockHash:     block.ID,
		PrevBlockHash: block.ParentID,
		Miner:         block.Proposer, // 直接存地址
		Body:          txs,
		TxsHash:       a.extractTxsHashFromData(block.Data),
		Window:        int32(block.Window),
		VrfProof:      block.VRFProof,
		VrfOutput:     block.VRFOutput,
		BlsPublicKey:  block.BLSPublicKey,
	}
}

// ============================================
// Message 转换
// ============================================

// 将共识消息转换为PushQuery
func (a *ConsensusAdapter) ConsensusMessageToPushQuery(msg types.Message, address string) (*pb.PushQuery, error) {
	container, isBlock := a.prepareContainer(msg)

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
		Height:    height,
		BlockHash: blockID,
		Body:      txs,
		ShortTxs:  container,
	}, nil
}

// ============================================
// 辅助方法
// ============================================

func (a *ConsensusAdapter) parseMinerToNodeID(miner string) types.NodeID {
	var nodeID int
	fmt.Sscanf(miner, db.KeyNode()+"%d", &nodeID)
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

func (a *ConsensusAdapter) prepareContainer(msg types.Message) ([]byte, bool) {
	// 准备容器数据的逻辑
	if msg.Block != nil {
		dbBlock := a.ConsensusBlockToDB(msg.Block, nil)
		data, _ := proto.Marshal(dbBlock)
		return data, true
	}
	return nil, false
}

func (a *ConsensusAdapter) resolveShorHashesToTxs(shortHashes []byte) ([]*pb.AnyTx, error) {
	// TODO: 实现短哈希到交易的解析
	return nil, nil
}
