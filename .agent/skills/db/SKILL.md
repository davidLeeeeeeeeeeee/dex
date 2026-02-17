---
name: Database Layer
description: PebbleDB-backed persistence manager, write queue, and keyspace rules.
triggers:
  - db
  - pebble
  - key schema
  - keyspace
  - persistence
---

# Database Layer Skill

Use this skill when debugging persistence, key naming, write ordering, or query performance.

## Source Map

- DB manager: `db/db.go`
- Write queue: `db/write_queue.go`
- Tx/raw/receipt helpers: `db/manage_tx_storage.go`
- Account/token/block managers: `db/manage_account.go`, `db/mange_block.go`, `db/manage_frost.go`
- Order scan/index: `db/scan_order.go`, `db/miner_index_manager.go`
- Key constructors: `keys/keys.go`
- Key category routing: `keys/category.go`

## Current Runtime Model

- `db.Manager` is the runtime persistence facade, backed by PebbleDB (`cockroachdb/pebble`).
- All state stored as flat KV pairs（无独立状态树）。
- Key category decision is centralized in `keys/category.go` (`CategorizeKey`).
- Block cache configured via `config.Database.BlockCacheSizeDB` to reduce CGo overhead.

## Typical Tasks

1. Add a new persisted object:
   - add key builder in `keys/keys.go`
   - classify behavior in `keys/category.go` if needed
   - wire read/write in relevant `db/manage_*.go` file
2. Debug missing tx/receipt:
   - inspect `GetAnyTxById`, `GetTxReceipt` in `db/manage_tx_storage.go`
3. Debug flush latency:
   - inspect write queue setup in `db/write_queue.go` (`InitWriteQueue`, `runWriteQueue`, `ForceFlush`)

## Quick Commands

```bash
rg "InitWriteQueue|runWriteQueue|ForceFlush" db
rg "func Key|KeyVersion|CategorizeKey|IsStatefulKey" keys
```
