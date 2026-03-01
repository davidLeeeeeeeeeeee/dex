# DEX Agent Guide

This folder stores AI-facing retrieval metadata for the `dex` codebase.
Last updated: `2026-02-17`.

## Quick Routing

Use this table to jump to the right area fast.

| Task | Primary paths | Suggested skill |
| --- | --- | --- |
| Transaction execution, receipts, state diff | `vm/`, `pb/data.proto`, `keys/` | `vm` |
| Order matching and orderbook behavior | `matching/`, `vm/order_handler.go`, `handlers/handleOrderBook.go` | `matching` + `vm` |
| Consensus, voting, block sync | `consensus/` | `consensus` |
| FROST withdraw / DKG / runtime workers | `frost/`, `vm/frost_*.go`, `handlers/frost_*.go` | `frost` |
| HTTP API behavior | `handlers/`, `cmd/main/node.go` | `handlers` |
| DB persistence and key schema | `db/`, `keys/` | `db` |
| Witness workflow and arbitration | `witness/`, `vm/witness_*.go` | `witness` |
| Tx queueing and gossip send path | `txpool/`, `sender/` | `txpool` + `sender` |
| Frontend dashboard and explorer APIs | `explorer/`, `cmd/explorer/` | `explorer` |
| Config and bootstrap defaults | `config/`, `cmd/main/bootstrap.go` | `config` |

## Runtime Entrypoints

- Node runtime:
  - `cmd/main/main.go`
  - `cmd/main/node.go`
  - `cmd/main/bootstrap.go`
- Explorer runtime:
  - `cmd/explorer/main.go`
  - `cmd/explorer/syncer/syncer.go`
  - `cmd/explorer/indexdb/`
- Simulator runtime:
  - `cmd/main/simulator.go`

## Current Storage Architecture

- Canonical persistence manager is `db.Manager` in `db/db.go`.
- Backend is **PebbleDB** (`cockroachdb/pebble`).
- All state via flat KV（无独立状态树）。
- Key routing defined in `keys/category.go`, key constructors in `keys/keys.go`.

## Main API Surfaces

- Node API registration is centralized in `handlers/manager.go` (`RegisterRoutes`).
- FROST query/admin routes live in:
  - `handlers/frost_query_handlers.go`
  - `handlers/frost_admin_handlers.go`
- Explorer-side API gateway is in:
  - `cmd/explorer/main.go`
  - `cmd/explorer/frost_handlers.go`
  - `explorer/src/api.ts`

## Skill Directory

```text
.agent/skills/
  config/
  consensus/
  db/
  explorer/
  frost/
  handlers/
  matching/
  sender/
  txpool/
  vm/
  witness/
  project_overview/
```

## Fast Search Commands

```bash
# All HTTP routes
rg "HandleFunc\\(" handlers cmd/main cmd/explorer

# All tx handler kinds in VM
rg "return \"[a-z0-9_]+\"" vm/handlers.go

# FROST runtime workers/sessions
rg "type .*Worker|type .*Session|func \\(.*\\) Start" frost/runtime

# Key schema
rg "func Key|KeyVersion|CategorizeKey|IsStatefulKey" keys
```

## Workflow Directory

```text
.agent/workflows/
  build_and_run.md      # 构建并运行节点、Explorer 或模拟器
  debug_balance.md      # 排查余额异常
  debug_consensus.md    # 排查共识分叉、同步卡住
```

## `.agent` Maintenance Checklist

When core code changes, update these files together:

1. `.agent/AGENT_GUIDE.md`
2. `.agent/skills/project_overview/SKILL.md`
3. Affected module skill files in `.agent/skills/*/SKILL.md`
4. `.agent/docs/retrieval_index.md`
5. `.agent/workflows/` if operational procedures change

## 全网一致性工程原则（无 State Hash 强校验场景）

1. 状态真源必须在 DB/StateView，内存结构（map/slice/cache）只能做性能优化，不能承载最终业务语义。
2. 任何会影响 `WriteOp`、交易提交、索引更新、日志上报顺序的遍历都必须确定化。
3. 禁止直接 `range map` 产出可观察结果；必须先提取 key 并排序后再处理。
4. 批量读取接口若返回 `map`（如 `GetMany/GetKVs`），后续使用必须显式排序。
5. VM/Handler 要满足“同输入 -> 同输出（内容与顺序）”，至少保证 `Diff` 和关键副作用顺序稳定。
6. 新增缓存时必须注明：缓存失效/重启后不影响最终状态，只影响性能。
7. 关键路径优先使用可重放、可审计的数据结构与流程，避免隐式随机性。

## 编写与实现约束

1. 用中文。`task.md` 和实现说明类 `md` 都用中文。
2. 代码在逻辑等价前提下保持精简，能一行写完不展开成三行。
3. 始终坚持奥卡姆剃刀原则：如无必要，勿增实体。
4. 所有输出的代码定位都要vscode里能点击直接跳转到对应代码段落的格式。