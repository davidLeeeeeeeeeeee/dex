---
name: frost
description: Threshold signing runtime, withdraw job window planning, DKG transition flow, chain adapters, and VM-facing frost transaction handlers.
---

# FROST and DKG Skill

## Trigger Cues

- `frost`
- `dkg`
- `roast`
- `withdraw signing`
- `vault transition`

Use this skill when working on withdraw planning/signing, DKG transitions, chain templates, or frost P2P envelopes.

## Source Map

- Runtime manager: `frost/runtime/manager.go`
- Worker loops:
  - `frost/runtime/workers/withdraw_worker.go`
  - `frost/runtime/workers/transition_worker.go`
- Job planning/scanning:
  - `frost/runtime/planning/scanner.go`
  - `frost/runtime/planning/job_window_planner.go`
- Signing orchestration service:
  - `frost/runtime/services/signing_service.go`
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
   - inspect scanner -> `PlanJobWindow` -> `ProcessWindow` pipeline
   - inspect withdraw state keys in `keys/keys.go`
2. DKG not advancing:
   - inspect transition worker stage gates and deadlines
   - inspect VM DKG handlers for state transitions
3. Signature verification mismatch:
   - inspect chain adapter template hash, tweaks, and VM-side recomputation

## Quick Commands

```bash
rg "type .*Worker|func \\(.*\\) Start|HandlePBEnvelope" frost/runtime
rg "PlanJobWindow|ScanOnce|ProcessWindow|StartSigningSession|WaitForCompletion" frost/runtime
rg "frost_vault|frost_withdraw|DKG|Transition" vm handlers keys
rg "ChainAdapter|RegisterAdapter|Build.*Template|Build.*Tx" frost/chain
```
