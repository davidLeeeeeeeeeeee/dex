---
name: Database Layer
description: Badger-backed persistence manager, write queue, keyspace rules, and runtime state sync bridge.
triggers:
  - db
  - badger
  - key schema
  - keyspace
  - persistence
---

# Database Layer Skill

Use this skill when debugging persistence, key naming, write ordering, or state sync to Verkle.

## Source Map

- DB manager and queue: `db/db.go`
- Tx/raw/receipt helpers: `db/manage_tx_storage.go`
- Account/token/block managers: `db/manage_account.go`, `db/mange_block.go`, `db/manage_frost.go`
- Key constructors: `keys/keys.go`
- Key category routing: `keys/category.go`

## Current Runtime Model

- `db.Manager` is the runtime persistence facade.
- Data is split into:
  - flow/history/index keys in Badger
  - stateful keys mirrored via Verkle session (`ApplyStateUpdate`)
- Key category decision is centralized in `keys/category.go` (`CategorizeKey`).

## Typical Tasks

1. Add a new persisted object:
   - add key builder in `keys/keys.go`
   - classify stateful/flow behavior in `keys/category.go` if needed
   - wire read/write in relevant `db/manage_*.go` file
2. Debug missing tx/receipt:
   - inspect `GetAnyTxById`, `GetTxReceipt` in `db/manage_tx_storage.go`
3. Debug flush latency:
   - inspect write queue setup in `db/db.go` (`InitWriteQueue`, `runWriteQueue`, `ForceFlush`)

## Quick Commands

```bash
rg "InitWriteQueue|runWriteQueue|ApplyStateUpdate|CommitRoot" db
rg "func Key|KeyVersion|CategorizeKey|IsStatefulKey" keys
```

