---
description: 排查余额异常（insufficient balance、余额突降、显示不正确等）
---

# 排查余额问题

## 1. 确定读取路径

余额有两条读取路径，确认走的是哪一条：

- **StateDB 路径**（优先）：`db/db.go` → `keys.IsStatefulKey` → `stateDB.Get`
- **KV 回退路径**：如果 StateDB 未命中，回退到 BadgerDB KV

```bash
rg "GetAccount|GetBalance|SetBalance" db/ vm/
rg "IsStatefulKey|CategorizeKey" keys/
```

## 2. 检查 WriteOp 写入链

交易执行后余额通过 WriteOp 写入。确认写入顺序：

1. `vm/order_handler.go` — `handleAddOrder` 中的 `SetBalance` 冻结余额
2. `vm/executor.go` — `applyResult` 持久化 WriteOp
3. `vm/balance_helper.go` — 余额工具函数

⚠️ **已知陷阱**：`handleAddOrder` 和 `updateAccountBalancesFromStates` 可能产生重复写入，导致后续交易看到错误余额。

## 3. 检查 Explorer 余额显示

Explorer 抓取余额快照时，`syncer` 使用的是最新 nodeDB 状态，而非历史高度状态。

```bash
rg "fb_balance_after|balanceToJSON|balance" cmd/explorer/syncer/ cmd/explorer/main.go
```

## 4. 常见根因

| 现象 | 可能原因 | 排查点 |
|------|----------|--------|
| insufficient balance | SetBalance 未持久化 | `order_handler.go` 中 balanceUpdates → WriteOp 链路 |
| 余额突降 | Order 冻结后快照取错时机 | `executor.go` 中 PreExecuteBlock 的余额快照 |
| 历史余额全相同 | syncer 取最新状态而非历史 | `syncer/syncer.go` 中的余额获取逻辑 |
| nil pointer panic | map key 为 nil | `updateAccountBalancesFromStates` 的空指针检查 |
