---
name: sender
description: P2P outbound pipeline with prioritized queues, retry/backoff, callback send paths, and state snapshot sync fetch.
---

# Sender Skill

## Trigger Cues

- `sender`
- `broadcast`
- `gossip`
- `retry queue`
- `p2p send`
- `snapshot sync`

Use this skill when outbound messages fail, retry/inflight policy is wrong, or queue pressure causes drops.

## Source Map

- Sender facade and task construction: `sender/manager.go`
- Priority queues and retry loop: `sender/queue.go`
- HTTP/3 client setup: `sender/http3_client.go`
- Simple no-reply send funcs (`/tx`, `/put`, `/pushquery`, `/pullquery`, `/gossipAnyMsg`): `sender/do_send_simple.go`
- Callback/read-response send funcs (`/heightquery`, `/getsyncblocks`, `/getblock`, `/getblockbyid`, `/batchgetdata`): `sender/do_send_callbacks.go`
- FROST envelope send path: `sender/doSendFrost.go`
- State snapshot sync pull helpers: `sender/state_snapshot_sync.go`

## Queue Model

- Three priority lanes:
  - `PriorityImmediate`
  - `PriorityControl`
  - `PriorityData`
- `SendQueue` enforces per-target inflight limits and task expiration (`TaskExpireTimeout`).
- Retries use backoff + jitter in `handleRetry`; overload can trigger delayed requeue.

## Typical Tasks

1. Messages dropped under load:
   - inspect `enqueueNow`, `drop*` counters, and channel capacities in `sender/queue.go`
2. Retry behavior too aggressive or too weak:
   - inspect `handleRetry`, `calcBackoffDuration`, and sender config defaults
3. Callback path decode errors:
   - inspect `postProtobufAndRead` and response unmarshal in `sender/do_send_callbacks.go`
4. Snapshot sync fetch failure:
   - inspect `FetchStateSnapshotShards` / `FetchStateSnapshotPage` in `sender/state_snapshot_sync.go`

## Quick Commands

```bash
rg "Priority|Enqueue|workerLoop|handleRetry|TaskExpireTimeout|maxInflightPerTarget" sender config
rg "doSend|postProtobufNoReply|postProtobufAndRead|FetchStateSnapshot" sender
rg "Broadcast|Pull|SendSyncRequest|SendHeightQuery|SendFrost" sender/manager.go sender/doSendFrost.go
```
