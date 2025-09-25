package interfaces

import (
	"context"
	"dex/types"
)

type BlockStore interface {
	Add(block *types.Block) (bool, error)
	Get(id string) (*types.Block, bool)
	GetByHeight(height uint64) []*types.Block
	GetLastAccepted() (string, uint64)
	GetFinalizedAtHeight(height uint64) (*types.Block, bool)
	GetBlocksFromHeight(from, to uint64) []*types.Block
	GetCurrentHeight() uint64

	// 快照相关
	CreateSnapshot(height uint64) (*types.Snapshot, error)
	LoadSnapshot(snapshot *types.Snapshot) error
	GetLatestSnapshot() (*types.Snapshot, bool)
	GetSnapshotAtHeight(height uint64) (*types.Snapshot, bool)

	SetFinalized(height uint64, blockID string)
}

type ConsensusEngine interface {
	Start(ctx context.Context) error
	RegisterQuery(nodeID types.NodeID, requestID uint32, blockID string, height uint64) string
	SubmitChit(nodeID types.NodeID, queryKey string, preferredID string)
	GetActiveQueryCount() int
	GetPreference(height uint64) string
}

type Event interface {
	Type() types.EventType
	Data() interface{}
}

// ============================================
// 区块提案接口定义
// ============================================

// BlockProposer 定义了区块提案的接口
type BlockProposer interface {
	// ProposeBlock 生成一个新的区块提案
	// parentID: 父区块ID
	// height: 区块高度
	// proposer: 提案者ID
	// round: 提案轮次
	ProposeBlock(parentID string, height uint64, proposer types.NodeID, round int) (*types.Block, error)

	// ShouldPropose 决定是否应该在当前轮次提出区块
	// nodeID: 节点ID
	// round: 当前轮次
	// currentBlocks: 当前高度已存在的区块数量
	ShouldPropose(nodeID types.NodeID, round int, currentBlocks int) bool
}

// ============================================
// 网络传输层接口
// ============================================

type Transport interface {
	Send(to types.NodeID, msg types.Message) error
	Receive() <-chan types.Message
	Broadcast(msg types.Message, peers []types.NodeID)
	SamplePeers(exclude types.NodeID, count int) []types.NodeID
}

type EventBus interface {
	Subscribe(topic types.EventType, handler EventHandler)
	Publish(event Event)
	PublishAsync(event Event)
}

type EventHandler func(Event)
