---
name: StateDB (Legacy Package)
description: Legacy stateDB package with sharding, memory windows, snapshots, and WAL experiments.
triggers:
  - statedb
  - wal
  - snapshot window
  - shard state
---

# StateDB Skill

Use this skill only when working inside the `stateDB/` package or related docs/tests.

## Source Map

- Main db type: `stateDB/db.go`
- Query/update APIs: `stateDB/query.go`, `stateDB/update.go`
- Snapshot and mem-window: `stateDB/snapshot.go`, `stateDB/mem_window.go`
- Shard helpers: `stateDB/shard.go`
- WAL logic: `stateDB/wal.go`

## Important Context

- Current runtime applies mutable state via Verkle (`verkle/` through `db/db.go`).
- `stateDB/` remains useful for legacy reference, experiments, or migration tooling.

## Typical Tasks

1. Evaluate WAL recovery behavior:
   - inspect `recoverFromWAL` path in `stateDB/db.go`
   - inspect record format in `stateDB/wal.go`
2. Tune snapshot window behavior:
   - inspect epoch/page settings in `stateDB/types.go`
3. Fix shard routing bugs:
   - inspect key-to-shard mapping in `stateDB/shard.go`

## Quick Commands

```bash
rg "WAL|Snapshot|Shard|Epoch|Apply" stateDB
go test ./stateDB/... -v
```

