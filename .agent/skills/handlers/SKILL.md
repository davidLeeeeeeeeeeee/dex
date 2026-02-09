---
name: HTTP Handlers
description: Node HTTP/3 API routing and request handlers for tx, block, orderbook, consensus, frost, and witness endpoints.
triggers:
  - handlers
  - api
  - route
  - endpoint
  - http3
---

# HTTP Handlers Skill

Use this skill for node API route changes, response format bugs, or endpoint-level behavior.

## Source Map

- Route registration: `handlers/manager.go` (`RegisterRoutes`)
- Core query/tx endpoints:
  - `handlers/handleTx.go`
  - `handlers/handleGetData.go`
  - `handlers/handleGetBlock.go`
  - `handlers/handleStatus.go`
- Orderbook/trades:
  - `handlers/handleOrderBook.go`
- Consensus/gossip endpoints:
  - `handlers/handlePush.go`
  - `handlers/handlePull.go`
  - `handlers/handleChits.go`
  - `handlers/handleBlockGossip.go`
- Frost/witness endpoints:
  - `handlers/frost_query_handlers.go`
  - `handlers/frost_admin_handlers.go`
  - `handlers/handleFrostMsg.go`

## Typical Tasks

1. Add a new endpoint:
   - implement handler in `handlers/`
   - register route in `handlers/manager.go`
2. Fix protobuf/json response mismatch:
   - inspect content-type and marshal path in relevant handler file
3. Fix API metrics visibility:
   - verify `hm.Stats.RecordAPICall(...)` usage

## Quick Commands

```bash
rg "HandleFunc\\(" handlers/manager.go
rg "RecordAPICall|http\\.Error|proto\\.Marshal|json\\.NewEncoder" handlers
```

