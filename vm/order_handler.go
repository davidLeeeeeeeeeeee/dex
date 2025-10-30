package vm

import (
	"dex/pb"
	"encoding/json"
	"fmt"
	"math/big"
)

// OrderTxHandler 订单交易处理器
type OrderTxHandler struct {
	// 可以注入其他依赖，如OrderBookManager等
}

func (h *OrderTxHandler) Kind() string {
	return "order"
}

func (h *OrderTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 1. 提取OrderTx
	orderTx, ok := tx.GetContent().(*pb.AnyTx_OrderTx)
	if !ok {
		return nil, &Receipt{
			TxID:   tx.GetTxId(),
			Status: "FAILED",
			Error:  "not an order transaction",
		}, fmt.Errorf("not an order transaction")
	}

	ord := orderTx.OrderTx
	if ord == nil || ord.Base == nil {
		return nil, &Receipt{
			TxID:   tx.GetTxId(),
			Status: "FAILED",
			Error:  "invalid order transaction",
		}, fmt.Errorf("invalid order transaction")
	}

	// 2. 根据操作类型分发处理
	switch ord.Op {
	case pb.OrderOp_ADD:
		return h.handleAddOrder(ord, sv)
	case pb.OrderOp_REMOVE:
		return h.handleRemoveOrder(ord, sv)
	default:
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "unknown order operation",
		}, fmt.Errorf("unknown order operation: %v", ord.Op)
	}
}

// handleAddOrder 处理添加/更新订单
func (h *OrderTxHandler) handleAddOrder(ord *pb.OrderTx, sv StateView) ([]WriteOp, *Receipt, error) {
	// 验证订单参数
	amount, ok := new(big.Int).SetString(ord.Amount, 10)
	if !ok || amount.Sign() <= 0 {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "invalid order amount",
		}, fmt.Errorf("invalid order amount: %s", ord.Amount)
	}

	price, ok := new(big.Int).SetString(ord.Price, 10)
	if !ok || price.Sign() <= 0 {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "invalid order price",
		}, fmt.Errorf("invalid order price: %s", ord.Price)
	}

	// 读取账户
	accountKey := fmt.Sprintf("account_%s", ord.Base.FromAddress)
	accountData, exists, err := sv.Get(accountKey)
	if err != nil || !exists {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "account not found",
		}, fmt.Errorf("account not found: %s", ord.Base.FromAddress)
	}

	var account pb.Account
	if err := json.Unmarshal(accountData, &account); err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse account",
		}, err
	}

	// 检查base_token余额（买单需要锁定base_token，即支付token）
	if account.Balances == nil || account.Balances[ord.BaseToken] == nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "insufficient base token balance",
		}, fmt.Errorf("no balance for base token: %s", ord.BaseToken)
	}

	// 计算需要锁定的金额（买单：amount * price，卖单：amount）
	// 这里简化处理，实际需要根据买卖方向判断
	// 假设：买BTC用USDT，则base_token=USDT, quote_token=BTC
	// 买单需要锁定 amount * price 的 base_token

	ws := make([]WriteOp, 0)

	// 保存订单
	orderKey := fmt.Sprintf("order_%s", ord.Base.TxId)
	orderData, _ := json.Marshal(ord)
	ws = append(ws, WriteOp{
		Key:   orderKey,
		Value: orderData,
		Del:   false,
	})

	// 添加订单到账户的订单列表
	if account.Orders == nil {
		account.Orders = make([]string, 0)
	}
	account.Orders = append(account.Orders, ord.Base.TxId)

	// 保存更新后的账户
	updatedAccountData, err := json.Marshal(&account)
	if err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to marshal account",
		}, err
	}

	ws = append(ws, WriteOp{
		Key:   accountKey,
		Value: updatedAccountData,
		Del:   false,
	})

	// 创建价格索引，用于快速查询
	// key格式: pair:base_quote|price:xxx|is_filled:false|order_id:xxx
	priceIndexKey := fmt.Sprintf("pair:%s_%s|price:%s|is_filled:%v|order_id:%s",
		ord.BaseToken, ord.QuoteToken, ord.Price, ord.IsFilled, ord.Base.TxId)

	priceIndex := &pb.OrderPriceIndex{Ok: true}
	priceIndexData, _ := json.Marshal(priceIndex)
	ws = append(ws, WriteOp{
		Key:   priceIndexKey,
		Value: priceIndexData,
		Del:   false,
	})

	return ws, &Receipt{
		TxID:       ord.Base.TxId,
		Status:     "SUCCEED",
		WriteCount: len(ws),
	}, nil
}

// handleRemoveOrder 处理撤单
func (h *OrderTxHandler) handleRemoveOrder(ord *pb.OrderTx, sv StateView) ([]WriteOp, *Receipt, error) {
	if ord.OpTargetId == "" {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "op_target_id is required for REMOVE operation",
		}, fmt.Errorf("op_target_id is required")
	}

	// 读取要撤销的订单
	targetOrderKey := fmt.Sprintf("order_%s", ord.OpTargetId)
	targetOrderData, exists, err := sv.Get(targetOrderKey)
	if err != nil || !exists {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "target order not found",
		}, fmt.Errorf("target order not found: %s", ord.OpTargetId)
	}

	var targetOrder pb.OrderTx
	if err := json.Unmarshal(targetOrderData, &targetOrder); err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse target order",
		}, err
	}

	// 验证权限：只有订单创建者可以撤单
	if targetOrder.Base.FromAddress != ord.Base.FromAddress {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "only order creator can remove the order",
		}, fmt.Errorf("permission denied")
	}

	// 读取账户
	accountKey := fmt.Sprintf("account_%s", ord.Base.FromAddress)
	accountData, exists, err := sv.Get(accountKey)
	if err != nil || !exists {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "account not found",
		}, fmt.Errorf("account not found")
	}

	var account pb.Account
	if err := json.Unmarshal(accountData, &account); err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to parse account",
		}, err
	}

	ws := make([]WriteOp, 0)

	// 删除订单
	ws = append(ws, WriteOp{
		Key:   targetOrderKey,
		Value: nil,
		Del:   true,
	})

	// 从账户的订单列表中移除
	if account.Orders != nil {
		newOrders := make([]string, 0)
		for _, orderId := range account.Orders {
			if orderId != ord.OpTargetId {
				newOrders = append(newOrders, orderId)
			}
		}
		account.Orders = newOrders
	}

	// 保存更新后的账户
	updatedAccountData, err := json.Marshal(&account)
	if err != nil {
		return nil, &Receipt{
			TxID:   ord.Base.TxId,
			Status: "FAILED",
			Error:  "failed to marshal account",
		}, err
	}

	ws = append(ws, WriteOp{
		Key:   accountKey,
		Value: updatedAccountData,
		Del:   false,
	})

	// 删除价格索引
	priceIndexKey := fmt.Sprintf("pair:%s_%s|price:%s|is_filled:%v|order_id:%s",
		targetOrder.BaseToken, targetOrder.QuoteToken, targetOrder.Price, targetOrder.IsFilled, ord.OpTargetId)

	ws = append(ws, WriteOp{
		Key:   priceIndexKey,
		Value: nil,
		Del:   true,
	})

	return ws, &Receipt{
		TxID:       ord.Base.TxId,
		Status:     "SUCCEED",
		WriteCount: len(ws),
	}, nil
}

func (h *OrderTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}

