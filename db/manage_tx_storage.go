package db

import (
	"dex/utils"
	"fmt"

	"github.com/shopspring/decimal"
)

// ------------- 基础交易 -------------
func (mgr *Manager) SaveTransaction(tx *Transaction) error {
	//logs.Trace("SaveTransaction %s\n", tx)
	key := KeyTx(tx.Base.TxId)
	data, err := ProtoMarshal(tx)
	if err != nil {
		return err
	}
	mgr.EnqueueSet(key, string(data))
	// 如果是PENDING
	if tx.Base.Status == Status_PENDING {
		pendingKey := KeyPendingAnyTx(tx.Base.TxId)
		mgr.EnqueueSet(pendingKey, string(data))
	}
	// 2. 同时把它封装进 AnyTx 并存 "anyTx_<txid>"
	mgr.EnqueueSet(KeyAnyTx(tx.Base.TxId), key)
	return nil
}

func (mgr *Manager) GetTransaction(txID string) (*Transaction, error) {
	key := KeyTx(txID)
	val, err := mgr.Read(key)
	if err != nil {
		return nil, err
	}
	tx := &Transaction{}
	if err := ProtoUnmarshal([]byte(val), tx); err != nil {
		return nil, err
	}
	return tx, nil
}

// ------------- OrderTx -------------

func (mgr *Manager) SaveOrderTx(order *OrderTx) error {
	// 1. 先拿到 pairKey
	pairKey := utils.GeneratePairKey(order.BaseToken, order.QuoteToken)

	// 2. 把 price 转成 stringKey
	priceKey, err := PriceToKey128(order.Price)
	if err != nil {
		return err
	}

	// 3. 构造索引key
	//    例如: "pair:BTC_USDT|price:000000000123123|order_id:..."
	indexKey := KeyOrderPriceIndex(pairKey, order.IsFilled, priceKey, order.Base.TxId)

	// 4. 存储 (跟你现在的逻辑一样，只是把 "base_token_base_quote" 替换成 pairKey)
	data, err := ProtoMarshal(order)
	if err != nil {
		return err
	}
	mgr.EnqueueSet(indexKey, string(data))

	// 存储原始订单
	orderKey := KeyOrderTx(order.Base.TxId)
	mgr.EnqueueSet(orderKey, string(data))
	// 5. 同时把它封装进 AnyTx 并存 "anyTx_<txid>"
	mgr.EnqueueSet(KeyAnyTx(order.Base.TxId), orderKey)
	return nil
}

func (mgr *Manager) GetOrderTx(txID string) (*OrderTx, error) {
	key := KeyOrderTx(txID)
	val, err := mgr.Read(key)
	if err != nil {
		return nil, err
	}
	order := &OrderTx{}
	if err := ProtoUnmarshal([]byte(val), order); err != nil {
		return nil, err
	}
	return order, nil
}

