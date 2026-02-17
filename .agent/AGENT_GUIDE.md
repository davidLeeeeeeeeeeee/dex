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
