---
name: db
description: PebbleDB-backed persistence manager, write queue, keyspace rules, and state snapshot sync helpers.
---

# Database Layer Skill

## Trigger Cues

- `db`
- `pebble`
- `key schema`
- `keyspace`
- `persistence`
- `state snapshot`

Use this skill when debugging persistence, key naming, write ordering, or query performance.

## Source Map

- DB manager: `db/db.go`
- Write queue: `db/write_queue.go`
- Tx/raw/receipt helpers: `db/manage_tx_storage.go`
- Account/token/block managers: `db/manage_account.go`, `db/mange_block.go`, `db/manage_frost.go`
- Miner/cache managers: `db/manage_miner.go`, `db/miner_index_manager.go`
- Order scan/index: `db/scan_order.go`
- State sync helpers: `db/state_sync.go`, `db/state_snapshot_sync.go`
- Key constructors: `keys/keys.go`
- Key category routing: `keys/category.go`

## Current Runtime Model

- `db.Manager` is the runtime persistence facade, backed by PebbleDB (`cockroachdb/pebble`).
- All state is stored as flat KV pairs (no independent state tree).
- Key category decision is centralized in `keys/category.go` (`CategorizeKey`).
- Block cache is configured via `config.Database.BlockCacheSizeDB` to reduce CGo overhead.

## Typical Tasks

1. Add a new persisted object:
   - add key builder in `keys/keys.go`
   - classify behavior in `keys/category.go` if needed
   - wire read/write in relevant `db/manage_*.go` file
2. Debug missing tx/receipt:
   - inspect `GetAnyTxById`, `GetTxReceipt` in `db/manage_tx_storage.go`
3. Debug flush latency:
   - inspect write queue setup in `db/write_queue.go` (`InitWriteQueue`, `runWriteQueue`, `ForceFlush`)
4. Debug snapshot sync page/shard behavior:
   - inspect `db/state_snapshot_sync.go` and related key scan order

## Quick Commands

```bash
rg "InitWriteQueue|runWriteQueue|ForceFlush" db
rg "StateSnapshot|Snapshot|state_sync" db stateDB
rg "func Key|KeyVersion|CategorizeKey|IsStatefulKey" keys
```
