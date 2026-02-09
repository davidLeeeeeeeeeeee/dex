---
name: Network Registry
description: Peer registry helper that loads, tracks, and updates node info records from DB.
triggers:
  - network
  - peer registry
  - node list
  - known node
---

# Network Registry Skill

Use this skill for peer map logic and node metadata persistence.

## Source Map

- Registry implementation: `network/network.go`
- Downstream users:
  - `txpool/txpool.go`
  - `txpool/txpool_queue.go`

## Core Behavior

- Loads known nodes from DB during startup.
- Stores nodes keyed by public key.
- Updates node online state and IP through `AddOrUpdateNode`.

## Typical Tasks

1. Unknown peers never become known:
   - inspect pubkey extraction path in `txpool/txpool_queue.go`
   - inspect `AddOrUpdateNode` save path in `network/network.go`
2. Node list stale/incomplete:
   - inspect initial DB load in `NewNetwork`

## Quick Commands

```bash
rg "NewNetwork|AddOrUpdateNode|IsKnownNode|GetAllNodes" network txpool
```

