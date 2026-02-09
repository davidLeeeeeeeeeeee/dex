# DEX Agent Guide

This folder stores AI-facing retrieval metadata for the `dex` codebase.
Last updated: `2026-02-09`.

## Quick Routing

Use this table to jump to the right area fast.

| Task | Primary paths | Suggested skill |
| --- | --- | --- |
| Transaction execution, receipts, state diff | `vm/`, `pb/data.proto`, `keys/` | `vm` |
| Order matching and orderbook behavior | `matching/`, `vm/order_handler.go`, `handlers/handleOrderBook.go` | `matching` + `vm` |
| Consensus, voting, block sync | `consensus/` | `consensus` |
| FROST withdraw / DKG / runtime workers | `frost/`, `vm/frost_*.go`, `handlers/frost_*.go` | `frost` |
| HTTP API behavior | `handlers/`, `cmd/main/node.go` | `handlers` |
| DB persistence and key schema | `db/`, `keys/`, `verkle/` | `db` + `verkle` |
| Verkle state root or state-session issues | `verkle/`, `db/db.go`, `keys/category.go` | `verkle` |
| Witness workflow and arbitration | `witness/`, `vm/witness_*.go` | `witness` |
| Tx queueing and gossip send path | `txpool/`, `sender/`, `network/` | `txpool` + `sender` + `network` |
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
  - `simulateMain/tes.go`

## Current Storage Architecture

- Canonical persistence manager is `db.Manager` in `db/db.go`.
- Mutable state is mirrored into Verkle through `dbSession.ApplyStateUpdate` and `verkle/`.
- Key routing (stateful vs flow keys) is defined in `keys/category.go`.
- Legacy/experimental trees still exist:
  - `stateDB/` (legacy StateDB package)
  - `jmt/` (Jellyfish Merkle Tree implementation)

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
  jmt/
  matching/
  network/
  project_overview/
  sender/
  statedb/
  txpool/
  verkle/
  vm/
  witness/
```

## Fast Search Commands

```bash
# All HTTP routes
rg "HandleFunc\\(" handlers cmd/main cmd/explorer

# All tx handler kinds in VM
rg "return \"[a-z0-9_]+\"" vm/handlers.go

# FROST runtime workers/sessions
rg "type .*Worker|type .*Session|func \\(.*\\) Start" frost/runtime

# Stateful key classification
rg "statePrefixes|IsStatefulKey|CategorizeKey" keys

# Verkle apply path
rg "ApplyStateUpdate|ApplyUpdate|CommitRoot" db verkle vm
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
