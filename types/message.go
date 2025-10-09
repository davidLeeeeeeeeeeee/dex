package types

import "strconv"

type NodeID string

func (id NodeID) Last2Mod100() int {
	s := string(id)
	if len(s) < 2 {
		return 0
	}
	last2 := s[len(s)-2:] // 取最后两位字符串

	n, err := strconv.Atoi(last2)
	if err != nil {
		return 0 // 如果不是数字，返回0（你也可以选择返回-1表示错误）
	}
	return n % 100
}

// 消息类型
type MessageType string

const (
	MsgPullQuery        = "MsgPullQuery"
	MsgPushQuery        = "MsgPushQuery"
	MsgChits            = "MsgChits"
	MsgGet              = "MsgGet" // 请求区块数据
	MsgPut              = "MsgPut" // 发送区块数据
	MsgGossip           = "MsgGossip"
	MsgSyncRequest      = "MsgSyncRequest"
	MsgSyncResponse     = "MsgSyncResponse"
	MsgHeightQuery      = "MsgHeightQuery"
	MsgHeightResponse   = "MsgHeightResponse"
	MsgSnapshotRequest  = "MsgSnapshotRequest"  // 请求快照
	MsgSnapshotResponse = "MsgSnapshotResponse" // 快照响应
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
