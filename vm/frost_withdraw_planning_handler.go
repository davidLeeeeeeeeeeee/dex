// vm/frost_withdraw_planning_handler.go
package vm

import (
	"dex/keys"
	"dex/pb"
	"fmt"

	"google.golang.org/protobuf/proto"
)

// FrostWithdrawPlanningLogTxHandler 处理提现规划日志汇报
type FrostWithdrawPlanningLogTxHandler struct{}

func (h *FrostWithdrawPlanningLogTxHandler) Kind() string {
	return "frost_withdraw_planning_log"
}

func (h *FrostWithdrawPlanningLogTxHandler) DryRun(tx *pb.AnyTx, sv StateView) ([]WriteOp, *Receipt, error) {
	content, ok := tx.GetContent().(*pb.AnyTx_FrostWithdrawPlanningLogTx)
	if !ok {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "not a planning log transaction"}, fmt.Errorf("not a planning log transaction")
	}

	planningTx := content.FrostWithdrawPlanningLogTx
	if planningTx == nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "invalid planning log"}, fmt.Errorf("invalid planning log")
	}

	withdrawID := planningTx.WithdrawId
	if withdrawID == "" {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "missing withdraw_id"}, fmt.Errorf("missing withdraw_id")
	}

	// 1. 读取当前的提现请求状态
	withdrawKey := keys.KeyFrostWithdraw(withdrawID)
	withdrawData, exists, err := sv.Get(withdrawKey)
	if err != nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "failed to read withdraw state"}, err
	}
	if !exists {
		// 如果提现请求不存在，记录日志但返回错误（防止垃圾数据）
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "withdraw request not found"}, fmt.Errorf("withdraw request not found: %s", withdrawID)
	}

	state := &pb.FrostWithdrawState{}
	if err := proto.Unmarshal(withdrawData, state); err != nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "failed to parse withdraw state"}, err
	}

	// 2. 追加日志（仅当状态未完成时）
	if state.Status == "SIGNED" {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "SKIPPED", Error: "withdraw already signed"}, nil
	}

	// 限制日志数量，防止无限膨胀
	const maxLogs = 50
	state.PlanningLogs = append(state.PlanningLogs, planningTx.Logs...)
	if len(state.PlanningLogs) > maxLogs {
		state.PlanningLogs = state.PlanningLogs[len(state.PlanningLogs)-maxLogs:]
	}

	// 3. 回写状态
	updatedData, err := proto.Marshal(state)
	if err != nil {
		return nil, &Receipt{TxID: tx.GetTxId(), Status: "FAILED", Error: "failed to marshal updated state"}, err
	}

	ws := []WriteOp{
		{Key: withdrawKey, Value: updatedData, Del: false, SyncStateDB: true, Category: "frost_planning"},
	}

	return ws, &Receipt{TxID: tx.GetTxId(), Status: "SUCCEED", WriteCount: len(ws)}, nil
}

func (h *FrostWithdrawPlanningLogTxHandler) Apply(tx *pb.AnyTx) error {
	return ErrNotImplemented
}
