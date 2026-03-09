---
name: vm
description: Transaction execution engine, handler registry, block pre-execution, commit path, and state diff generation.
---

# VM Skill

## Trigger Cues

- `vm`
- `execute block`
- `tx handler`
- `receipt`
- `writeop`

Use this skill for execution correctness, receipt status, write operations, and tx handler behavior.

## Source Map

- Handler kind dispatch: `vm/handlers.go`
- Default handler registration: `vm/default_handlers.go`
- Executor lifecycle: `vm/executor.go`
- Executor probes/diagnostics: `vm/executor_probe.go`
- Core types (`WriteOp`, `Receipt`): `vm/types.go`
- Order integration:
  - `vm/order_handler.go`
  - `vm/orderbook_rebuild.go`
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
5. `applyResult` persists writes to PebbleDB through DB session.

## Typical Tasks

1. Wrong tx type routing:
   - inspect `DefaultKindFn` cases in `vm/handlers.go`
2. Block valid in one node but invalid in another:
   - inspect deterministic rebuild/order handling in `vm/orderbook_rebuild.go` and `vm/executor.go`
3. Receipt status or error text issues:
   - inspect receipt mutation and final persistence flow in `vm/executor.go`

## 已知陷阱（实战）

1. 内存增长风险: `SpecExecLRU` 缓存过大。
   - 位置：`vm/spec_cache.go`。
   - 现象：预执行缓存包含大块 `Diff` 时，堆占用会快速增长。
   - 应对：控制缓存大小；区块 finalize 后及时释放或裁剪 `SpecResult.Diff`。
2. 余额不一致: `SetBalance` 双写冲突。
   - 位置：`vm/order_handler.go` 与 `vm/executor.go` 的 `applyResult` 链路。
   - 现象：冻结余额和聚合余额更新顺序冲突，可能触发 `insufficient balance`。
   - 排查流程：参考 `/.agent/workflows/debug_balance.md`。
3. `updateAccountBalancesFromStates` 空指针风险。
   - 历史问题：map key/value 为空时触发 panic。
   - 要求：在 `vm/order_handler.go` 中使用 map 前先做空值校验。
4. 余额辅助与抽象。
   - `vm/balance_helper.go` 提供余额读写通用助手。
   - `vm/stateview.go` 是 `StateView` 抽象层，调试时优先确认它的读写一致性。

## 一致性检查清单（无 State Hash 强校验）

1. 检查所有 `WriteOp` 产出路径，禁止直接 `range map` 后写入可观察结果。
2. 若必须用 `map` 去重，落盘、回执、上报前必须按 key 排序。
3. `StateView.Diff()`、索引重建、批量读取后的处理必须保证顺序可重放。
4. 内存缓存只能优化性能，不能成为状态真源；重启后结果必须和冷启动一致。

## Quick Commands

```bash
rg "DefaultKindFn|RegisterDefaultHandlers|Kind\\(\\)" vm
rg "PreExecuteBlock|CommitFinalizedBlock|applyResult|IsBlockCommitted" vm/executor.go
rg "DryRun\\(" vm
rg "SpecExecLRU|specCache|SpecResult" vm
rg "SetBalance|GetBalance|balanceUpdates|orderbook_rebuild" vm
```
