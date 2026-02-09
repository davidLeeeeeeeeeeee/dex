---
name: JMT
description: Jellyfish Merkle Tree package for versioned state roots, proofs, and batch/parallel tree updates.
triggers:
  - jmt
  - jellyfish merkle
  - merkle proof
  - versioned tree
  - merkle
---

# JMT Skill

Use this skill when modifying or testing the `jmt/` package itself.

## Source Map

- Core tree operations: `jmt/jmt.go`
- Parallel update path: `jmt/jmt_parallel.go`
- Proof generation/verification: `jmt/jmt_proof.go`
- Hasher/node encoding: `jmt/jmt_hasher.go`, `jmt/node.go`
- Versioned stores:
  - `jmt/versioned_store.go`
  - `jmt/versioned_badger_store.go`

## Important Context

- JMT package remains in repo and has extensive tests/benchmarks.
- Current runtime state backend is Verkle (`verkle/` + `db/db.go`), not JMT.
- Treat JMT as standalone tree module unless runtime wiring changes.

## Typical Tasks

1. Add proof logic or fix proof mismatch:
   - inspect `jmt/jmt_proof.go` and `jmt/jmt_proof_test.go`
2. Improve update performance:
   - inspect `jmt/jmt_parallel.go` and related benches
3. Debug version fetch issues:
   - inspect root history and versioned store reads in `jmt/jmt.go`

## Quick Commands

```bash
rg "Update|Delete|Prove|Version|CommitRoot" jmt
go test ./jmt/... -v
```

