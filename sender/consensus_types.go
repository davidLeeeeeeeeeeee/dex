package sender

import (
	"dex/db"
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
	onSuccess   func(*db.HeightResponse)
}

// syncRequestMessage 用于同步请求
type syncRequestMessage struct {
	requestData []byte
	fromHeight  uint64
	toHeight    uint64
	onSuccess   func([]*db.Block)
}
