package types

type NodeID int

// 消息类型
type MessageType int

const (
	MsgPullQuery MessageType = iota
	MsgPushQuery
	MsgChits
	MsgGet
	MsgPut
	MsgGossip
	MsgSyncRequest
	MsgSyncResponse
	MsgHeightQuery
	MsgHeightResponse
	MsgSnapshotRequest  // 新增：请求快照
	MsgSnapshotResponse // 新增：快照响应
)

// 基础消息结构
type Message struct {
	Type      MessageType
	From      NodeID
	RequestID uint32
	BlockID   string
	Block     *Block
	Height    uint64
	// For Chits
	PreferredID       string
	PreferredIDHeight uint64
	AcceptedID        string
	AcceptedHeight    uint64
	// For Sync
	FromHeight uint64
	ToHeight   uint64
	Blocks     []*Block
	SyncID     uint32
	// For Height Query
	CurrentHeight uint64
	// For Snapshot
	Snapshot        *Snapshot
	SnapshotHeight  uint64
	RequestSnapshot bool
}
