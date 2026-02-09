---
name: Project Overview
description: High-level routing map for dex architecture, module ownership, and entrypoint navigation.
triggers:
  - overview
  - architecture
  - module map
  - codebase
---

# Project Overview Skill

Use this skill when you need to quickly route a task to the right module before deep editing.

## Architecture Snapshot

- Runtime entry:
  - `cmd/main/main.go`
  - `cmd/main/node.go`
  - `cmd/main/bootstrap.go`
- Explorer entry:
  - `cmd/explorer/main.go`
  - `cmd/explorer/syncer/syncer.go`
  - `explorer/src/App.vue`
- Protocol definition:
  - `pb/data.proto`

## Core Data Path

1. Incoming tx enters `handlers/` and then `txpool/`.
2. Consensus proposes/finalizes blocks in `consensus/`.
3. VM executes block body in `vm/`.
4. `db.Manager` persists writes to Badger and mirrors stateful keys to Verkle.
5. Query APIs and explorer read paths return state/tx/block data.

Key files:

- `vm/executor.go`
- `db/db.go`
- `keys/category.go`
- `verkle/verkle_statedb.go`

## Module-to-Skill Routing

| If task mentions | Go to skill | Primary paths |
| --- | --- | --- |
| tx execution, receipt, dry-run, write diff | `vm` | `vm/` |
| orderbook/match logic | `matching` (+ `vm`) | `matching/`, `vm/order_handler.go` |
| consensus/vote/query/sync | `consensus` | `consensus/` |
| withdraw/dkg/committee/frost envelope | `frost` | `frost/`, `vm/frost_*.go` |
| API route/response errors | `handlers` | `handlers/`, `cmd/main/node.go` |
| badger keys/storage/index | `db` | `db/`, `keys/` |
| state root / session / proof with Verkle | `verkle` | `verkle/`, `db/db.go` |
| witness stake/challenge/arbitration | `witness` | `witness/`, `vm/witness_*.go` |
| tx queue, pending, dedupe | `txpool` | `txpool/` |
| P2P sending/retry/backoff | `sender` | `sender/` |
| peer registry | `network` | `network/network.go` |
| frontend explorer UI/API | `explorer` | `explorer/`, `cmd/explorer/` |
| config/defaults/bootstrap params | `config` | `config/`, `cmd/main/bootstrap.go` |
| legacy state db package | `statedb` | `stateDB/` |
| legacy JMT tree package | `jmt` | `jmt/` |

## Current Notes

- Verkle is wired into runtime state apply path (`db/db.go`).
- `stateDB/` and `jmt/` still exist but are no longer the default runtime state backend.
- FROST has both on-chain handlers (`vm/frost_*.go`) and off-chain runtime workers (`frost/runtime/`).

## Useful Commands

```bash
rg "HandleFunc\\(" handlers cmd/main cmd/explorer
rg "DefaultKindFn|RegisterDefaultHandlers" vm
rg "ApplyStateUpdate|CommitRoot" db verkle vm
rg "issueQuery|RegisterQuery|SubmitChit" consensus
```

