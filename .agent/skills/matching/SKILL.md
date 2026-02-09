---
name: Matching Engine
description: Price-time order matching, orderbook heaps, and trade generation logic.
triggers:
  - matching
  - orderbook
  - trade match
  - price heap
---

# Matching Engine Skill

Use this skill for matching rules, partial fill behavior, or orderbook data-structure bugs.

## Source Map

- Core match loop: `matching/match.go`
- Types/orderbook structs: `matching/types.go`
- Heap implementations: `matching/price_heap.go`
- Coverage:
  - `matching/matching_test.go`
  - `matching/boundary_test.go`
  - `matching/trade_sink_test.go`

## Integration Points

- VM order handler drives matching:
  - `vm/order_handler.go`
  - `vm/executor.go` (rebuild orderbooks for pairs before tx execution)
- HTTP orderbook/trades view:
  - `handlers/handleOrderBook.go`

## Typical Tasks

1. Fix wrong fill quantity or stale order:
   - inspect `match` and `executeTrade` in `matching/match.go`
2. Fix best bid/ask ordering:
   - inspect `price_heap.go` and map/heap maintenance
3. Fix replay/rebuild inconsistency:
   - inspect VM orderbook rebuild path in `vm/executor.go`

## Quick Commands

```bash
rg "AddOrder|AddOrderWithoutMatch|match|executeTrade|PruneByMarkPrice" matching vm
go test ./matching/... -v
```

