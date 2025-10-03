package txpool

import (
	"dex/db"
	"dex/logs"
	"dex/utils"
	"log"
)

// 内部消息类型
type txMsgType int

const (
	msgAddTx txMsgType = iota
	msgRemoveTx
)

// 回调函数类型：在交易真正加入TxPool之后，是否要做额外操作
type OnTxAddedCallback func(txID string)

// TxPoolMessage 封装了要在 txpool 里处理的各种任务
type txPoolMessage struct {
	Type    txMsgType
	AnyTx   *db.AnyTx
	IP      string
	OnAdded OnTxAddedCallback
}

type txPoolQueue struct {
	pool      *TxPool
	validator TxValidator
	MsgChan   chan *txPoolMessage
}

// TxValidator 用于抽象交易校验
type TxValidator interface {
	CheckAnyTx(tx *db.AnyTx) error
}

// NewTxPoolQueue 创建新的队列实例
func newTxPoolQueue(pool *TxPool, validator TxValidator) *txPoolQueue {
	return &txPoolQueue{
		pool:      pool,
		validator: validator,
		MsgChan:   make(chan *txPoolMessage, 10000),
	}
}

func (tq *txPoolQueue) runLoop() {
	defer tq.pool.wg.Done()

	for {
		select {
		case <-tq.pool.stopChan:
			return
		case msg := <-tq.MsgChan:
			if msg == nil {
				continue
			}
			switch msg.Type {
			case msgAddTx:
				tq.handleAddTx(msg.AnyTx, msg.IP, msg.OnAdded)
			case msgRemoveTx:
				txID := msg.AnyTx.GetTxId()
				tq.pool.RemoveTx(txID)
				logs.Debug("[TxPoolQueue] removed tx=%s from TxPool", txID)
			default:
				logs.Debug("[TxPoolQueue] unknown msg type: %d", msg.Type)
			}
		}
	}
}

func (tq *txPoolQueue) handleAddTx(incoming *db.AnyTx, ip string, onAdded OnTxAddedCallback) {
	txID := incoming.GetTxId()
	if txID == "" {
		log.Println("[TxPoolQueue] AddTx but no txID, skip.")
		return
	}

	if tq.pool.HasTransaction(txID) {
		return
	}

	base := incoming.GetBase()
	if base == nil {
		logs.Debug("[TxPoolQueue] missing BaseMessage, skip.")
		return
	}

	pubKeyPem := base.PublicKey

	// 检查是否为已知节点
	isKnown := false
	if pubKeyPem != "" {
		if pubKeyObj, err := utils.DecodePublicKey(pubKeyPem); err == nil {
			pubKeyStr := utils.ExtractPublicKeyString(pubKeyObj)
			isKnown = tq.pool.network.IsKnownNode(pubKeyStr)
		}
	}

	if isKnown {
		// 已知节点：先广播后验证
		if onAdded != nil {
			onAdded(txID)
		}

		// 异步校验 & 入池
		go func(tx *db.AnyTx) {
			if err := tq.validator.CheckAnyTx(tx); err != nil {
				return
			}
			if err := tq.pool.storeAnyTx(tx); err != nil {
				logs.Debug("[TxPoolQueue] StoreAnyTx fail: %v", err)
			}
		}(incoming)

	} else {
		// 未知节点：先验证后广播
		if err := tq.validator.CheckAnyTx(incoming); err != nil {
			logs.Debug("[TxPoolQueue] unknown node, tx=%s invalid: %v", txID, err)
			return
		}

		// 更新网络节点信息
		if pubKeyPem != "" {
			if pubKeyObj, err := utils.DecodePublicKey(pubKeyPem); err == nil {
				pubKeyStr := utils.ExtractPublicKeyString(pubKeyObj)
				tq.pool.network.AddOrUpdateNode(pubKeyStr, ip, true)
			}
		}

		// 入池
		if err := tq.pool.storeAnyTx(incoming); err != nil {
			logs.Debug("[TxPoolQueue] unknown node => store tx=%s fail: %v", txID, err)
			return
		}

		// 最后广播
		if onAdded != nil {
			onAdded(txID)
		}
	}
}
