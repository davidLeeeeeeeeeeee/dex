---
name: Witness
description: Witness staking, recharge voting, challenge/arbitration flow, and VM witness transaction integration.
triggers:
  - witness
  - recharge
  - arbitration
  - challenge
  - witness stake
---

# Witness Skill

Use this skill for witness lifecycle, vote thresholds, challenge escalation, and recharge finalization logic.

## Source Map

- Service coordinator: `witness/service.go`
- Core config and enums: `witness/types.go`
- Stake state manager: `witness/stake_manager.go`
- Voting logic: `witness/vote_manager.go`
- Challenge/arbitration logic: `witness/challenge_manager.go`
- Selection algorithm: `witness/selector.go`
- VM integration:
  - `vm/witness_handler.go`
  - `vm/witness_events.go`

## Core Workflow

1. Request created (`WitnessRequestTx`) -> witness set selected.
2. Witnesses submit votes -> consensus check.
3. Request enters challenge period or rejected state.
4. Challenge/arbitration can expand scope if needed.
5. Finalized/rejected status emitted and persisted by VM write ops.

## Typical Tasks

1. Voting never reaches consensus:
   - inspect `checkAndProcessConsensus`, thresholds, and round expansion
2. Challenge loop never resolves:
   - inspect timeout handling and scope expansion paths in `service.go` and `challenge_manager.go`
3. Stake/unstake anomalies:
   - inspect stake manager validation and lock-period checks

## Quick Commands

```bash
rg "ProcessVote|CreateRechargeRequest|checkAndProcessConsensus|expandScope" witness
rg "Challenge|Arbitration|Finalize|Shelve|Retry" witness
rg "Witness.*TxHandler|witness_" vm
```

