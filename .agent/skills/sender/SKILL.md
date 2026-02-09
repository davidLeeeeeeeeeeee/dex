---
name: Sender
description: P2P outbound pipeline with send queue, retry policy, HTTP/3 transport, and consensus/gossip message delivery.
triggers:
  - sender
  - broadcast
  - gossip
  - retry queue
  - p2p send
---

# Sender Skill

Use this skill when messages are not delivered, retries are wrong, or queue pressure causes drops.

## Source Map

- Sender facade: `sender/manager.go`
- Priority queues/retry loop: `sender/queue.go`
- HTTP/3 client setup: `sender/http3_client.go`
- Specific send functions:
  - `sender/doSendTx.go`
  - `sender/doSendBlock.go`
  - `sender/doSendPushQuery.go`
  - `sender/doSendPullQuery.go`
  - `sender/doSendFrost.go`
  - `sender/gossip_sender.go`

## Queue Model

- Three priority lanes:
  - `PriorityImmediate`
  - `PriorityControl`
  - `PriorityData`
- Queue worker split and backoff are configured in `sender/queue.go` and `config/config.go`.

## Typical Tasks

1. Messages dropped under load:
   - inspect queue capacity/drop paths in `enqueueNow`
2. Retry behavior too aggressive or too weak:
   - inspect `handleRetry` and sender config defaults
3. Gossip endpoint mismatch:
   - inspect `gossip_sender.go` URL path and payload content-type

## Quick Commands

```bash
rg "Priority|Enqueue|workerLoop|handleRetry|TaskExpireTimeout" sender config
rg "doSend.*|Broadcast|Pull|PushQuery|Frost" sender
```

