package vm

import (
	"encoding/json"
	"fmt"
)

// ========== 示例Handler实现 ==========

// OrderTxHandler 订单交易处理器示例
type OrderTxHandler struct {
	// 可以注入其他依赖，如OrderBookManager等
}

func (h *OrderTxHandler) Kind() string {
	return "order"
}

func (h *OrderTxHandler) DryRun(tx *AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 1. 解析交易数据
	type OrderTx struct {
		FromAddress string `json:"from_address"`
		Symbol      string `json:"symbol"`
		Side        string `json:"side"` // "buy" or "sell"
		Price       string `json:"price"`
		Amount      string `json:"amount"`
	}

	var ord OrderTx
	if err := json.Unmarshal(tx.Payload, &ord); err != nil {
		return nil, &Receipt{
			TxID:   tx.TxID,
			Status: "FAILED",
			Error:  "bad payload",
		}, err
	}

	// 2. 验证交易（余额、权限等）
	accountKey := fmt.Sprintf("account_%s", ord.FromAddress)
	accountData, exists, err := sv.Get(accountKey)
	if err != nil {
		return nil, &Receipt{
			TxID:   tx.TxID,
			Status: "FAILED",
			Error:  "read account failed",
		}, err
	}

	if !exists {
		return nil, &Receipt{
			TxID:   tx.TxID,
			Status: "FAILED",
			Error:  "account not found",
		}, fmt.Errorf("account not found: %s", ord.FromAddress)
	}

	// 3. 执行交易逻辑，生成状态变更
	ws := make([]WriteOp, 0)

	// 示例：创建订单记录
	orderKey := fmt.Sprintf("order_%s", tx.TxID)
	orderData, _ := json.Marshal(ord)
	ws = append(ws, WriteOp{
		Key:   orderKey,
		Value: orderData,
		Del:   false,
	})

	// 更新账户状态（这里应该解析accountData，修改后再序列化）
	ws = append(ws, WriteOp{
		Key:   accountKey,
		Value: accountData, // 实际应该是修改后的数据
		Del:   false,
	})

	// 4. 返回执行结果
	rc := &Receipt{
		TxID:       tx.TxID,
		Status:     "SUCCEED",
		WriteCount: len(ws),
	}

	return ws, rc, nil
}

func (h *OrderTxHandler) Apply(tx *AnyTx) error {
	// 兜底实现，通常不需要
	return ErrNotImplemented
}

// TransferTxHandler 转账交易处理器
type TransferTxHandler struct{}

func (h *TransferTxHandler) Kind() string {
	return "transfer"
}

func (h *TransferTxHandler) DryRun(tx *AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	type TransferTx struct {
		From   string `json:"from"`
		To     string `json:"to"`
		Amount string `json:"amount"`
		Token  string `json:"token"`
	}

	var transfer TransferTx
	if err := json.Unmarshal(tx.Payload, &transfer); err != nil {
		return nil, &Receipt{
			TxID:   tx.TxID,
			Status: "FAILED",
			Error:  "bad payload",
		}, err
	}

	// 读取发送方账户
	fromKey := fmt.Sprintf("balance_%s_%s", transfer.From, transfer.Token)
	fromData, exists, err := sv.Get(fromKey)
	if err != nil || !exists {
		return nil, &Receipt{
			TxID:   tx.TxID,
			Status: "FAILED",
			Error:  "from account not found",
		}, fmt.Errorf("from account not found")
	}

	// 读取接收方账户
	toKey := fmt.Sprintf("balance_%s_%s", transfer.To, transfer.Token)
	toData, _, _ := sv.Get(toKey)
	if toData == nil {
		toData = []byte("0") // 初始化接收方余额
	}

	// TODO: 实际应该解析账户数据，检查余额，执行转账
	// 这里简化处理

	// 更新两个账户的状态
	ws := []WriteOp{
		{Key: fromKey, Value: fromData, Del: false}, // 应该是减少余额后的数据
		{Key: toKey, Value: toData, Del: false},     // 应该是增加余额后的数据
	}

	// 记录转账历史
	historyKey := fmt.Sprintf("transfer_history_%s", tx.TxID)
	historyData, _ := json.Marshal(transfer)
	ws = append(ws, WriteOp{
		Key:   historyKey,
		Value: historyData,
		Del:   false,
	})

	return ws, &Receipt{
		TxID:       tx.TxID,
		Status:     "SUCCEED",
		WriteCount: len(ws),
	}, nil
}

func (h *TransferTxHandler) Apply(tx *AnyTx) error {
	return ErrNotImplemented
}

// MinerTxHandler 矿工交易处理器
type MinerTxHandler struct{}

func (h *MinerTxHandler) Kind() string {
	return "miner"
}

func (h *MinerTxHandler) DryRun(tx *AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	type MinerTx struct {
		Miner  string `json:"miner"`
		Reward string `json:"reward"`
		Height uint64 `json:"height"`
	}

	var minerTx MinerTx
	if err := json.Unmarshal(tx.Payload, &minerTx); err != nil {
		return nil, &Receipt{
			TxID:   tx.TxID,
			Status: "FAILED",
			Error:  "bad payload",
		}, err
	}

	// 更新矿工余额
	minerBalanceKey := fmt.Sprintf("balance_%s_native", minerTx.Miner)
	balanceData, _, _ := sv.Get(minerBalanceKey)
	if balanceData == nil {
		balanceData = []byte("0")
	}

	// TODO: 实际应该增加奖励到余额

	ws := []WriteOp{
		{Key: minerBalanceKey, Value: balanceData, Del: false},
	}

	// 记录挖矿历史
	minerHistoryKey := fmt.Sprintf("miner_history_%d", minerTx.Height)
	minerData, _ := json.Marshal(minerTx)
	ws = append(ws, WriteOp{
		Key:   minerHistoryKey,
		Value: minerData,
		Del:   false,
	})

	rc := &Receipt{
		TxID:       tx.TxID,
		Status:     "SUCCEED",
		WriteCount: len(ws),
	}

	return ws, rc, nil
}

func (h *MinerTxHandler) Apply(tx *AnyTx) error {
	return ErrNotImplemented
}

// RegisterDefaultHandlers 注册默认处理器
func RegisterDefaultHandlers(reg *HandlerRegistry) error {
	handlers := []TxHandler{
		&OrderTxHandler{},
		&TransferTxHandler{},
		&MinerTxHandler{},
	}

	for _, h := range handlers {
		if err := reg.Register(h); err != nil {
			return err
		}
	}

	return nil
}
