package txpool

import (
	"dex/pb"
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
	AnyTx   *pb.AnyTx
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
	CheckAnyTx(tx *pb.AnyTx) error
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
	// 不需要 SetThreadNodeContext，直接使用 tq.pool.Logger

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
				tq.pool.Logger.Debug("[TxPoolQueue] removed tx=%s from TxPool", txID)
			default:
				tq.pool.Logger.Debug("[TxPoolQueue] unknown msg type: %d", msg.Type)
			}
		}
	}
}

func (tq *txPoolQueue) handleAddTx(incoming *pb.AnyTx, ip string, onAdded OnTxAddedCallback) {
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
		tq.pool.Logger.Debug("[TxPoolQueue] missing BaseMessage, skip.")
		return
	}

	// 从 PublicKeys 中获取 ECDSA_P256 公钥
	var pubKeyPem string
	if base.PublicKeys != nil {
		if pk, ok := base.PublicKeys.Keys[int32(pb.SignAlgo_SIGN_ALGO_ECDSA_P256)]; ok {
			pubKeyPem = string(pk)
		}
	}

	// 检查是否为已知节点
	isKnown := false
	if pubKeyPem != "" {
		if pubKeyObj, err := utils.DecodePublicKey(pubKeyPem); err == nil {
			pubKeyStr := utils.ExtractPublicKeyString(pubKeyObj)
			isKnown = tq.pool.network.IsKnownNode(pubKeyStr)
		}
	}

	if isKnown {
		// 已知节点：先验证并入池，再触发广播回调，避免“先广播后入池”造成的重复广播窗口
		if err := tq.validator.CheckAnyTx(incoming); err != nil {
			tq.pool.Logger.Debug("[TxPoolQueue] known node, tx=%s invalid: %v", txID, err)
			return
		}
		if err := tq.pool.storeAnyTx(incoming); err != nil {
			tq.pool.Logger.Debug("[TxPoolQueue] known node => store tx=%s fail: %v", txID, err)
			return
		}
		if onAdded != nil {
			onAdded(txID)
		}
	} else {
		// 未知节点：先验证后广播
		if err := tq.validator.CheckAnyTx(incoming); err != nil {
			tq.pool.Logger.Debug("[TxPoolQueue] unknown node, tx=%s invalid: %v", txID, err)
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
			tq.pool.Logger.Debug("[TxPoolQueue] unknown node => store tx=%s fail: %v", txID, err)
			return
		}

		// 最后广播
		if onAdded != nil {
			onAdded(txID)
		}
	}
}
