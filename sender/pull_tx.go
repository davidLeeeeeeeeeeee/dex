package sender

import (
	"dex/db"
	"dex/txpool"
)

// 用来包装“拉取请求的[]byte + 回调函数”
type pullTxMessage struct {
	requestData []byte
	onSuccess   func(*db.AnyTx)
	txPool      *txpool.TxPool
}
