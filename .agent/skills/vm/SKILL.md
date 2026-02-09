---
name: VM
description: Transaction execution engine, handler registry, block pre-execution, commit path, and state diff generation.
triggers:
  - vm
  - execute block
  - tx handler
  - receipt
  - writeop
---

# VM Skill

Use this skill for execution correctness, receipt status, write operations, and tx handler behavior.

## Source Map

- Handler kind dispatch: `vm/handlers.go`
- Default handler registration: `vm/default_handlers.go`
- Executor lifecycle: `vm/executor.go`
- Core types (`WriteOp`, `Receipt`): `vm/types.go`
- Order integration:
  - `vm/order_handler.go`
  - `vm/executor.go` (orderbook rebuild path)
- Frost integration:
  - `vm/frost_*.go`
  - `vm/frost_dkg_handlers.go`
- Witness integration:
  - `vm/witness_handler.go`
  - `vm/witness_events.go`

## Core Execution Path

1. `PreExecuteBlock` runs dry execution over block txs.
2. Each tx resolves kind via `DefaultKindFn`.
3. Handler `DryRun` returns `[]WriteOp` and a receipt.
4. `CommitFinalizedBlock` reuses or recomputes result and calls `applyResult`.
5. `applyResult` persists writes and updates state backend through DB session.

## Typical Tasks

1. Wrong tx type routing:
   - inspect `DefaultKindFn` cases in `vm/handlers.go`
2. Block valid in one node but invalid in another:
   - inspect deterministic orderbook rebuild and tx duplicate skip logic in `vm/executor.go`
3. Receipt status or error text issues:
   - inspect receipt mutation and final persistence flow in `vm/executor.go`

## 已知陷阱（实战经验）

1. **内存泄漏 — SpecExecLRU 缓存过大**（已识别，待优化）：
   - `vm/spec_cache.go` 中的 `SpecExecLRU` 会缓存每个区块的预执行结果（含完整 `Diff`）
   - pprof 堆分析显示该缓存可增长到 40GB+
   - **应对**：缩小缓存大小；区块 finalize 后清除 `SpecResult.Diff`

2. **余额不一致 — SetBalance 双写冲突**：
   - `order_handler.go` 中 `handleAddOrder` 调用 `SetBalance` 冻结余额
   - `updateAccountBalancesFromStates` 也会写入余额
   - 两者的 WriteOp 在 `executor.go` 中通过 `applyResult` 重复应用，可能导致 "insufficient balance"
   - **排查流程**：参考 `/.agent/workflows/debug_balance.md`

3. **nil pointer panic in updateAccountBalancesFromStates**：
   - 历史bug：map key 为 nil 指针时触发 `runtime.fatalpanic`
   - 确保 `order_handler.go` 中使用 map 前做空指针检查

4. **余额工具函数**：
   - `vm/balance_helper.go` 包含余额读写的通用辅助函数
   - `vm/stateview.go` 提供 StateView 抽象层

## Quick Commands

```bash
rg "DefaultKindFn|RegisterDefaultHandlers|Kind\\(\\)" vm
rg "PreExecuteBlock|CommitFinalizedBlock|applyResult|IsBlockCommitted" vm/executor.go
rg "DryRun\\(" vm
rg "SpecExecLRU|specCache|SpecResult" vm
rg "SetBalance|GetBalance|balanceUpdates" vm
```

