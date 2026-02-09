---
name: Verkle State Backend
description: Verkle state tree backend, versioned store sessions, and runtime state-root persistence path.
triggers:
  - verkle
  - state root
  - apply state update
  - versioned store
---

# Verkle State Backend Skill

Use this skill for state root mismatches, session commit behavior, and stateful key apply logic.

## Source Map

- StateDB facade: `verkle/verkle_statedb.go`
- Tree/store implementation:
  - `verkle/versioned_store.go`
  - `verkle/versioned_badger_store.go`
  - `verkle/verkle_statedb.go`
- DB integration:
  - `db/db.go` (`dbSession.ApplyStateUpdate`, `CommitRoot`)
- Key routing:
  - `keys/category.go`

## Runtime Integration

- VM execution emits write ops.
- DB session filters stateful keys and applies them to Verkle session.
- State root is returned to block header and committed after session commit.

## Typical Tasks

1. State root differs across nodes:
   - verify deterministic ordering of state updates
   - verify `keys.IsStatefulKey` classification
   - inspect `VerkleStateDBSession.ApplyUpdate`
2. Missing value at version:
   - inspect `GetAtVersion` and root/version recovery behavior
3. Startup recovery issues:
   - inspect `recoverLatestVersion` in `verkle/verkle_statedb.go`

## Quick Commands

```bash
rg "ApplyUpdate|CommitRoot|GetAtVersion|recoverLatestVersion" verkle db
rg "IsStatefulKey|CategorizeKey|statePrefixes" keys
```
