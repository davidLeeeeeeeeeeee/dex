---
name: txpool
description: Pending transaction admission and queue processing in txpool, including duplicate handling, validator checks, async pending persistence, short-hash indexing, and proposer fetch paths. Use when tasks mention txpool or mempool behavior, /tx submit failures, queue or pending pressure, missing pending tx after restart, or short-hash tx lookup/sync mismatches.
---

# TxPool Skill

## Read First

- Pool internals: `txpool/txpool.go`
- Queue loop: `txpool/txpool_queue.go`
- HTTP submit entry: `handlers/handleTx.go`
- Batch short-hash fetch: `handlers/handleBatchGetTx.go`
- Block proposal fetch: `consensus/realProposer.go`
- Runtime config: `config/config.go` (`TxPoolConfig`)

## Runtime Flow

1. Submit transactions through `SubmitTx`.
2. Fast-reject nil/uninitialized input; fast-accept already-known tx (`HasTransaction`).
3. Enforce `MaxPendingTxs`; return `txpool pending is full` on overflow.
4. Push message into `Queue.MsgChan`; return `txpool queue is full` when saturated.
5. Queue worker `runLoop` calls `handleAddTx`:
   - require non-empty tx id and non-nil base message
   - validate by `validator.CheckAnyTx`
   - persist by `storeAnyTx`
6. `storeAnyTx` keeps memory-first semantics:
   - if tx already applied (`isTxApplied`), cache only for lookup (`cacheTx`, `shortTxCache`)
   - else cache in pending maps first, then enqueue async DB save
7. Pending save workers call `SavePendingAnyTx`; startup `loadFromDB` restores pending cache and removes stale pending keys already applied by VM.

## Operational Checks

1. `/tx` returns 429:
   - check whether error is `txpool queue is full` or `txpool pending is full`
   - inspect `QueueDepth()` and `PendingLen()`
   - tune `TxPool.MaxPendingTxs`; note `MsgChan` capacity in `newTxPoolQueue` is currently hard-coded to `10000`
2. Duplicate tx still creates pressure:
   - inspect early duplicate-accept path in `SubmitTx`
   - verify caller behavior in `handlers/handleTx.go` callback path
3. Pending tx missing after restart:
   - inspect `loadFromDB`, `LoadPendingAnyTx`, and stale cleanup by `keys.KeyPendingAnyTx`
4. Short-hash miss in sync:
   - inspect `ConcatFirst8Bytes` hex/fallback behavior
   - inspect `GetTxsByShortHashes` with `isSync=true/false`

## Editing Rules

1. Keep duplicate semantics stable: known tx should be treated as accepted.
2. Keep memory-first write order in `storeAnyTx`; proposer paths expect immediate visibility.
3. Preserve async persistence backpressure and worker drain-on-stop behavior.
4. If tx-id encoding changes, update both short-hash producer (`ConcatFirst8Bytes`) and lookup consumer (`GetTxsByShortHashes`).
5. `StoreAnyTx` and `RemoveAnyTx` are legacy direct-DB APIs; verify compatibility before removing or changing them.

## Quick Commands

```bash
rg "SubmitTx|storeAnyTx|loadFromDB|isTxApplied|enqueuePendingSave" txpool
rg "handleAddTx|CheckAnyTx|MsgChan|QueueDepth|PendingLen" txpool
rg "HandleTx|HandleBatchGetTx" handlers
rg "GetPendingTxs|ConcatFirst8Bytes|GetTxsByShortHashes" consensus txpool
```
