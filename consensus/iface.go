package consensus

import "context"

type BlockStore interface {
	Add(block *Block) (bool, error)
	Get(id string) (*Block, bool)
	GetByHeight(height uint64) []*Block
	GetLastAccepted() (string, uint64)
	GetFinalizedAtHeight(height uint64) (*Block, bool)
	GetBlocksFromHeight(from, to uint64) []*Block
	GetCurrentHeight() uint64

	// 快照相关
	CreateSnapshot(height uint64) (*Snapshot, error)
	LoadSnapshot(snapshot *Snapshot) error
	GetLatestSnapshot() (*Snapshot, bool)
	GetSnapshotAtHeight(height uint64) (*Snapshot, bool)
}

type ConsensusEngine interface {
	Start(ctx context.Context) error
	RegisterQuery(nodeID NodeID, requestID uint32, blockID string, height uint64) string
	SubmitChit(nodeID NodeID, queryKey string, preferredID string)
	GetActiveQueryCount() int
	GetPreference(height uint64) string
}

type Event interface {
	Type() EventType
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
	ProposeBlock(parentID string, height uint64, proposer NodeID, round int) (*Block, error)

	// ShouldPropose 决定是否应该在当前轮次提出区块
	// nodeID: 节点ID
	// round: 当前轮次
	// currentBlocks: 当前高度已存在的区块数量
	ShouldPropose(nodeID NodeID, round int, currentBlocks int) bool
}

// ============================================
// 网络传输层接口
// ============================================

type Transport interface {
	Send(to NodeID, msg Message) error
	Receive() <-chan Message
	Broadcast(msg Message, peers []NodeID)
	SamplePeers(exclude NodeID, count int) []NodeID
}
