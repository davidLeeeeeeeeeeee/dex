---
name: FROST and DKG
description: Threshold signing runtime, DKG transition flow, chain adapters, and VM-facing frost transaction handlers.
triggers:
  - frost
  - dkg
  - roast
  - withdraw signing
  - vault transition
---

# FROST and DKG Skill

Use this skill when working on withdraw jobs, DKG transitions, chain templates, or frost P2P envelopes.

## Source Map

- Runtime manager: `frost/runtime/manager.go`
- Worker loops:
  - `frost/runtime/workers/withdraw_worker.go`
  - `frost/runtime/workers/transition_worker.go`
- Job planning/scanning:
  - `frost/runtime/planning/scanner.go`
  - `frost/runtime/planning/job_planner.go`
- Session and nonce store:
  - `frost/runtime/session/store.go`
  - `frost/runtime/session/dkg.go`
  - `frost/runtime/session/roast.go`
- ROAST messaging/coordination:
  - `frost/runtime/roast/coordinator.go`
  - `frost/runtime/roast/participant.go`
- Chain adapters: `frost/chain/*`
- VM integration handlers: `vm/frost_*.go`, `vm/frost_dkg_handlers.go`
- HTTP query/admin APIs: `handlers/frost_query_handlers.go`, `handlers/frost_admin_handlers.go`

## Layering Rules

- `frost/core/` is algorithm-focused logic.
- `frost/runtime/` is node runtime orchestration and background execution.
- `vm/frost_*.go` is on-chain state transition and validation.

## Typical Tasks

1. Withdraw stuck in queued/planning:
   - inspect scanner/planner/withdraw worker pipeline
   - inspect withdraw state keys in `keys/keys.go`
2. DKG not advancing:
   - inspect transition worker stage gates and deadlines
   - inspect VM DKG handlers for state transitions
3. Signature verification mismatch:
   - inspect chain adapter template hash and VM-side recomputation

## Quick Commands

```bash
rg "type .*Worker|func \\(.*\\) Start|HandlePBEnvelope" frost/runtime
rg "frost_vault|frost_withdraw|DKG|Transition" vm handlers keys
rg "ChainAdapter|RegisterAdapter|Build.*Template" frost/chain
```

