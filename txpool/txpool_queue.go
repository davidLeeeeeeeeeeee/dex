package txpool

import (
	"dex/config"
	"dex/db"
	"dex/interfaces"
	"dex/logs"
	"dex/network"
	"dex/utils"
	"fmt"
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
	dbMgr     interfaces.DBManager
	msgChan   chan *TxPoolMessage
	stopChan  chan struct{}
	wg        sync.WaitGroup
	validator TxValidator
	pool      *TxPool          // 不再依赖单例，而是作为成员
	network   *network.Network // 网络管理器也作为成员
	started   bool             // 标记是否已启动
	mu        sync.Mutex       // 保护 started 状态
}

func NewTxPoolQueue(dbMgr interfaces.DBManager, validator TxValidator, pool *TxPool, cfg config.NetworkConfig) (*TxPoolQueue, error) {
	if dbMgr == nil {
		return nil, fmt.Errorf("dbMgr cannot be nil")
	}
	if validator == nil {
		return nil, fmt.Errorf("validator cannot be nil")
	}
	if pool == nil {
		return nil, fmt.Errorf("pool cannot be nil")
	}

	// 正确处理 NewNetwork 的返回值
	networkInstance, err := network.NewNetwork(cfg, dbMgr)
	if err != nil {
		return nil, fmt.Errorf("failed to create network: %w", err)
	}

	tq := &TxPoolQueue{
		dbMgr:     dbMgr,
		validator: validator,
		pool:      pool,
		network:   networkInstance, // 使用返回的实例
		msgChan:   make(chan *TxPoolMessage, 1000),
		stopChan:  make(chan struct{}),
		started:   false,
	}

	return tq, nil
}

// Start 启动队列处理
func (tq *TxPoolQueue) Start() error {
	tq.mu.Lock()
	defer tq.mu.Unlock()

	if tq.started {
		return fmt.Errorf("TxPoolQueue is already started")
	}

	tq.wg.Add(1)
	go tq.runLoop()
	tq.started = true
	log.Println("[TxPoolQueue] started.")

	return nil
}

// Stop 停止队列处理
func (tq *TxPoolQueue) Stop() {
	tq.mu.Lock()
	if !tq.started {
		tq.mu.Unlock()
		return
	}
	tq.started = false
	tq.mu.Unlock()

	close(tq.stopChan)
	tq.wg.Wait()
	log.Println("[TxPoolQueue] stopped.")
}

// Enqueue 将消息加入队列
func (tq *TxPoolQueue) Enqueue(msg *TxPoolMessage) error {
	tq.mu.Lock()
	defer tq.mu.Unlock()

	if !tq.started {
		return fmt.Errorf("TxPoolQueue is not started")
	}

	select {
	case tq.msgChan <- msg:
		return nil
	default:
		return fmt.Errorf("TxPoolQueue is full")
	}
}

// EnqueueBlocking 阻塞式地将消息加入队列
func (tq *TxPoolQueue) EnqueueBlocking(msg *TxPoolMessage) error {
	tq.mu.Lock()
	if !tq.started {
		tq.mu.Unlock()
		return fmt.Errorf("TxPoolQueue is not started")
	}
	tq.mu.Unlock()

	tq.msgChan <- msg
	return nil
}

func (tq *TxPoolQueue) runLoop() {
	defer tq.wg.Done()

	for {
		select {
		case <-tq.stopChan:
			// 处理剩余的消息后退出
			tq.drainMessages()
			return
		case msg := <-tq.msgChan:
			if msg == nil {
				continue
			}
			tq.processMessage(msg)
		}
	}
}

// drainMessages 在停止前处理剩余的消息
func (tq *TxPoolQueue) drainMessages() {
	for {
		select {
		case msg := <-tq.msgChan:
			if msg != nil {
				tq.processMessage(msg)
			}
		default:
			return
		}
	}
}

// processMessage 处理单个消息
func (tq *TxPoolQueue) processMessage(msg *TxPoolMessage) {
	switch msg.Type {
	case MsgAddTx:
		if msg.AnyTx == nil {
			log.Println("[TxPoolQueue] MsgAddTx but AnyTx is nil, skip")
			return
		}
		tq.handleAddTx(msg.AnyTx, msg.IP, msg.OnAdded)

	case MsgRemoveTx:
		if msg.AnyTx == nil {
			log.Println("[TxPoolQueue] MsgRemoveTx but AnyTx is nil, skip")
			return
		}
		txID := msg.AnyTx.GetTxId()
		tq.pool.RemoveAnyTx(txID)
		logs.Debug("[TxPoolQueue] removed tx=%s from TxPool.\n", txID)

	default:
		logs.Debug("[TxPoolQueue] unknown msg type: %d\n", msg.Type)
	}
}

// handleAddTx 处理"新增交易"逻辑，包含 known / unknown 流程
func (tq *TxPoolQueue) handleAddTx(incoming *db.AnyTx, ip string, onAdded OnTxAddedCallback) {
	txID := incoming.GetTxId()
	if txID == "" {
		log.Println("[TxPoolQueue] AddTx but no txID, skip.")
		return
	}

	// 1. 检查 TxPool 是否已经有该交易
	if tq.pool.HasTransaction(txID) {
		//logs.Debug("[TxPoolQueue] tx=%s already in TxPool, skip.\n", txID)
		return
	}

	// 2. 分析来源节点(假设我们在 AnyTx 的BaseMessage或别处携带了 fromPubKey / fromIP)
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
			isKnown = tq.network.IsKnownNode(pubKeyStr)
		}
	}

	// 3. known / unknown 流程
	if isKnown {
		// 已知节点：做法 => (1)广播 => (2)异步校验 => (3)若合法则存TxPool
		// 3.1 先广播(SendInvToAllPeers)
		if onAdded != nil {
			onAdded(txID)
		}

		// 3.2 异步校验 & 入池
		go func(tx *db.AnyTx) {
			if err := tq.validator.CheckAnyTx(tx); err != nil {
				//logs.Debug("[TxPoolQueue] known node, but invalid tx=%s, err=%v\n", txID, err)
				return
			}
			// 校验通过 => 存TxPool
			if err := tq.pool.StoreAnyTx(tx); err != nil {
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
				tq.network.AddOrUpdateNode(pubKeyStr, ip, true)
			}
		}

		// 入池
		if err := tq.pool.StoreAnyTx(incoming); err != nil {
			logs.Debug("[TxPoolQueue] unknown node => store tx=%s fail: %v", txID, err)
			return
		}
		logs.Debug("[TxPoolQueue] unknown node => store tx=%s ok.\n", txID)

		// 最后再广播
		if onAdded != nil {
			onAdded(txID)
		}
	}
}

// GetQueueLength 获取当前队列长度（用于监控）
func (tq *TxPoolQueue) GetQueueLength() int {
	return len(tq.msgChan)
}

// IsStarted 检查队列是否已启动
func (tq *TxPoolQueue) IsStarted() bool {
	tq.mu.Lock()
	defer tq.mu.Unlock()
	return tq.started
}
