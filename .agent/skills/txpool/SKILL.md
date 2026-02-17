---
name: TxPool
description: Pending transaction ingestion, validation queue, dedupe, and proposal selection cache.
triggers:
  - txpool
  - pending tx
  - mempool
  - submit tx
  - transaction queue
---

# TxPool Skill

Use this skill for pending tx admission, dedupe, eviction, and queue handling.

## Source Map

- Pool facade and caches: `txpool/txpool.go`
- Queue loop and validator path: `txpool/txpool_queue.go`

## Core Flow

1. Incoming tx is enqueued via `SubmitTx`.
2. Queue worker validates and decides known-node/unknown-node path.
3. Accepted tx enters in-memory pending cache and async DB persistence.
4. Consensus fetches pending txs for proposals.

## Typical Tasks

1. Duplicate tx still accepted:
   - inspect `HasTransaction`, short-id mapping, and queue dedupe path
2. Pending tx disappears:
   - inspect async DB write path and load-from-db initialization
3. Queue full under burst:
   - inspect `MsgChan` capacity and caller backpressure behavior

## Quick Commands

```bash
rg "SubmitTx|storeAnyTx|loadFromDB|HasTransaction|GetPendingTxs" txpool
rg "handleAddTx|CheckAnyTx|msgAddTx" txpool
```

