---
name: project-overview
description: High-level routing map for dex architecture, module ownership, and entrypoint navigation.
---

# Project Overview Skill

## Trigger Cues

- `overview`
- `architecture`
- `module map`
- `codebase`

Use this skill when you need to quickly route a task to the right module before deep editing.

## Architecture Snapshot

- Runtime entry:
  - `cmd/main/main.go`
  - `cmd/main/node.go`
  - `cmd/main/bootstrap.go`
  - `cmd/main/simulator.go` (local testnet traffic engine)
- Explorer entry:
  - `cmd/explorer/main.go`
  - `cmd/explorer/syncer/syncer.go`
  - `explorer/src/App.vue`
- Wallet entry:
  - `wallet/background/service_worker.js`
  - `wallet/src/App.vue`
- Protocol definition:
  - `pb/data.proto`

## Core Data Path

1. Incoming tx enters `handlers/` and then `txpool/`.
2. Consensus proposes/finalizes blocks in `consensus/`.
3. VM executes block body in `vm/`.
4. `db.Manager` persists writes to PebbleDB (flat KV, no independent state tree).
5. Query APIs and explorer read paths return state/tx/block data.

Key files:

- `vm/executor.go`
- `db/db.go`
- `keys/keys.go`
- `keys/category.go`

## Module-to-Skill Routing

| If task mentions | Go to skill | Primary paths |
| --- | --- | --- |
| tx execution, receipt, dry-run, write diff | `vm` | `vm/` |
| orderbook/match logic | `matching` (+ `vm`) | `matching/`, `vm/order_handler.go` |
| consensus/vote/query/sync | `consensus` | `consensus/` |
| withdraw/dkg/committee/frost envelope | `frost` | `frost/`, `vm/frost_*.go` |
| API route/response errors | `handlers` | `handlers/`, `cmd/main/node.go` |
| pebble keys/storage/index | `db` | `db/`, `keys/` |
| witness stake/challenge/arbitration | `witness` | `witness/`, `vm/witness_*.go` |
| tx queue, pending, dedupe | `txpool` | `txpool/` |
| P2P sending/retry/backoff | `sender` | `sender/` |
| frontend explorer UI/API | `explorer` | `explorer/`, `cmd/explorer/` |
| wallet extension, DApp sig popup | `wallet` | `wallet/` |
| state snapshot sync / shard paging | `db` + `sender` | `stateDB/`, `db/state_snapshot_sync.go`, `sender/state_snapshot_sync.go` |
| config/defaults/bootstrap params | `config` | `config/`, `cmd/main/bootstrap.go` |
| test harness / runtime integration tests | `testharness` | `testharness/` |
| testnet tx simulator & chaos traffic | `project-overview` | `cmd/main/simulator.go` |

## Current Notes

- FROST has both on-chain handlers (`vm/frost_*.go`) and off-chain runtime workers (`frost/runtime/`).
- DB backend is PebbleDB, all state stored as flat KV pairs.

## Useful Commands

```bash
rg "HandleFunc\\(" handlers cmd/main cmd/explorer
rg "DefaultKindFn|RegisterDefaultHandlers" vm
rg "issueQuery|RegisterQuery|SubmitChit" consensus
```
