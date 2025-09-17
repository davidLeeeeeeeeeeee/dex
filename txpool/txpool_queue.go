package txpool

import (
	"awesomeProject1/db"
	"awesomeProject1/internal/network"
	"awesomeProject1/logs"
	"awesomeProject1/utils"
	"log"
	"sync"
)

// TxMsgType 枚举要处理的消息类型
type TxMsgType int

const (
	MsgAddTx TxMsgType = iota // 新增交易
	MsgRemoveTx
	// 你可以继续扩展更多类型
)

// 回调函数类型：在交易真正加入TxPool之后，是否要做额外操作
type OnTxAddedCallback func(txID string)

// TxPoolMessage 封装了要在 txpool 里处理的各种任务
type TxPoolMessage struct {
	Type    TxMsgType
	AnyTx   *db.AnyTx
	IP      string
	OnAdded OnTxAddedCallback
}

type TxPoolQueue struct {
	dbMgr     *db.Manager
	msgChan   chan *TxPoolMessage
	stopChan  chan struct{}
	wg        sync.WaitGroup
	validator TxValidator
}

// TxValidator 用于抽象交易校验
type TxValidator interface {
	CheckAnyTx(tx *db.AnyTx) error
}

// 全局队列实例（在项目启动时初始化一次）
var GlobalTxPoolQueue *TxPoolQueue

// InitTxPoolQueue 在程序启动时被调用
func InitTxPoolQueue(dbMgr *db.Manager, validator TxValidator) {
	GlobalTxPoolQueue = &TxPoolQueue{
		dbMgr:     dbMgr,
		validator: validator,
		msgChan:   make(chan *TxPoolMessage, 1000),
		stopChan:  make(chan struct{}),
	}
	GlobalTxPoolQueue.start()
}

func (tq *TxPoolQueue) start() {
	tq.wg.Add(1)
	go tq.runLoop()
	log.Println("[TxPoolQueue] started.")
}

func (tq *TxPoolQueue) Stop() {
	close(tq.stopChan)
	tq.wg.Wait()
	log.Println("[TxPoolQueue] stopped.")
}

func (tq *TxPoolQueue) Enqueue(msg *TxPoolMessage) {
	tq.msgChan <- msg
}

func (tq *TxPoolQueue) runLoop() {
	defer tq.wg.Done()

	pool := GetInstance()                   // 获取 TxPool 单例
	netInst := network.NewNetwork(tq.dbMgr) // 获取 network 管理器（可改成全局单例）

	for {
		select {
		case <-tq.stopChan:
			return
		case msg := <-tq.msgChan:
			if msg == nil {
				continue
			}
			switch msg.Type {
			case MsgAddTx:
				if msg.AnyTx == nil {
					log.Println("[TxPoolQueue] MsgAddTx but AnyTx is nil, skip")
					continue
				}
				// 把逻辑拆出去，便于阅读
				tq.handleAddTx(msg.AnyTx, pool, msg.IP, msg.OnAdded, netInst)

			case MsgRemoveTx:
				if msg.AnyTx == nil {
					log.Println("[TxPoolQueue] MsgRemoveTx but AnyTx is nil, skip")
					continue
				}
				txID := msg.AnyTx.GetTxId()
				pool.RemoveAnyTx(txID)
				logs.Debug("[TxPoolQueue] removed tx=%s from TxPool.\n", txID)

			default:
				logs.Debug("[TxPoolQueue] unknown msg type: %d\n", msg.Type)
			}
		}
	}
}

// handleAddTx 处理“新增交易”逻辑，包含 known / unknown 流程
func (tq *TxPoolQueue) handleAddTx(incoming *db.AnyTx,
	pool *TxPool,
	Ip string,
	OnAdded OnTxAddedCallback,
	netInst *network.Network) {

	txID := incoming.GetTxId()
	if txID == "" {
		log.Println("[TxPoolQueue] AddTx but no txID, skip.")
		return
	}

	// 1. 检查  TxPool 是否已经有该交易

	if pool.HasTransaction(txID) {
		//logs.Debug("[TxPoolQueue] tx=%s already in TxPool, skip.\n", txID)
		return
	}

	// 2. 分析来源节点(假设我们在 AnyTx 的BaseMessage或别处携带了 fromPubKey / fromIP)
	//    这里只是示例：假设 baseMessage.PublicKey 存了PEM格式
	base := incoming.GetBase()
	if base == nil {
		logs.Debug("[TxPoolQueue] missing BaseMessage, skip.")
		return
	}
	pubKeyPem := base.PublicKey

	// 2.1 解出 pubKey => check known or not
	isKnown := false
	if pubKeyPem != "" {
		if pubKeyObj, err := utils.DecodePublicKey(pubKeyPem); err == nil {
			pubKeyStr := utils.ExtractPublicKeyString(pubKeyObj)
			// 用 network.IsKnownNode(pubKeyStr) 来判断
			isKnown = netInst.IsKnownNode(pubKeyStr)
		}
	}

	// 3. known / unknown 流程
	if isKnown {
		// 已知节点：做法 => (1)广播 => (2)异步校验 => (3)若合法则存TxPool
		// 3.1 先广播(SendInvToAllPeers)
		if OnAdded != nil {
			OnAdded(txID)
		}

		// 3.2 异步校验 & 入池
		go func(tx *db.AnyTx) {
			if err := tq.validator.CheckAnyTx(tx); err != nil {
				//logs.Debug("[TxPoolQueue] known node, but invalid tx=%s, err=%v\n", txID, err)
				return
			}
			// 校验通过 => 存TxPool
			if err := pool.StoreAnyTx(tx); err != nil {
				logs.Debug("[TxPoolQueue] StoreAnyTx fail: %v", err)
			} else {
				//logs.Debug("[TxPoolQueue] known node => store tx=%s ok.\n", txID)
			}
		}(incoming)

	} else {
		// 未知节点：做法 => (1)先校验 => (2)若合法则更新network并写TxPool => (3)最后再广播
		if err := tq.validator.CheckAnyTx(incoming); err != nil {
			logs.Debug("[TxPoolQueue] unknown node, tx=%s invalid: %v\n", txID, err)
			return
		}

		if pubKeyPem != "" {
			if pubKeyObj, err := utils.DecodePublicKey(pubKeyPem); err == nil {
				pubKeyStr := utils.ExtractPublicKeyString(pubKeyObj)
				// fromAddr 可能只是链上地址，不是IP => 你可以在 msg.AnyTx 加一个 extra: PeerIP
				netInst.AddOrUpdateNode(pubKeyStr, Ip, true)
			}
		}

		// 入池
		if err := pool.StoreAnyTx(incoming); err != nil {
			logs.Debug("[TxPoolQueue] unknown node => store tx=%s fail: %v", txID, err)
			return
		}
		logs.Debug("[TxPoolQueue] unknown node => store tx=%s ok.\n", txID)

		// 最后再广播
		// 第二步：如果有回调，就调用
		if OnAdded != nil {
			OnAdded(txID)
		}
	}
}