func (mgr *Manager) SaveMinerTx(tx *MinerTx) error {
	// 0) 先把 MinerTx 本身排入写队列（保持你原来的逻辑）
	mainKey := KeyMinerTx(tx.Base.TxId)
	data, err := ProtoMarshal(tx)
	if err != nil {
		return err
	}
	mgr.EnqueueSet(mainKey, string(data))
	mgr.EnqueueSet(KeyAnyTx(tx.Base.TxId), mainKey)

	// 1) 读取 / 初始化账户
	addr := tx.Base.FromAddress
	acc, err := mgr.GetAccount(addr)
	if err != nil {
		return err
	}
	//logs.Trace("acc.FB=%s", acc.Balances["FB"])
	// 确保 FB 余额存在
	fb, ok := acc.Balances["FB"]
	if !ok {
		fb = NewZeroTokenBalance()
		acc.Balances["FB"] = fb
	}

	// 2) 金额解析（tx.Amount 可能为空，REMOVE 时可忽略）
	amt := decimal.Zero
	if tx.Amount != "" {
		amt, err = decimal.NewFromString(tx.Amount)
		if err != nil {
			return fmt.Errorf("invalid MinerTx amount=%q: %v", tx.Amount, err)
		}
	}

	switch tx.Op {
	case OrderOp_ADD:
		// (ADD) 2-a 判断是否已是矿工
		if acc.IsMiner {
			// 已是矿工 → 只增加质押
			prevLocked, _ := decimal.NewFromString(fb.MinerLockedBalance)
			fb.MinerLockedBalance = prevLocked.Add(amt).String()
		} else {
			// 不是矿工 → 分配 index & 置 is_miner=true
			idx, tasks, err := getNewIndex(mgr)
			if err != nil {
				return err
			}
			// 把 getNewIndex 生成的元数据写任务排进队列
			for _, w := range tasks {
				mgr.writeQueueChan <- w
			}
			acc.Index = idx
			acc.IsMiner = true

			prevLocked, _ := decimal.NewFromString(fb.MinerLockedBalance)
			fb.MinerLockedBalance = prevLocked.Add(amt).String()
			// 存入indexToAccount
			indexToAccount := KeyAccount(acc.Address)
			mgr.EnqueueSet(KeyIndexToAccount(idx), indexToAccount)
			mgr.IndexMgr.Add(idx) //内存维护在线矿工索引
		}

		// 从可用余额扣除
		prevBal, _ := decimal.NewFromString(fb.Balance)
		fb.Balance = prevBal.Sub(amt).String()
		//logs.Trace("fb.Balance =%s amt=%s idx=%s", fb.Balance, amt, acc.Index)

	case OrderOp_REMOVE:
		// (REMOVE) 回退锁仓 + 取消矿工标记 + 释放 index
		// 1) 回退余额
		prevLocked, _ := decimal.NewFromString(fb.MinerLockedBalance)
		prevBal, _ := decimal.NewFromString(fb.Balance)
		fb.Balance = prevBal.Add(prevLocked).String()
		fb.MinerLockedBalance = "0"

		// 2) 取消矿工身份
		acc.IsMiner = false

		// 3) 回收 index (tx.Index 必须在上层填好)
		mgr.writeQueueChan <- removeIndex(acc.Index)
		// 4) 回收indexToAccount_
		mgr.EnqueueDelete(KeyIndexToAccount(acc.Index))
		mgr.IndexMgr.Remove(acc.Index)

	default:
		return fmt.Errorf("unknown MinerTx op=%v", tx.Op)
	}

	// 3) 把更新后的账户写回
	if err := mgr.SaveAccount(acc); err != nil {
		return err
	}

	return nil
}

// GetAnyTxById 根据给定的 tx_id 从数据库中读取对应的交易（AnyTx）
// 这里假设在保存时，除了专用前缀外，还额外保存了一个通用 key "anyTx_<txID>"
// 其值为实际存储该交易的 key（如 "tx_<txID>" 或 "order_<txID>" 等）。
func (mgr *Manager) GetAnyTxById(txID string) (*AnyTx, error) {
	// 1. 先读取通用 key "anyTx_<txID>"
	anyKey := KeyAnyTx(txID)
	specificKey, err := mgr.Read(anyKey)
	if err != nil {
		return nil, fmt.Errorf("failed to read anyTx key %s: %v", anyKey, err)
	}
	if specificKey == "" {
		return nil, fmt.Errorf("no anyTx record for txID %s", txID)
	}

	// 2. 根据专用 key读取实际交易数据
	txData, err := mgr.Read(specificKey)
	if err != nil {
		return nil, fmt.Errorf("failed to read transaction data for key %s: %v", specificKey, err)
	}
	if txData == "" {
		return nil, fmt.Errorf("empty transaction data for key %s", specificKey)
	}

	// 3. 反序列化为 AnyTx 对象
	var anyTx AnyTx
	if err := ProtoUnmarshal([]byte(txData), &anyTx); err != nil {
		return nil, fmt.Errorf("failed to unmarshal AnyTx data: %v", err)
	}
	return &anyTx, nil
}
