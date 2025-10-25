// vm/engine.go
package vm

import (
	"dex/db"
	"dex/matching"
	"dex/types"
	"strconv"
)

type Executor struct {
	DB       *db.Manager
	Matching *matching.OrderBookManager
	// 以后需要别的模块就加字段；没有副作用时不需要 NodeControl
}

// =============== 可选：幂等键 & 提交标记 ===============
func appliedTxKey(txID string) string { return "vm_applied_tx_" + txID }
func commitKey(h uint64) string       { return "vm_commit_h_" + strconv.FormatUint(h, 10) }

// =============== 入口：被最终化事件调用 =================
// 你可以在共识处收到 EventBlockFinalized 时调用它
func (x *Executor) ExecuteFinalizedBlock(b *types.Block) error {
	// 已提交过该高度就跳过（幂等）
	if x.DB.Exists(commitKey(b.Height)) {
		return nil
	}

	dbBlock, err := x.DB.GetBlock(b.Height)
	if err != nil || dbBlock == nil {
		return err // 视你的 DB 设计决定是否直接返回或重试
	}

	// —— 遍历交易：只分发 & 写 DB（无副作用）——
	for _, any := range dbBlock.Body {
		base := any.GetBase()
		if base == nil {
			continue
		}
		txID := base.TxId
		if x.DB.Exists(appliedTxKey(txID)) {
			continue // 幂等跳过
		}

		switch any.GetContent().(type) {
		case *db.AnyTx_OrderTx:
			// A. 让撮合模块产出写任务（推荐）
			writes, err := x.Matching.ApplyOrderTx(any.GetOrderTx())
			if err != nil {
				// 这里按需：记录失败，再继续下一个 or 直接 return err
				return err
			}
			for _, w := range writes {
				x.DB.EnqueueTask(w)
			}

			// 如果你目前的撮合是“直接写 DB”而非返回 writes，也可以直接在这里调用
			// err := x.Matching.ApplyOrderTxAndWrite(x.DB, any.GetOrderTx())

		case *db.AnyTx_MinerTx:
			// 没有本地副作用，仅做状态入库
			if err := x.DB.SaveMinerTx(any.GetMinerTx()); err != nil {
				return err
			}

		// 未来逐步加：StakeTx / UnbondTx / GovernanceTx / TransferTx ...
		// case *db.AnyTx_TransferTx: ...
		default:
			// 未识别类型：按需忽略或记录
		}

		// 标注该 tx 已处理（幂等）
		x.DB.EnqueueSet(appliedTxKey(txID), dbBlock.ID)
	}

	// —— 块末一次 Flush（减少写放大，有提交点）——
	if err := x.DB.ForceFlush(); err != nil {
		return err
	}

	// —— 写高度提交标记（清晰恢复边界；可选但强烈建议）——
	x.DB.EnqueueSet(commitKey(b.Height), dbBlock.ID)
	return x.DB.ForceFlush()
}
