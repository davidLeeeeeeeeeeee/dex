package sender

import (
	"dex/pb"
	"dex/types"
)

// ============= Message Types =============

// chitsMessage 用于发送Chits响应
type chitsMessage struct {
	requestData []byte
}

// blockMessage 用于发送完整区块
type blockMessage struct {
	requestData []byte
}

// heightQueryMessage 用于发送高度查询
type heightQueryMessage struct {
	requestData []byte
	onSuccess   func(*pb.HeightResponse)
}

// syncRequestMessage 用于同步请求
type syncRequestMessage struct {
	requestData         []byte
	fromHeight          uint64
	toHeight            uint64
	syncShortMode       bool
	onSuccess           func([]*pb.Block)
	onSuccessWithProofs func(*types.SyncBlocksResponse)
}

// pushQueryMsg 用于发送 PushQuery
type pushQueryMsg struct {
	requestData []byte
}

// pullQueryMsg 用于发送 PullQuery
type pullQueryMsg struct {
	requestData []byte
}

// pullBlockMessage 用于按高度拉取区块
type pullBlockMessage struct {
	requestData []byte
	onSuccess   func(*pb.Block)
}

// pullBatchTxMessage 用于批量拉取交易
type pullBatchTxMessage struct {
	requestData []byte
	onSuccess   func([]*pb.AnyTx)
}
